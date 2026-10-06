// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package extbench

// The guard of ADR-0029 (docs/adr/0029-extension-points-do-not-grow-in-v1.md):
// within v1 an extension point does not gain a method, and none of its
// methods changes.
//
// "Extension point" is what the contract page
// (website/docs/reference/extension-contract.mdx) marks "yes" in its
// Extension point column. The shape of each one — an interface's methods, a
// function type's signature, a record's fields, rendered as
// acceptance/apisurface.json records them — is written down in
// testdata/extension-points.txt, and TestExtensionPointsFrozen fails when the
// compiled API and that record disagree.
//
// apisurface.json freezes the same signatures, but `make regen` rewrites it:
// a method added to quarkdriver.Dialect, with the file regenerated in the same
// pull request, passes every check that file feeds. This record has no
// generator, on purpose. An entry of a type already in it changes only by
// hand, in a diff a reviewer reads, and the ADR says it changes only at v2.
// The one change it asks for rather than refuses is the one the ADR adds
// capability with — a NEW extension point, a new optional interface — and for
// that the failure prints the lines to add.
//
// It is a test of its own and not a control of the bench: the bench's
// controls measure what a third party can build, with a recorded verdict the
// umbrella's posture guard counts; this one enforces a rule, and has nothing
// to record but pass or fail.

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const (
	contractPagePath = "website/docs/reference/extension-contract.mdx"
	frozenPointsPath = "testdata/extension-points.txt"
)

// pointShapes is the shape of a set of extension points: the qualified type
// ("quarkdriver.Dialect") → member → signature. The member is a method's
// name for an interface declared in quark or quarkdriver, and "" for an
// entry that is the whole type: a function type, a record of functions, or
// an alias of an internal interface (quark.TableNamer), whose methods the
// signature spells out.
type pointShapes map[string]map[string]string

// pointKey is the first column of a line of the record.
func pointKey(typ, member string) string {
	if member == "" {
		return typ
	}
	return typ + "." + member
}

// pointLines renders one type's shape as lines of the record, by member.
func pointLines(typ string, shape map[string]string) []string {
	members := make([]string, 0, len(shape))
	for m := range shape {
		members = append(members, m)
	}
	sort.Strings(members)
	out := make([]string, 0, len(members))
	for _, m := range members {
		out = append(out, pointKey(typ, m)+"\t"+shape[m])
	}
	return out
}

// readFrozenPoints parses the record: one line per member, the key and the
// signature separated by a tab; blank lines and lines starting with # are
// skipped. The key is the qualified type, plus ".Member" for a method.
func readFrozenPoints(path string) (pointShapes, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	out := pointShapes{}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, sig, ok := strings.Cut(line, "\t")
		if !ok || sig == "" {
			return nil, fmt.Errorf("%s:%d: want \"<type>[.<member>]<TAB><signature>\", got %q", path, n, line)
		}
		parts := strings.Split(key, ".")
		if len(parts) < 2 || (parts[0] != "quark" && parts[0] != "quarkdriver") {
			return nil, fmt.Errorf("%s:%d: %q does not name a type of quark or quarkdriver", path, n, key)
		}
		typ, member := parts[0]+"."+parts[1], strings.Join(parts[2:], ".")
		if out[typ] == nil {
			out[typ] = map[string]string{}
		}
		if _, dup := out[typ][member]; dup {
			return nil, fmt.Errorf("%s:%d: %s is recorded twice", path, n, key)
		}
		out[typ][member] = sig
	}
	return out, sc.Err()
}

// currentShape is frozenShape keyed by member instead of by the symbol
// apisurface.json records: "(Dialect).Name" → "Name", "Scope" → "".
func currentShape(api *apiTypes, c censusEntry) map[string]string {
	out := map[string]string{}
	for sym, sig := range frozenShape(api, c) {
		member := ""
		if strings.HasPrefix(sym, "(") {
			_, member, _ = strings.Cut(sym, ").")
		}
		out[member] = sig
	}
	return out
}

// extensionPointDrift compares the extension points the page and the
// compiler describe today (current) with the record (frozen). plumbing names
// the implementable types the page marks "no". It returns one message per
// difference, sorted; none means the rule holds.
func extensionPointDrift(current pointShapes, plumbing map[string]bool, frozen pointShapes) []string {
	const howToGrow = "add the capability as a new optional interface that Quark checks for with a type assertion, with a default for a type that does not implement it"
	var out []string
	for typ, was := range frozen {
		now, ok := current[typ]
		switch {
		case !ok && plumbing[typ]:
			out = append(out, fmt.Sprintf("%s is recorded as an extension point and the contract page now marks it plumbing: within v1 a type does not stop being an extension point, because that would let it gain methods (ADR-0029)", typ))
			continue
		case !ok:
			out = append(out, fmt.Sprintf("%s is recorded as an extension point and is no longer one the contract page lists or a third party can implement: removing or sealing it breaks every implementation, and waits for v2 (ADR-0029)", typ))
			continue
		}
		for member, sig := range now {
			old, had := was[member]
			switch {
			case !had:
				out = append(out, fmt.Sprintf("%s gained %s %s: an extension point does not gain a method or a field within v1, since every implementation outside Quark would stop compiling (or registering) — %s (ADR-0029)", typ, member, sig, howToGrow))
			case old != sig:
				what := typ + "." + member
				if member == "" {
					what = typ
				}
				out = append(out, fmt.Sprintf("%s changed from %q to %q: an extension point keeps its shape within v1 — no method or field added, none changed; %s, and leave the change itself to v2 (ADR-0029)", what, old, sig, howToGrow))
			}
		}
		for member, sig := range was {
			if _, has := now[member]; !has {
				out = append(out, fmt.Sprintf("%s lost %s %s: removing a member breaks every caller that uses it, and waits for v2 (ADR-0029)", typ, member, sig))
			}
		}
	}
	for typ, shape := range current {
		if _, ok := frozen[typ]; ok {
			continue
		}
		out = append(out, fmt.Sprintf("%s is marked as an extension point on the contract page and has no record. A new extension point is how ADR-0029 adds capability, so this is allowed: add these lines to internal/extbench/%s in the same change —\n%s",
			typ, frozenPointsPath, strings.Join(pointLines(typ, shape), "\n")))
	}
	sort.Strings(out)
	return out
}

// TestExtensionPointsFrozen fails when an extension point of the contract
// page has a shape the record does not, or the record has one the page and
// the compiler no longer do.
func TestExtensionPointsFrozen(t *testing.T) {
	e := newEnv(t)
	api := e.loadAPI(t) // a child go command: skipped under -short, run in the full lane
	census := implementable(api)
	known := censusNames(api, census)

	page, err := os.ReadFile(filepath.Join(e.root, contractPagePath))
	if err != nil {
		t.Fatalf("read the contract page: %v", err)
	}
	current, plumbing := pointShapes{}, map[string]bool{}
	for _, rows := range stabilityTables(string(page)) {
		for _, r := range rows {
			c, ok := known[r.symbol]
			if !ok {
				continue // CON-01 reports a row that names nothing
			}
			switch r.extension {
			case "yes":
				current[c.qualified()] = currentShape(api, c)
			case "no":
				plumbing[c.qualified()] = true
			}
		}
	}
	if len(current) == 0 {
		t.Fatalf("%s marks no type as an extension point: the guard would check nothing", contractPagePath)
	}

	frozen, err := readFrozenPoints(frozenPointsPath)
	if err != nil {
		t.Fatalf("read the record: %v", err)
	}
	members := 0
	for _, shape := range frozen {
		members += len(shape)
	}
	t.Logf("extension points on the page: %d; in the record: %d, with %d members; plumbing: %d", len(current), len(frozen), members, len(plumbing))
	drift := extensionPointDrift(current, plumbing, frozen)
	for _, d := range drift {
		t.Error(d)
	}
	if len(drift) > 0 {
		return
	}

	// The record and the compiler agree; the guard must still see a method
	// added to the real Dialect, the interface every driver implements.
	grown := pointShapes{}
	for typ, shape := range current {
		grown[typ] = map[string]string{}
		for m, s := range shape {
			grown[typ][m] = s
		}
	}
	grown["quarkdriver.Dialect"]["Ping"] = "func(context.Context) error"
	if drift := extensionPointDrift(grown, plumbing, frozen); len(drift) != 1 || !strings.Contains(drift[0], "quarkdriver.Dialect gained Ping") {
		t.Errorf("a method added to quarkdriver.Dialect should be the one difference reported, got %q", drift)
	}
}

// TestExtensionPointsFrozenBites runs the comparison on a small record and
// one change at a time: each change the ADR forbids within v1 is reported,
// and the one it allows — a new extension point — is reported as lines to
// add. It needs no compiled API, so it runs in every lane, -short included.
func TestExtensionPointsFrozenBites(t *testing.T) {
	record := func() pointShapes {
		return pointShapes{
			"quarkdriver.Dialect": {
				"Name":        "func() string",
				"Placeholder": "func(int) string",
			},
			"quark.Scope":            {"": "[T any] func(*Query[T]) *Query[T]"},
			"quarkdriver.Classifier": {"": "struct{UniqueViolation func(error) bool; Deadlock func(error) bool; TransientConn func(error) bool}"},
		}
	}
	cases := []struct {
		name   string
		change func(current pointShapes, plumbing map[string]bool)
		want   string // a substring of the one message expected; "" for none
	}{
		{"nothing changed", func(pointShapes, map[string]bool) {}, ""},
		{"a method added to an interface", func(c pointShapes, _ map[string]bool) {
			c["quarkdriver.Dialect"]["Quote"] = "func(string) string"
		}, "quarkdriver.Dialect gained Quote"},
		{"a method's signature changed", func(c pointShapes, _ map[string]bool) {
			c["quarkdriver.Dialect"]["Placeholder"] = "func(int, bool) string"
		}, `quarkdriver.Dialect.Placeholder changed from "func(int) string"`},
		{"a method removed", func(c pointShapes, _ map[string]bool) {
			delete(c["quarkdriver.Dialect"], "Placeholder")
		}, "quarkdriver.Dialect lost Placeholder"},
		{"a function type's signature changed", func(c pointShapes, _ map[string]bool) {
			c["quark.Scope"][""] = "[T any] func(*Query[T]) (*Query[T], error)"
		}, "quark.Scope changed from"},
		{"a field added to the record of functions", func(c pointShapes, _ map[string]bool) {
			c["quarkdriver.Classifier"][""] = "struct{UniqueViolation func(error) bool; Deadlock func(error) bool; TransientConn func(error) bool; Timeout func(error) bool}"
		}, "quarkdriver.Classifier changed from"},
		{"an extension point marked plumbing", func(c pointShapes, p map[string]bool) {
			delete(c, "quarkdriver.Dialect")
			p["quarkdriver.Dialect"] = true
		}, "quarkdriver.Dialect is recorded as an extension point and the contract page now marks it plumbing"},
		{"an extension point gone", func(c pointShapes, _ map[string]bool) {
			delete(c, "quark.Scope")
		}, "quark.Scope is recorded as an extension point and is no longer one"},
		{"a new extension point", func(c pointShapes, _ map[string]bool) {
			c["quarkdriver.Pinger"] = map[string]string{"Ping": "func(context.Context) error"}
		}, "quarkdriver.Pinger.Ping\tfunc(context.Context) error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			current, plumbing := record(), map[string]bool{}
			tc.change(current, plumbing)
			drift := extensionPointDrift(current, plumbing, record())
			switch {
			case tc.want == "" && len(drift) != 0:
				t.Errorf("want no difference, got %q", drift)
			case tc.want != "" && (len(drift) != 1 || !strings.Contains(drift[0], tc.want)):
				t.Errorf("want one difference containing %q, got %q", tc.want, drift)
			}
		})
	}

	// The record round-trips through the file format.
	dir := t.TempDir()
	var lines []string
	for typ, shape := range record() {
		lines = append(lines, pointLines(typ, shape)...)
	}
	path := filepath.Join(dir, "points.txt")
	if err := os.WriteFile(path, []byte("# header\n\n"+strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := readFrozenPoints(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if drift := extensionPointDrift(record(), nil, got); len(drift) != 0 {
		t.Errorf("the record does not round-trip: %q", drift)
	}
	if err := os.WriteFile(path, []byte(lines[0]+"\n"+lines[0]+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readFrozenPoints(path); err == nil || !strings.Contains(err.Error(), "recorded twice") {
		t.Errorf("a line recorded twice should be refused, got %v", err)
	}
}
