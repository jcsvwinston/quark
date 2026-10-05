// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package engines

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// verdict is where an operation stands against its target.
type verdict string

const (
	// present: quark is within the limit of every baseline the control names.
	present verdict = "present"
	// partial: within the limit of some of them, not all. Only a control
	// judged against two baselines can be partial.
	partial verdict = "partial"
	// absent: within the limit of none.
	absent verdict = "absent"
)

// limit is the target every control is judged against: quark's time over the
// baseline's, at most 1.15 — "within 15 %". It is a PROPOSAL. The owner of the
// decision has not adopted it; until then the bench measures against it so
// that the work it is meant to steer has a number to move, and the page says
// that the target is still open.
const limit = 1.15

// The two tolerances, and why they are what they are.
//
// A CI runner is a shared virtual machine: a neighbour takes the CPU for a
// second, the clock of one sample stretches, and a test that compares that
// sample with a fixed number goes red for a reason that is not in the code.
// This project has paid for that lesson once already. So no assertion here
// compares a single sample with anything. Every comparison is between MEDIANS
// of per-round ratios, and every one is made with a band whose width comes
// from the run's own scatter — a noisy run widens its own band, and says by
// how much.
//
// The run's own scatter is not enough, and that was measured, not assumed.
// Within one run the per-round ratios agree closely — the median absolute
// deviation is usually under 0.03 — but WHOLE RUNS shift against each other,
// and nothing inside a run can see it. On the CI runner the shift follows the
// CPU GitHub hands out: five runs on an AMD EPYC 7763 agreed within 2 %, while
// a Xeon 8370C put MySQL's FindByPK 16 % higher and a Xeon 6973P put
// InsertOne 8 % lower. On a busy laptop, eight runs put MySQL's FindByPK
// anywhere between 1.10 and 1.40. The floors below are what covers that, and
// they were sized from those runs.
const (
	// bandFloor is the narrowest half-width the band around the LIMIT can
	// have: a ratio within ±0.10 of 1.15 is never called met or missed, it is
	// "on the threshold" and the verdict is not asserted. Measured: the
	// single-row ratios spread by up to ±0.06 around their median across
	// seven CI runs on 2026-10-04, and by up to ±0.08 across eight laptop
	// runs; across the ten CI runs of 2026-10-05, on five CPU models, by up
	// to 0.14, all of it one model (an EPYC 9V45 put FindByPK at 1.10
	// against a median of 1.24).
	bandFloor = 0.10
	// bandMADs widens the band on a noisy run: the band is at least this many
	// median absolute deviations of the per-round ratios.
	bandMADs = 2.0
	// driftFloor is the smallest relative move of a ratio, against the ratio
	// the bench RECORDS, that the test reports as a change in the code. It is
	// asserted on the reference machine only, on the CPU models the record
	// was taken on (see referenceEnv), and it has to absorb the differences
	// between those models: across the ten runs of the record, on five
	// models, the ratios moved by up to 16 % from their median. On the EPYC
	// runner, 20 % of a single-row ratio is about 30 µs of quark's time per
	// operation; a slowdown smaller than that is visible in the table, and the
	// test does not fail on it — unless it allocates, which the next check
	// sees.
	driftFloor = 0.20
	// allocTolerance is how far quark's allocations per operation may move
	// from the recorded ones. Allocations do not depend on the machine — the
	// same code, toolchain and dependencies allocate the same on an Apple
	// core and a CI runner, and from run to run they moved by under 2 % — so
	// this tolerance can be tight where the time one cannot. It is what
	// notices a regression too small for the clock: a second JSON
	// serialization of a 100-row list moved List100's ratio by 12 %, inside
	// the drift floor, and its bytes per call by 28 %.
	allocTolerance = 0.05
)

// target is one baseline a control is judged against, and the ratio the
// bench records for it.
type target struct {
	base     string  // the baseline arm
	recorded float64 // quark ÷ base, median of the reference run
}

// control is one operation, with its target(s) and what the bench records.
type control struct {
	id      string
	engine  string
	op      string // the operation's name; the operation lives in <engine>_test.go
	title   string
	targets []target
	want    verdict // the RECORDED verdict — what the bench publishes
	note    string  // for partial/absent: what the distance is made of, measured
	allocs  memory  // what quark's arm allocates per operation, recorded
}

// memory is what one operation allocates: count and bytes.
type memory struct {
	count float64 // allocs/op
	bytes float64 // B/op
}

// outcome of one comparison against the limit.
type outcome string

const (
	met       outcome = "met"
	missed    outcome = "missed"
	unsettled outcome = "on the threshold"
)

// judged is one target of one control, measured.
type judged struct {
	target
	ratio   float64 // median of the per-round ratios
	mad     float64 // their median absolute deviation
	band    float64 // half-width of the band around the limit
	outcome outcome
	drift   float64 // allowed relative move from the recorded ratio
	moved   float64 // the relative move measured: ratio/recorded - 1
}

func (j judged) drifted() bool { return math.Abs(j.moved) > j.drift }

func judge(run *opRun, c control) []judged {
	out := make([]judged, 0, len(c.targets))
	for _, tg := range c.targets {
		rs := run.ratios(armQuark, tg.base)
		j := judged{target: tg, ratio: median(rs), mad: mad(rs)}
		j.band = math.Max(bandFloor, bandMADs*j.mad)
		j.outcome = classify(j.ratio, j.band)
		j.drift = math.Max(driftFloor, bandMADs*j.mad/j.ratio)
		j.moved = j.ratio/tg.recorded - 1
		out = append(out, j)
	}
	return out
}

// classify places a ratio against the limit, with a band of ±band around it.
func classify(ratio, band float64) outcome {
	switch {
	case ratio <= limit-band:
		return met
	case ratio > limit+band:
		return missed
	default:
		return unsettled
	}
}

// possible returns every verdict the outcomes allow: a target on the
// threshold could be either, so a control with one such target has two
// possible verdicts, and the test accepts the recorded one if it is among
// them.
func possible(outcomes []outcome) []verdict {
	sure, maybe := 0, 0
	for _, o := range outcomes {
		switch o {
		case met:
			sure++
		case unsettled:
			maybe++
		}
	}
	var vs []verdict
	for m := sure; m <= sure+maybe; m++ {
		v := verdictFor(m, len(outcomes))
		if !slices.Contains(vs, v) {
			vs = append(vs, v)
		}
	}
	return vs
}

func verdictFor(metCount, n int) verdict {
	switch metCount {
	case n:
		return present
	case 0:
		return absent
	default:
		return partial
	}
}

func outcomesOf(js []judged) []outcome {
	out := make([]outcome, len(js))
	for i, j := range js {
		out[i] = j.outcome
	}
	return out
}

// TestEngineBenchCatalogue checks the catalogue itself, with no database: one
// control per operation, every target names a real baseline, every gap has a
// note, and every recorded verdict is one the recorded ratios allow. It runs
// in the smoke lane, where no engine is started.
func TestEngineBenchCatalogue(t *testing.T) {
	ops := map[string]*operation{}
	for _, op := range allOperations() {
		key := op.engine + "/" + op.name
		if ops[key] != nil {
			t.Fatalf("operation %s is defined twice", key)
		}
		if op.arm(armQuark) == nil {
			t.Fatalf("operation %s has no %q arm: there is nothing to judge", key, armQuark)
		}
		ops[key] = op
	}
	seenID := map[string]bool{}
	seenOp := map[string]string{}
	for _, c := range controls() {
		if seenID[c.id] {
			t.Fatalf("duplicate control id %q", c.id)
		}
		seenID[c.id] = true
		key := c.engine + "/" + c.op
		op := ops[key]
		if op == nil {
			t.Fatalf("%s names operation %s, which does not exist", c.id, key)
		}
		if prev, dup := seenOp[key]; dup {
			t.Fatalf("%s and %s both judge %s: one control per operation", prev, c.id, key)
		}
		seenOp[key] = c.id
		if len(c.targets) == 0 {
			t.Fatalf("%s has no target: a control with nothing to be judged against does not belong here", c.id)
		}
		if c.want != present && c.note == "" {
			t.Fatalf("%s is %s with no note: the gap has to say what it is made of", c.id, c.want)
		}
		if !(c.allocs.count > 0 && c.allocs.bytes > 0) {
			t.Fatalf("%s records no allocations for quark's arm", c.id)
		}
		outs := make([]outcome, 0, len(c.targets))
		for _, tg := range c.targets {
			if op.arm(tg.base) == nil || tg.base == armQuark {
				t.Fatalf("%s is judged against %q, which is not a baseline arm of %s", c.id, tg.base, key)
			}
			if !(tg.recorded > 0) {
				t.Fatalf("%s records no ratio against %s", c.id, tg.base)
			}
			outs = append(outs, classify(tg.recorded, bandFloor))
		}
		if vs := possible(outs); !slices.Contains(vs, c.want) {
			t.Errorf("%s records %s, but its recorded ratios allow only %v", c.id, c.want, vs)
		}
	}
	if len(referenceCPUs) == 0 {
		t.Errorf("the record names no CPU model: the time checks would never be asserted (see referenceEnv)")
	}
	for key := range ops {
		if _, ok := seenOp[key]; !ok {
			t.Errorf("operation %s has no control: an operation the bench measures and never judges is a number nobody reads", key)
		}
	}
}

// TestEngineBench measures every operation on every engine it has a DSN for
// and checks two things against the record, per control:
//
//  1. the VERDICT, wherever the ratio is off the threshold by more than its
//     band. A control whose ratio sits on the threshold is reported, not
//     asserted: the run cannot tell, and saying so is the honest answer.
//  2. the RATIO, against the recorded one, with the drift tolerance. This is
//     what notices a regression in a control that is already absent, and a
//     gain that does not cross the limit — both would leave the verdict, and
//     the page, where they were.
//
// Either failure asks for the record in cases_test.go to move in the same
// change as the code that moved it.
func TestEngineBench(t *testing.T) {
	cfg := loadConfig(t)
	byOp := map[string]control{}
	for _, c := range controls() {
		byOp[c.engine+"/"+c.op] = c
	}
	var report []reportRow
	servers := map[string]string{}
	for _, e := range engines() {
		t.Run(e.name, func(t *testing.T) {
			dsn := dsnFor(t, e)
			e.setup(t, dsn)
			admin := mustOpen(t, e.driver, dsn)
			defer admin.Close()
			var v string
			if err := admin.QueryRow(e.version).Scan(&v); err != nil {
				t.Fatalf("%s version: %v", e.name, err)
			}
			servers[e.name] = v
			for _, op := range e.ops() {
				c, ok := byOp[e.name+"/"+op.name]
				if !ok {
					t.Fatalf("operation %s/%s has no control", e.name, op.name)
				}
				t.Run(c.id+"_"+op.name, func(t *testing.T) {
					run := measure(t, op, admin, dsn, cfg)
					js := judge(run, c)
					report = append(report, reportRow{c: c, run: run, js: js})
					assertControl(t, c, js)
					assertAllocs(t, c, run)
				})
			}
		})
	}
	if len(report) == 0 {
		return
	}
	table := renderRun(report, cfg, servers)
	t.Log("\n" + table)
	if dir := os.Getenv("QUARK_BENCH_OUT"); dir != "" {
		if err := os.WriteFile(filepath.Join(dir, "bench-table.md"), []byte(table), 0o644); err != nil {
			t.Errorf("write the table: %v", err)
		}
	}
}

// onReference reports whether this run is on the machine the record was taken
// on. The time checks are asserted there and only reported elsewhere; see
// referenceEnv.
func onReference() bool { return os.Getenv(referenceEnv) != "" }

// timeAsserted reports whether this run asserts the time checks — the verdict
// and the drift of every ratio — and, when it does not, why. It does on the
// reference machine with a CPU the record was taken on (referenceCPUs), and
// nowhere else; allocations are asserted on every run regardless (QK-47).
func timeAsserted() (bool, string) {
	if !onReference() {
		return false, fmt.Sprintf("not on the reference machine (%s unset)", referenceEnv)
	}
	if cpu := cpuModel(); !slices.Contains(referenceCPUs, cpu) {
		if cpu == "" {
			cpu = "a CPU this run could not name"
		}
		return false, fmt.Sprintf("on the reference machine, but on %s, which no reference run drew (the record was taken on %s)", cpu, strings.Join(referenceCPUs, ", "))
	}
	return true, ""
}

// referenceEnv marks the reference machine: the CI workflow sets it.
//
// A ratio is portable between machines only roughly, and that was measured:
// the same code gave InsertOne 1.31 against database/sql on a CI runner
// (Xeon, 4 vCPUs) and 1.12 on a laptop (Apple M4 Pro under Docker's VM), and
// MySQL's FindByPK 1.57 against 1.23. A verdict on the limit therefore
// belongs to a machine, and the bench records the CI runner's, because that
// is where it runs on every change. Elsewhere the verdict and the drift are
// printed in the table, marked, and do not fail the test: a laptop is for
// comparing a change with itself, before and after, on the same machine.
// Allocations do not depend on the machine and are asserted everywhere.
//
// The CI runner is itself more than one machine: GitHub draws its CPU from a
// pool, and the CPU moves whole runs. Across 34 runs on 2026-10-04 and 05,
// before the record of 2026-10-05, the runs on one model agreed within 3 %
// (22 on an AMD EPYC 7763, 7 on an EPYC 9V74), while from one model to
// another List100 moved by up to 24 % and MySQL's FindByPK by up to 20 %,
// with the same allocations — enough to fail the drift check on a model no
// reference run had drawn, with nothing changed in the code (QK-47). So the time checks are
// asserted only on the models the record names (referenceCPUs); on any other
// they are reported like a laptop's, and the allocation check, which no CPU
// moves, still asserts. To make the time checks bite on a new model, take
// reference runs on it and add it to the record.
const referenceEnv = "QUARK_BENCH_REFERENCE"

func assertControl(t *testing.T, c control, js []judged) {
	t.Helper()
	fail := t.Errorf
	if ok, why := timeAsserted(); !ok {
		fail = func(format string, args ...any) {
			t.Logf("%s: reported, not asserted.\n"+format, append([]any{why}, args...)...)
		}
	}
	vs := possible(outcomesOf(js))
	if !slices.Contains(vs, c.want) {
		fail("control %s (%s) measures %s, the bench records %s.\n\n%s\n"+
			"If the code just moved, that is the point: update the recorded\n"+
			"verdict and ratios in cases_test.go in the same change, so the\n"+
			"published page moves with the code instead of behind it.",
			c.id, c.op, joinVerdicts(vs), c.want, describe(js))
	}
	for _, j := range js {
		if j.drifted() {
			fail("control %s (%s): quark ÷ %s measures %.2f, the bench records %.2f — a move of %+.0f %%, past the %.0f %% the bench tolerates.\n\n%s\n"+
				"A move this size is the code, not the machine: record the new\n"+
				"ratio in cases_test.go in the same change.",
				c.id, c.op, j.base, j.ratio, j.recorded, 100*j.moved, 100*j.drift, describe(js))
		}
	}
	if len(vs) > 1 {
		t.Logf("control %s (%s) is on the threshold: it measures %s; the recorded %s is among them, and is not asserted further", c.id, c.op, joinVerdicts(vs), c.want)
	}
}

// measuredAllocs is the median of what quark's arm allocated per operation.
func measuredAllocs(run *opRun) memory {
	q := run.arm(armQuark)
	return memory{count: median(q.allocs), bytes: median(q.bytes)}
}

func assertAllocs(t *testing.T, c control, run *opRun) {
	t.Helper()
	got := measuredAllocs(run)
	check := func(what string, got, want float64) {
		if moved := got/want - 1; math.Abs(moved) > allocTolerance {
			t.Errorf("control %s (%s): quark allocates %.0f %s per operation, the bench records %.0f — a move of %+.0f %%, past the %.0f %% the bench tolerates.\n\n"+
				"Allocations do not depend on the machine: this is the code, the toolchain or a\n"+
				"dependency. Record the new figure in cases_test.go in the same change.",
				c.id, c.op, got, what, want, 100*moved, 100*allocTolerance)
		}
	}
	check("times", got.count, c.allocs.count)
	check("bytes", got.bytes, c.allocs.bytes)
}

func joinVerdicts(vs []verdict) string {
	s := make([]string, len(vs))
	for i, v := range vs {
		s[i] = string(v)
	}
	return strings.Join(s, " or ")
}

func describe(js []judged) string {
	var b strings.Builder
	for _, j := range js {
		fmt.Fprintf(&b, "  quark ÷ %-32s %.2f (±%.2f band, MAD %.3f) — %s; recorded %.2f\n",
			j.base, j.ratio, j.band, j.mad, j.outcome, j.recorded)
	}
	return b.String()
}

func allocMark(got, want float64) string {
	if moved := got/want - 1; math.Abs(moved) > allocTolerance {
		return fmt.Sprintf(" (moved %+.0f %% ✗)", 100*moved)
	}
	return ""
}

// reportRow is one control as measured, for the table.
type reportRow struct {
	c   control
	run *opRun
	js  []judged
}

// renderRun writes the markdown table of a run: what the CI lane publishes as
// its step summary and artifact, and what a reference run is copied from.
func renderRun(rows []reportRow, cfg config, servers map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "## Engine bench — %s\n\n", time.Now().UTC().Format("2006-01-02 15:04 MST"))
	names := make([]string, 0, len(servers))
	for _, e := range engines() {
		if v, ok := servers[e.name]; ok {
			names = append(names, engineTitle(e.name)+" "+v)
		}
	}
	fmt.Fprintf(&b, "Machine: %s. Engines: %s.\n\n", machine(), strings.Join(names, ", "))
	if ok, why := timeAsserted(); ok {
		b.WriteString("This is the reference machine, on a CPU the record was taken on: verdicts, ratios and allocations are asserted against the record.\n\n")
	} else {
		fmt.Fprintf(&b, "This run is %s: verdicts and ratios are compared with the record and marked ✗ where they differ, but only allocations are asserted.\n\n", why)
	}
	fmt.Fprintf(&b, "Medians of %d rounds; a round is about %s of each arm, in blocks of %s interleaved across the arms. Target (proposed, not adopted): quark within %.0f %% of every baseline a control names. "+
		"A ratio within its band of %.2f is on the threshold and its verdict is not asserted.\n\n",
		cfg.rounds, cfg.sample, cfg.block, 100*(limit-1), limit)

	b.WriteString("| control | operation | arm | time/op | allocs/op | B/op |\n|---|---|---|---:|---:|---:|\n")
	for _, r := range rows {
		for i, a := range r.run.arms {
			id, op := "", ""
			if i == 0 {
				id, op = "`"+r.c.id+"`", r.c.engine+" "+r.c.op
			}
			fmt.Fprintf(&b, "| %s | %s | %s | %s | %.0f | %.0f |\n",
				id, op, a.name, human(median(a.ns)), median(a.allocs), median(a.bytes))
		}
	}

	b.WriteString("\n| control | operation | quark allocs/op | recorded | quark B/op | recorded |\n|---|---|---:|---:|---:|---:|\n")
	for _, r := range rows {
		got := measuredAllocs(r.run)
		fmt.Fprintf(&b, "| `%s` | %s %s | %.0f | %.0f%s | %.0f | %.0f%s |\n", r.c.id, r.c.engine, r.c.op,
			got.count, r.c.allocs.count, allocMark(got.count, r.c.allocs.count),
			got.bytes, r.c.allocs.bytes, allocMark(got.bytes, r.c.allocs.bytes))
	}

	b.WriteString("\n| control | operation | quark ÷ baseline | band | outcome | recorded | verdict | recorded verdict |\n|---|---|---|---:|---|---:|---|---|\n")
	for _, r := range rows {
		vs := possible(outcomesOf(r.js))
		for i, j := range r.js {
			id, op, v, want := "", "", "", ""
			if i == 0 {
				id, op = "`"+r.c.id+"`", r.c.engine+" "+r.c.op
				v, want = joinVerdicts(vs), string(r.c.want)
				if !slices.Contains(vs, r.c.want) {
					want += " ✗"
				}
			}
			moved := ""
			if j.drifted() {
				moved = fmt.Sprintf(" (moved %+.0f %% ✗)", 100*j.moved)
			}
			fmt.Fprintf(&b, "| %s | %s | %.2f × %s | ±%.2f | %s | %.2f%s | %s | %s |\n",
				id, op, j.ratio, j.base, j.band, j.outcome, j.recorded, moved, v, want)
		}
	}
	return b.String()
}
