// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package enterprisebench is the measured inventory of what Quark can do
// today as the data layer of an enterprise application — one executable probe
// per control, and a RECORDED verdict the probe is checked against.
//
// It exists because the arc that owns this ground (A8, "Quark enterprise")
// was written months before anyone looked. Its three headline items turned
// out to be two capabilities that already shipped and one that does not mean
// what the plan assumed. That is the rule, not the exception: the three times
// an arc was planned without measuring, the measurement corrected the plan.
//
// So no control here is judged by reading. Each one RUNS: it opens a client
// and drives the public API, reads the SQL the builder actually emitted
// through a query observer, or asks for a capability and reads the error it
// gets back. A capability that exists only in an internal package, or only in
// a comment, is not present — an application cannot reach either.
//
// The test asserts the RECORDED verdict, not success. Closing a gap turns the
// suite red and asks for the verdict to be updated in the same change, so the
// numerator this bench publishes ("N of M controls present") cannot drift
// away from the truth it claims to measure: a number nobody maintains is a
// number that stops being true in silence.
//
// WHAT THIS BENCH CAN AND CANNOT SEE. It runs on SQLite, in the root module,
// so `go test ./...` exercises it on every change. That is enough to measure
// a surface, a contract and a refusal — whether the builder can emit a clause
// at all, whether a capability is reachable, what a dialect that lacks a
// feature answers. It is NOT enough to certify behaviour against PostgreSQL,
// MySQL, SQL Server or Oracle: where a control needs a live engine to go
// further, its note says so, and internal/enginesuite is where that proof
// lives.
package enterprisebench

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// verdict is what a probe MEASURED, and what the case records.
type verdict string

const (
	// present: the control exists, an application can reach it, and this
	// probe exercised it end to end.
	present verdict = "present"
	// partial: a piece of the control exists; the note says what is missing —
	// an engine, a path, a part of the contract.
	partial verdict = "partial"
	// absent: no surface an application can use. The probe measures the
	// absence — an error returned, a clause the builder never emits, a value
	// dropped on the way to the database — never the lack of a grep hit.
	absent verdict = "absent"
)

// control is one capability of the enterprise data surface, with the probe
// that decides its verdict.
type control struct {
	id     string  // stable id, referenced by the arc plan and the registry
	family string  // grouping for the summary table
	title  string  // what the control is, in one line
	want   verdict // the RECORDED verdict — what the bench publishes
	note   string  // for partial/absent: what exactly is missing
	probe  func(t *testing.T, e *env) verdict
}

func TestEnterpriseBench(t *testing.T) {
	cases := controls()
	seen := map[string]bool{}
	for _, c := range cases {
		if seen[c.id] {
			t.Fatalf("duplicate control id %q", c.id)
		}
		seen[c.id] = true
		if c.probe == nil {
			t.Fatalf("%s has no probe: a control that cannot be measured does not belong in the bench", c.id)
		}
		if c.want != present && c.note == "" {
			t.Fatalf("%s is %s with no note: the gap has to say what is missing", c.id, c.want)
		}
	}

	e := newEnv(t)
	for _, c := range cases {
		t.Run(c.id, func(t *testing.T) {
			got := c.probe(t, e)
			if got == c.want {
				return
			}
			t.Errorf("control %s (%s) measures %q, the bench records %q.\n\n"+
				"If the control just gained ground, that is the point: update the\n"+
				"recorded verdict in enterprisebench_cases_test.go in the same\n"+
				"change, so the published numerator moves with the code instead of\n"+
				"behind it.",
				c.id, c.title, got, c.want)
		})
	}
}

// TestEnterpriseBenchSummary prints the table the arc plan quotes. It asserts
// nothing: TestEnterpriseBench is what fails when a verdict drifts.
func TestEnterpriseBenchSummary(t *testing.T) {
	cases := controls()
	byFamily := map[string][]control{}
	for _, c := range cases {
		byFamily[c.family] = append(byFamily[c.family], c)
	}
	families := make([]string, 0, len(byFamily))
	for f := range byFamily {
		families = append(families, f)
	}
	sort.Strings(families)

	var b strings.Builder
	total := map[verdict]int{}
	for _, f := range families {
		count := map[verdict]int{}
		for _, c := range byFamily[f] {
			count[c.want]++
			total[c.want]++
		}
		b.WriteString(fmt.Sprintf("%-14s present %2d · partial %2d · absent %2d\n",
			f, count[present], count[partial], count[absent]))
	}
	b.WriteString(fmt.Sprintf("%-14s present %2d · partial %2d · absent %2d  (of %d)\n",
		"TOTAL", total[present], total[partial], total[absent], len(cases)))
	t.Log("\n" + b.String())
}
