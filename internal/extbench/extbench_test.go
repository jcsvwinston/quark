// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package extbench is the measured inventory of what a third party can build
// ON Quark today — the plugin contract, the driver kit and the framework
// integrations — with one executable probe per control and a RECORDED verdict
// the probe is checked against.
//
// It is the numerator of arc A11 ("Extensibility and catalog"), and it exists
// for the reason the four benches before it exist: the arc was written before
// anyone looked. Its three quark deliverables — a published plugin contract,
// an external driver template with a conformance kit, and official
// integrations for chi, Echo, Gin, gRPC and Nucleus — each assume something
// about the code. This bench checks the assumption before a session builds on
// it.
//
// So no control here is judged by reading. A control about an extension point
// implements it from outside package quark and drives it through the public
// API. A control about the driver contract builds a driver module that is NOT
// in this repository's module path, with no workspace, and runs it; or asks
// the build graph (`go list -deps`) what that module had to import. A control
// about a race runs the registry under the race detector in a child process,
// because whether the race detector is on in THIS process depends on which CI
// lane is running it. A control about an integration looks for a module that
// compiles against the framework and runs its tests. A probe that only greps
// a name measures the name.
//
// The test asserts the RECORDED verdict, not success. Closing a gap turns the
// suite red and asks for the verdict to be updated in the same change, so the
// numerator this bench publishes ("N of M controls present") cannot drift
// away from what it claims to measure.
//
// WHY A BENCH OF ITS OWN and not a sixth family of internal/enterprisebench:
// that bench is the numerator of A8's gate, and the umbrella's posture guard
// (check_quark_posture.sh) counts its controls and checks its page's figure
// against a floor of 69. Adding A11's controls there would move A8's
// published figure with work that is not A8's, and the guard could no longer
// say which arc a number belongs to.
//
// WHAT THIS BENCH CAN AND CANNOT SEE. Its in-process probes run on SQLite in
// the root module, so `go test ./...` exercises them on every change. The
// probes that run a child `go` command (the race detector, a standalone driver
// module, the driver modules of this repository) are skipped under -short,
// where the race lane runs, and measured in the full lane; they need the
// module cache, or the network to fill it. Nothing here certifies a dialect
// against a live engine other than SQLite — that is internal/enginesuite's
// job, and DRV-07 records that a driver outside this repository cannot reach
// it.
package extbench

import (
	"fmt"
	"sort"
	"strings"
	"testing"
)

// verdict is what a probe MEASURED, and what the case records.
type verdict string

const (
	// present: the control exists, a third party can reach it, and this
	// probe exercised it end to end.
	present verdict = "present"
	// partial: a piece of the control exists; the note says what is missing.
	partial verdict = "partial"
	// absent: nothing a third party can use. The probe measures the absence —
	// a build that fails, a race the detector reports, a module graph with no
	// module in it — never the lack of a grep hit.
	absent verdict = "absent"
)

// control is one capability of the extension surface, with the probe that
// decides its verdict.
type control struct {
	id     string  // stable id, referenced by the arc plan
	family string  // grouping for the summary table
	title  string  // what the control is, in one line
	want   verdict // the RECORDED verdict — what the bench publishes
	note   string  // for partial/absent: what exactly is missing
	probe  func(t *testing.T, e *env) verdict
}

func TestExtensionBench(t *testing.T) {
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
				"recorded verdict in the cases_*_test.go file of its family in the\n"+
				"same change, so the published numerator moves with the code instead\n"+
				"of behind it.",
				c.id, c.title, got, c.want)
		})
	}
}

// TestExtensionBenchSummary prints the table the arc plan quotes. It asserts
// nothing: TestExtensionBench is what fails when a verdict drifts.
func TestExtensionBenchSummary(t *testing.T) {
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
