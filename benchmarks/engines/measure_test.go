// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package engines

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"os"
	"runtime"
	"sort"
	"strconv"
	"testing"
	"time"
)

// stepFunc runs ONE iteration of an operation on one arm. i is the arm's own
// running counter, so an arm reads a different row, or writes a different
// name, on every call.
type stepFunc func(ctx context.Context, i int) error

// arm is one way of doing an operation: a baseline (database/sql, pgx) or the
// subject (quark). open builds the arm's own pool or client and returns the
// step; whatever it opens is closed by tb's cleanup.
type arm struct {
	name string
	open func(tb testing.TB, dsn string) stepFunc
}

// operation is one thing an application does, done every way the bench
// compares. reset, when set, is a statement run on a connection of the
// bench's own before every block and outside the clock: the write operations
// empty their table there, so no block pays for the rows an earlier one left
// behind.
type operation struct {
	engine string // "postgres" or "mysql"
	name   string // the published name: InsertOne, FindByPK, ...
	what   string // what one iteration does, in one line
	reset  string
	arms   []arm
}

func (op *operation) arm(name string) *arm {
	for i := range op.arms {
		if op.arms[i].name == name {
			return &op.arms[i]
		}
	}
	return nil
}

// config is how long the bench measures. The defaults are what run.sh and the
// CI lane use; both knobs are environment variables so a local run can trade
// time for a tighter band.
type config struct {
	rounds int           // QUARK_BENCH_ROUNDS: samples per arm
	sample time.Duration // QUARK_BENCH_SAMPLE: clock time of one sample, per arm
	block  time.Duration // QUARK_BENCH_BLOCK: a sample is cut into blocks of about this long, interleaved with the other arms
	warmup time.Duration // per arm, before calibration: pools, statement caches, the planner
}

func loadConfig(tb testing.TB) config {
	c := config{rounds: 10, sample: 200 * time.Millisecond, block: 20 * time.Millisecond, warmup: 300 * time.Millisecond}
	if v := os.Getenv("QUARK_BENCH_ROUNDS"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 3 {
			tb.Fatalf("QUARK_BENCH_ROUNDS=%q: want an integer >= 3 (a median of fewer is not a median)", v)
		}
		c.rounds = n
	}
	if v := os.Getenv("QUARK_BENCH_BLOCK"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < time.Millisecond {
			tb.Fatalf("QUARK_BENCH_BLOCK=%q: want a duration >= 1ms", v)
		}
		c.block = d
	}
	if v := os.Getenv("QUARK_BENCH_SAMPLE"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < c.block {
			tb.Fatalf("QUARK_BENCH_SAMPLE=%q: want a duration >= %s", v, c.block)
		}
		c.sample = d
	}
	return c
}

// armRun is what the rounds measured for one arm: one entry per round.
type armRun struct {
	name   string
	iters  int       // iterations per block, fixed by calibration
	ns     []float64 // ns/op, per round
	allocs []float64 // allocs/op, per round
	bytes  []float64 // B/op, per round
}

// opRun is one operation measured: every arm, every round.
type opRun struct {
	op     *operation
	rounds int
	arms   []*armRun
}

func (r *opRun) arm(name string) *armRun {
	for _, a := range r.arms {
		if a.name == name {
			return a
		}
	}
	return nil
}

// ratios returns subject/base for each round. Both samples of a round were
// taken within one rotation of the arms, so whatever the machine was doing
// in that second it did to both.
func (r *opRun) ratios(subject, base string) []float64 {
	s, b := r.arm(subject), r.arm(base)
	out := make([]float64, r.rounds)
	for i := range out {
		out[i] = s.ns[i] / b.ns[i]
	}
	return out
}

// measure runs an operation: warm every arm, calibrate how many iterations
// make a block of cfg.block on each, then take cfg.rounds samples per arm.
//
// A sample is not taken in one piece. It is cut into blocks, and the blocks of
// all the arms alternate — quark, database/sql, pgx, quark, database/sql, … —
// with the order rotating every block, so the arms' samples cover the SAME
// stretch of wall clock. A machine that slows down for 50 ms slows a block of
// every arm, not the whole sample of one; on a shared runner, that is the
// difference between a ratio and a coin toss. Measured on the single-row
// operations: with one 200 ms piece per arm, the per-round ratios scattered
// several times more than with 20 ms blocks.
//
// Before every block: the reset statement, if any, and a garbage collection,
// as testing.B does before every run, so that the garbage one arm leaves is
// not collected on the next arm's clock. Both are outside the clock.
func measure(t *testing.T, op *operation, admin *sql.DB, dsn string, cfg config) *opRun {
	t.Helper()
	ctx := context.Background()
	k := len(op.arms)
	steps := make([]stepFunc, k)
	for j, a := range op.arms {
		steps[j] = a.open(t, dsn)
	}
	counters := make([]int, k)
	run := &opRun{op: op, rounds: cfg.rounds, arms: make([]*armRun, k)}
	reset := func() {
		if op.reset == "" {
			return
		}
		if _, err := admin.ExecContext(ctx, op.reset); err != nil {
			t.Fatalf("%s reset: %v", op.name, err)
		}
	}

	for j, a := range op.arms {
		reset()
		start := time.Now()
		iters := 0
		for time.Since(start) < cfg.warmup || iters < 5 {
			if err := steps[j](ctx, counters[j]); err != nil {
				t.Fatalf("%s/%s warm-up: %v", op.name, a.name, err)
			}
			counters[j]++
			iters++
		}
		per := time.Since(start) / time.Duration(iters)
		n := int(cfg.block / per)
		if n < 1 {
			n = 1
		}
		run.arms[j] = &armRun{name: a.name, iters: n}
	}

	blocks := int(cfg.sample / cfg.block)
	for r := 0; r < cfg.rounds; r++ {
		var ns, allocs, bytes, iters = make([]float64, k), make([]float64, k), make([]float64, k), make([]float64, k)
		for blk := 0; blk < blocks; blk++ {
			for jj := 0; jj < k; jj++ {
				j := (jj + blk + r) % k
				a := run.arms[j]
				reset()
				runtime.GC()
				var m0, m1 runtime.MemStats
				runtime.ReadMemStats(&m0)
				start := time.Now()
				for i := 0; i < a.iters; i++ {
					if err := steps[j](ctx, counters[j]); err != nil {
						t.Fatalf("%s/%s round %d: %v", op.name, a.name, r, err)
					}
					counters[j]++
				}
				elapsed := time.Since(start)
				runtime.ReadMemStats(&m1)
				ns[j] += float64(elapsed.Nanoseconds())
				allocs[j] += float64(m1.Mallocs - m0.Mallocs)
				bytes[j] += float64(m1.TotalAlloc - m0.TotalAlloc)
				iters[j] += float64(a.iters)
			}
		}
		for j, a := range run.arms {
			a.ns = append(a.ns, ns[j]/iters[j])
			a.allocs = append(a.allocs, allocs[j]/iters[j])
			a.bytes = append(a.bytes, bytes[j]/iters[j])
		}
	}
	return run
}

// median of xs; xs is not modified.
func median(xs []float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	m := len(s) / 2
	if len(s)%2 == 1 {
		return s[m]
	}
	return (s[m-1] + s[m]) / 2
}

// mad is the median absolute deviation from the median: how far a typical
// round strays from the median round. Unlike a standard deviation it is not
// moved by the one round a CI neighbour stole the CPU in.
func mad(xs []float64) float64 {
	m := median(xs)
	dev := make([]float64, len(xs))
	for i, x := range xs {
		dev[i] = math.Abs(x - m)
	}
	return median(dev)
}

// human renders ns as the unit a reader of the table expects.
func human(ns float64) string {
	switch {
	case ns >= 1e6:
		return fmt.Sprintf("%.2f ms", ns/1e6)
	default:
		return fmt.Sprintf("%.1f µs", ns/1e3)
	}
}
