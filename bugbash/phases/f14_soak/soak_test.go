// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

//go:build bugbash
// +build bugbash

// Package f14_soak is bug-bash phase F14: a sustained mixed-workload soak that
// looks for degradation over time rather than a single-operation failure.
//
// The spec's target is 12h × 6 engines (72 engine-hours) with metrics snapshots
// every 5 min. That is a release-candidate-window run, not a CI run, so this
// phase is **time-boxed and parameterised**: it runs a short mixed workload by
// default (-soak-seconds, default 5s) and asserts the soak invariants on that
// window. Point it at the full duration + every engine for the real RC soak:
//
//	go test -tags=bugbash -run TestSoak ./phases/f14_soak/ \
//	    -engines=all -soak-seconds=43200 -timeout 13h
//
// Workload: 60% reads, 30% writes, 10% complex (a two-table JOIN), across
// several workers, with the L2 cache configured (reads use Cache()). Invariants
// checked on the window:
//   - latency does not degrade: the second half's median op latency is within a
//     generous factor of the first half's (catches runaway growth, not jitter);
//   - memory is stable: post-run heap (after GC) is within a factor of the
//     post-seed baseline (catches a leak, not normal churn);
//   - zero unexpected panics (workers recover and count them) and zero op errors.
//
// SQLite is the default (engine-agnostic workload logic, no container). The
// 5-min OTel metric snapshots are part of the full RC run, out of scope here.
package f14_soak

import (
	"context"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/bugbash/reporter"
	"github.com/jcsvwinston/quark/bugbash/tools"
	"github.com/jcsvwinston/quark/cache/memory"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/microsoft/go-mssqldb"
	_ "github.com/sijms/go-ora/v2"
	_ "modernc.org/sqlite"
)

const phase = "f14_soak"

var (
	engineFlag = flag.String("engines", "sqlite",
		"comma-separated engines (sqlite,postgres,mysql,mariadb,mssql,oracle) or 'all'")
	soakSeconds = flag.Int("soak-seconds", 5, "soak duration per engine (spec RC run: 43200 = 12h)")
	soakWorkers = flag.Int("soak-workers", 4, "concurrent workers driving the mixed workload")
)

func selectedEngines() []string {
	v := strings.TrimSpace(*engineFlag)
	if v == "" || v == "all" {
		return tools.AllEngines
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

type soakAccount struct {
	ID      int64  `db:"id" pk:"true"`
	Name    string `db:"name"`
	Balance int    `db:"balance"`
}

func (soakAccount) TableName() string { return "soak_accounts" }

type soakTxn struct {
	ID     int64 `db:"id" pk:"true"`
	AcctID int64 `db:"acct_id"`
	Amount int   `db:"amount"`
}

func (soakTxn) TableName() string { return "soak_txns" }

const seedAccounts = 200

func TestSoak(t *testing.T) {
	engines := selectedEngines()
	ctx := context.Background()

	conns, err := tools.Up(ctx, engines)
	if err != nil {
		t.Fatalf("bring up engines %v: %v", engines, err)
	}
	t.Cleanup(func() {
		var ce []string
		for _, e := range engines {
			if e != tools.SQLite {
				ce = append(ce, e)
			}
		}
		tools.Down(ce...)
	})

	for _, eng := range engines {
		conn := conns[eng]
		t.Run(eng, func(t *testing.T) { soakEngine(t, ctx, conn, eng) })
	}
}

func soakEngine(t *testing.T, ctx context.Context, conn tools.EngineConn, eng string) {
	r := newRec(t, eng)
	// SQLite is single-writer; without a busy timeout, concurrent writers fail
	// fast with SQLITE_BUSY. A busy_timeout turns contention into latency (the
	// honest soak shape), matching benchmarks/stress. Server engines: real
	// concurrency, DSN unchanged.
	dsn := conn.DSN
	maxConns := *soakWorkers
	if eng == tools.SQLite {
		dsn += "?_pragma=busy_timeout(5000)"
		maxConns = 1 // single-writer: serialize through one connection (no SQLITE_BUSY)
	}
	// Bounded, REUSED pool: MaxIdle == MaxOpen keeps the connections alive and
	// reused rather than churned. With the default MaxIdleConns(2) and N workers,
	// most worker connections open+close every op — that churn hammered the
	// Oracle listener into ORA-12516 (handler exhaustion) on the 12h RC soak. A
	// real app runs a bounded pool; the soak should too. L2 cache is on so the
	// cache path is under load (reads use Cache()).
	client, err := quark.New(conn.Driver, dsn,
		quark.WithCacheStore(memory.New()),
		quark.WithMaxOpenConns(maxConns),
		quark.WithMaxIdleConns(maxConns))
	if err != nil {
		t.Fatalf("quark.New(%q): %v", conn.Driver, err)
	}
	t.Cleanup(func() {
		_ = client.Close()
		if eng == tools.SQLite {
			_ = os.Remove(conn.DSN)
		}
	})
	if err := client.Migrate(ctx, &soakAccount{}, &soakTxn{}); err != nil {
		t.Fatalf("migrate on %s: %v", eng, err)
	}
	// Index the JOIN/WHERE column. Without it the 10% JOIN op full-scans
	// soak_txns, which grows unbounded over the soak (30% of ops are INSERTs)
	// — an O(n) cost that masquerades as a latency "regression" once the table
	// is large enough (BB-14: mysql crossed the 4x degrade gate on the 12h RC
	// run; EXPLAIN showed type=ALL, 261888 rows scanned → type=ref, 1309 with
	// this index). Indexing it keeps the soak measuring engine/ORM overhead,
	// not a self-inflicted table scan. CreateIndex is idempotent per dialect
	// (IF NOT EXISTS / 1061 / ORA-01408 swallow), so a re-run is safe.
	if err := client.CreateIndex(ctx, "soak_txns", "idx_soak_txns_acct", []string{"acct_id"}, false); err != nil {
		t.Fatalf("create index on %s: %v", eng, err)
	}

	// Seed accounts (+ one txn each) so reads/joins have data.
	accIDs := make([]int64, 0, seedAccounts)
	for i := 0; i < seedAccounts; i++ {
		a := soakAccount{Name: fmt.Sprintf("acc-%d", i), Balance: i}
		if err := quark.For[soakAccount](ctx, client).Create(&a); err != nil {
			t.Fatalf("seed account %d: %v", i, err)
		}
		accIDs = append(accIDs, a.ID)
		if err := quark.For[soakTxn](ctx, client).Create(&soakTxn{AcctID: a.ID, Amount: i}); err != nil {
			t.Fatalf("seed txn %d: %v", i, err)
		}
	}

	dur := time.Duration(*soakSeconds) * time.Second
	t.Logf("soak: %s for %s with %d workers (spec target 12h × 6 engines is RC-window-tier)", eng, dur, *soakWorkers)

	start := time.Now()
	deadline := start.Add(dur)
	runtime.GC()
	var msStart runtime.MemStats
	runtime.ReadMemStats(&msStart)

	var (
		wg         sync.WaitGroup
		mu         sync.Mutex
		firstLat   []time.Duration // ops that started in the first half of the window
		lastLat    []time.Duration // ops that started in the second half
		panics     atomic.Int64
		errs       atomic.Int64
		ops        atomic.Int64
		errSamples = map[string]int{} // distinct op-error strings (capped) → count, for diagnosis
	)
	half := start.Add(dur / 2)

	for w := 0; w < *soakWorkers; w++ {
		wg.Add(1)
		go func(seed uint64) {
			defer wg.Done()
			rng := rand.New(rand.NewPCG(seed, 0x50AC))
			for time.Now().Before(deadline) {
				// Recover PER OP, not per worker: an isolated panic is counted
				// but the worker keeps driving load — over the 12h RC run a single
				// panic must not silently remove a quarter of worker capacity.
				func() {
					defer func() {
						if p := recover(); p != nil {
							panics.Add(1)
						}
					}()
					opStart := time.Now()
					if err := doOp(ctx, client, accIDs, rng); err != nil {
						errs.Add(1)
						// Record a capped sample of distinct error strings so a
						// non-zero count is diagnosable (which ORA-/driver error?),
						// not just a number.
						s := err.Error()
						mu.Lock()
						if _, ok := errSamples[s]; ok || len(errSamples) < 20 {
							errSamples[s]++
						}
						mu.Unlock()
					}
					lat := time.Since(opStart)
					ops.Add(1)
					// Cap the per-half latency samples: an unbounded slice over
					// millions of ops would itself dominate the heap and poison
					// the memory-stability check. A bounded sample is plenty for
					// a median.
					const maxSamples = 5000
					mu.Lock()
					if opStart.Before(half) {
						if len(firstLat) < maxSamples {
							firstLat = append(firstLat, lat)
						}
					} else if len(lastLat) < maxSamples {
						lastLat = append(lastLat, lat)
					}
					mu.Unlock()
				}()
			}
		}(uint64(w + 1))
	}
	wg.Wait()

	runtime.GC()
	var msEnd runtime.MemStats
	runtime.ReadMemStats(&msEnd)

	// Zero panics, zero op errors.
	if n := panics.Load(); n != 0 {
		r.fail(reporter.SeverityP1, "%d worker panic(s) during soak", n)
	}
	if n := errs.Load(); n != 0 {
		r.fail(reporter.SeverityP1, "%d op error(s) during soak (want 0)", n)
		mu.Lock()
		for s, c := range errSamples {
			t.Logf("soak %s: error sample [%dx]: %s", eng, c, s)
		}
		mu.Unlock()
	}
	if ops.Load() == 0 {
		r.fail(reporter.SeverityP1, "no operations executed during the soak window")
		return
	}

	// Latency non-degradation: second-half median within a generous factor of
	// first-half median. Needs enough samples in both halves to be meaningful.
	if len(firstLat) >= 50 && len(lastLat) >= 50 {
		m1, m2 := median(firstLat), median(lastLat)
		const degradeFactor = 4.0
		t.Logf("soak %s: %d ops, median first-half=%s last-half=%s", eng, ops.Load(), m1, m2)
		if m1 > 0 && float64(m2) > float64(m1)*degradeFactor {
			r.fail(reporter.SeverityP1,
				"latency degraded over the soak: first-half median %s → last-half median %s (>%.0fx)", m1, m2, degradeFactor)
		}
	} else {
		t.Logf("soak %s: %d ops (too few per-half samples for a latency-trend check; raise -soak-seconds)", eng, ops.Load())
	}

	// Memory stability: post-run heap within a generous factor of the post-seed
	// baseline. Best-effort (heap is noisy on a short window): only flag a real
	// runaway — both a ≥5x factor AND an absolute floor (64 MiB), so warmup
	// churn over a tiny baseline doesn't false-positive.
	const (
		memFactor   = 5.0
		memFloorAbs = 64 << 20 // 64 MiB
	)
	t.Logf("soak %s: HeapAlloc start=%d end=%d", eng, msStart.HeapAlloc, msEnd.HeapAlloc)
	if msStart.HeapAlloc > 0 && msEnd.HeapAlloc > memFloorAbs &&
		float64(msEnd.HeapAlloc) > float64(msStart.HeapAlloc)*memFactor {
		reporter.Fail(t, reporter.Failure{
			Phase: phase, Test: "Soak", Engine: eng, Category: reporter.CategoryGap, Severity: reporter.SeverityP2,
			Error: fmt.Sprintf("heap grew %dx over the soak to %d bytes (start=%d) — possible leak",
				int(float64(msEnd.HeapAlloc)/float64(msStart.HeapAlloc)), msEnd.HeapAlloc, msStart.HeapAlloc),
		})
	}
}

// doOp runs one workload operation: 60% read, 30% write, 10% complex (JOIN).
func doOp(ctx context.Context, c *quark.Client, accIDs []int64, rng *rand.Rand) error {
	id := accIDs[rng.IntN(len(accIDs))]
	switch n := rng.IntN(10); {
	case n < 6: // 60% read (cached point lookup)
		_, err := quark.For[soakAccount](ctx, c).Where("id", "=", id).Cache(time.Second, "soak").First()
		return err
	case n < 9: // 30% write (insert a txn; invalidates the account-table cache tag)
		return quark.For[soakTxn](ctx, c).Create(&soakTxn{AcctID: id, Amount: rng.IntN(1000)})
	default: // 10% complex: two-table JOIN
		// ON is table-qualified (the join-on validator allows that); the WHERE
		// column is unqualified — the identifier guard rejects dotted names, and
		// acct_id is unambiguous (only soak_txns has it).
		_, err := quark.For[soakTxn](ctx, c).
			Join("soak_accounts").On("soak_txns.acct_id", "=", "soak_accounts.id").
			Where("acct_id", "=", id).
			Limit(20).List()
		return err
	}
}

func median(d []time.Duration) time.Duration {
	if len(d) == 0 {
		return 0
	}
	cp := append([]time.Duration(nil), d...)
	sort.Slice(cp, func(i, j int) bool { return cp[i] < cp[j] })
	return cp[len(cp)/2]
}

type rec struct {
	t   *testing.T
	eng string
}

func newRec(t *testing.T, eng string) rec { return rec{t: t, eng: eng} }

func (r rec) fail(sev reporter.Severity, format string, args ...any) {
	r.t.Helper()
	reporter.Fail(r.t, reporter.Failure{
		Phase: phase, Test: "Soak", Engine: r.eng, Category: reporter.CategoryRegression, Severity: sev,
		Error: fmt.Sprintf(format, args...),
		Reproducer: reporter.Reproducer{
			Command: "go test -tags=bugbash -run TestSoak ./phases/f14_soak/... -engines=" + r.eng,
		},
	})
}
