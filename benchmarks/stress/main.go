// Command stress is the F6-9 load / stress harness for Quark.
//
// It drives a configurable number of concurrent workers against a Quark
// Client for a fixed duration, mixing reads and writes, and reports latency
// percentiles (p50/p95/p99/max), throughput, error and contention counts, and
// connection-pool statistics. The goal is not a micro-benchmark of one
// operation (that is benchmarks/), but to surface the FIRST real bottleneck
// under concurrency — pool contention, write serialization, lock waits — so
// post-1.0 optimization effort is spent where it matters.
//
// Reproducible run (SQLite in-memory, the CI-friendly default):
//
//	go run ./stress
//
// Against PostgreSQL (or any engine Quark supports), point it at a DSN:
//
//	go run ./stress -driver pgx -dsn "postgres://user:pass@localhost/db?sslmode=disable" -conns 16 -workers 64
//
// SQLite note: the in-memory shared-cache database serializes writes (one
// writer at a time). The default DSN sets busy_timeout so contention shows up
// as write LATENCY rather than immediate SQLITE_BUSY errors — which is the
// honest shape of the bottleneck. Raise -workers / -write-pct to see it grow.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/rand"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/benchmarks/internal/model"

	_ "modernc.org/sqlite"
)

type config struct {
	driver   string
	dsn      string
	conns    int
	workers  int
	duration time.Duration
	writePct int
	seedRows int
}

// result is one worker's accumulated measurements; merged at the end so the
// hot loop never touches shared state (no lock contention from the harness
// itself skewing the numbers).
type result struct {
	readLat  []time.Duration
	writeLat []time.Duration
	errs     int
	contend  int // subset of errs that look like lock/contention failures
}

func main() {
	cfg := parseFlags()

	client, err := quark.New(cfg.driver, cfg.dsn,
		quark.WithMaxOpenConns(cfg.conns),
		quark.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
	)
	if err != nil {
		fail("open client: %v", err)
	}
	defer func() { _ = client.Close() }()

	ctx := context.Background()
	if err := client.Migrate(ctx, &model.BenchUser{}); err != nil {
		fail("migrate: %v", err)
	}
	seed(ctx, client, cfg.seedRows)

	results := runWorkload(ctx, client, cfg)
	report(client, cfg, results)
}

func parseFlags() config {
	var cfg config
	flag.StringVar(&cfg.driver, "driver", "sqlite", "database driver (sqlite, pgx, mysql, ...)")
	flag.StringVar(&cfg.dsn, "dsn", "", "data source name; empty = in-memory SQLite with busy_timeout")
	flag.IntVar(&cfg.conns, "conns", 8, "max open connections (pool size)")
	flag.IntVar(&cfg.workers, "workers", 16, "concurrent worker goroutines")
	flag.DurationVar(&cfg.duration, "duration", 5*time.Second, "how long to run the workload")
	flag.IntVar(&cfg.writePct, "write-pct", 20, "percentage of operations that are writes (0-100)")
	flag.IntVar(&cfg.seedRows, "seed", model.SeedRows, "rows to pre-load before the run")
	flag.Parse()

	if cfg.dsn == "" {
		// Shared-cache in-memory DB so the pool's connections see the same
		// data; busy_timeout turns write contention into latency, not errors.
		cfg.dsn = "file:quark_stress?mode=memory&cache=shared&_pragma=busy_timeout(5000)"
	}
	if cfg.writePct < 0 || cfg.writePct > 100 {
		fail("write-pct must be 0-100, got %d", cfg.writePct)
	}
	if cfg.workers < 1 || cfg.conns < 1 {
		fail("workers and conns must be >= 1")
	}
	return cfg
}

func seed(ctx context.Context, client *quark.Client, n int) {
	users := make([]*model.BenchUser, n)
	for i := range users {
		u := model.MakeUser(i)
		users[i] = &u
	}
	if err := quark.For[model.BenchUser](ctx, client).CreateBatch(users); err != nil {
		fail("seed: %v", err)
	}
}

func runWorkload(ctx context.Context, client *quark.Client, cfg config) []result {
	runCtx, cancel := context.WithTimeout(ctx, cfg.duration)
	defer cancel()

	results := make([]result, cfg.workers)
	var wg sync.WaitGroup
	for w := 0; w < cfg.workers; w++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(int64(idx) + 1))
			r := &results[idx]
			for runCtx.Err() == nil {
				if rng.Intn(100) < cfg.writePct {
					dur, err := doWrite(runCtx, client, cfg, rng)
					r.record(true, dur, err)
				} else {
					dur, err := doRead(runCtx, client, cfg, rng)
					r.record(false, dur, err)
				}
			}
		}(w)
	}
	wg.Wait()
	return results
}

// record adds one operation's latency and error classification. dur is the
// measured latency; err is nil on success.
func (r *result) record(write bool, dur time.Duration, err error) {
	if err != nil {
		// context deadline at run end is not a real failure — drop it.
		if ctxExpired(err) {
			return
		}
		r.errs++
		if looksLikeContention(err) {
			r.contend++
		}
		return
	}
	if write {
		r.writeLat = append(r.writeLat, dur)
	} else {
		r.readLat = append(r.readLat, dur)
	}
}

func doRead(ctx context.Context, client *quark.Client, cfg config, rng *rand.Rand) (time.Duration, error) {
	id := int64(rng.Intn(cfg.seedRows) + 1)
	start := time.Now()
	var err error
	// Two-thirds point reads, one-third range scans — a typical read shape.
	if rng.Intn(3) == 0 {
		_, err = quark.For[model.BenchUser](ctx, client).
			Where("age", ">=", model.MinAge).
			Limit(model.ListLimit).
			List()
	} else {
		_, err = quark.For[model.BenchUser](ctx, client).Find(id)
	}
	return time.Since(start), err
}

func doWrite(ctx context.Context, client *quark.Client, cfg config, rng *rand.Rand) (time.Duration, error) {
	u := model.BenchUser{ID: int64(rng.Intn(cfg.seedRows) + 1), Age: model.MinAge + rng.Intn(50)}
	start := time.Now()
	_, err := quark.For[model.BenchUser](ctx, client).UpdateFields(&u, "age")
	return time.Since(start), err
}

func report(client *quark.Client, cfg config, results []result) {
	var readLat, writeLat []time.Duration
	var errs, contend int
	for i := range results {
		readLat = append(readLat, results[i].readLat...)
		writeLat = append(writeLat, results[i].writeLat...)
		errs += results[i].errs
		contend += results[i].contend
	}
	totalOps := len(readLat) + len(writeLat)
	opsPerSec := float64(totalOps) / cfg.duration.Seconds()

	fmt.Printf("Quark stress run (F6-9)\n")
	fmt.Printf("  driver=%s conns=%d workers=%d duration=%s write-pct=%d seed=%d\n\n",
		cfg.driver, cfg.conns, cfg.workers, cfg.duration, cfg.writePct, cfg.seedRows)

	fmt.Printf("  throughput : %d ops in %s = %.0f ops/sec\n", totalOps, cfg.duration, opsPerSec)
	fmt.Printf("  errors     : %d (%d contention/lock)\n\n", errs, contend)

	printLatency("read ", readLat)
	printLatency("write", writeLat)

	s := client.Raw().Stats()
	fmt.Printf("\n  pool: maxOpen=%d inUse=%d idle=%d waitCount=%d waitTotal=%s\n",
		s.MaxOpenConnections, s.InUse, s.Idle, s.WaitCount, s.WaitDuration)
	if s.WaitCount > 0 {
		fmt.Printf("        avgWaitPerBlock=%s  <-- pool saturation: workers blocked waiting for a connection\n",
			s.WaitDuration/time.Duration(s.WaitCount))
	}
}

func printLatency(label string, lat []time.Duration) {
	if len(lat) == 0 {
		fmt.Printf("  %s: (no successful ops)\n", label)
		return
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	fmt.Printf("  %s: n=%-7d p50=%-10s p95=%-10s p99=%-10s max=%s\n",
		label, len(lat), pct(lat, 50), pct(lat, 95), pct(lat, 99), lat[len(lat)-1])
}

// pct returns the p-th percentile of a sorted slice (nearest-rank: rank =
// ceil(p/100 * n), 0-indexed rank-1). The integer ceiling is written
// (p*n + 99) / 100 - 1 to avoid a math import.
func pct(sorted []time.Duration, p int) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	idx := (p*len(sorted)+99)/100 - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}

// ctxExpired reports whether err is the run-end context cancellation rather
// than a real failure. It uses errors.Is so it works through Quark's error
// wrapping (wrapDBError joins the driver error, which carries the context
// sentinel) on every engine — not just SQLite, whose driver happens to put
// the canonical string in err.Error(). Note: it deliberately does NOT match
// quark.ErrTimeout, which also covers genuine DB/pool timeouts worth counting.
func ctxExpired(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}

// looksLikeContention coarsely classifies lock/contention errors across
// engines (the precise classifier isDeadlock is internal to quark). Used only
// for the harness's contention counter, not for control flow.
func looksLikeContention(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"busy", "locked", "deadlock", "lock wait", "timeout"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "stress: "+format+"\n", args...)
	os.Exit(1)
}
