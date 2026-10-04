// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package engines

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The published page carries the recorded verdicts and ratios between two
// markers, and this test writes that block from controls() and fails when the
// page holds anything else. A table somebody retypes is a table that drifts;
// this one is the catalogue, rendered.
//
// It needs no database, so the smoke lane runs it on every pull request. To
// rewrite the block after changing the record:
//
//	QUARK_BENCH_PAGE=1 go test ./engines -run TestEngineBenchPage
const (
	pagePath   = "../../website/docs/reference/benchmarks.mdx"
	pageBegin  = "{/* engine-bench:begin — written by TestEngineBenchPage from benchmarks/engines/cases_test.go; do not edit by hand */}"
	pageEnd    = "{/* engine-bench:end */}"
	pageRewrap = "QUARK_BENCH_PAGE"
)

func TestEngineBenchPage(t *testing.T) {
	raw, err := os.ReadFile(filepath.FromSlash(pagePath))
	if err != nil {
		t.Fatalf("read the page: %v", err)
	}
	page := string(raw)
	i := strings.Index(page, pageBegin)
	j := strings.Index(page, pageEnd)
	if i < 0 || j < i {
		t.Fatalf("%s has no engine-bench block: it must hold\n%s\n…\n%s", pagePath, pageBegin, pageEnd)
	}
	have := page[i+len(pageBegin) : j]
	want := "\n\n" + renderPageBlock() + "\n"
	if have == want {
		return
	}
	if os.Getenv(pageRewrap) != "" {
		out := page[:i+len(pageBegin)] + want + page[j:]
		if err := os.WriteFile(filepath.FromSlash(pagePath), []byte(out), 0o644); err != nil {
			t.Fatalf("rewrite the page: %v", err)
		}
		t.Logf("rewrote the engine-bench block of %s", pagePath)
		return
	}
	t.Errorf("the engine-bench block of %s is not what cases_test.go records.\n"+
		"Regenerate it:\n\n\t%s=1 go test ./engines -run TestEngineBenchPage\n\nwant:\n%s",
		pagePath, pageRewrap, want)
}

// renderPageBlock is the recorded catalogue as the page shows it: a table of
// verdicts and ratios, then what each distance is made of.
func renderPageBlock() string {
	var b strings.Builder
	b.WriteString("| control | engine | operation | judged against | recorded ratio | verdict | Quark allocs/op · B/op |\n")
	b.WriteString("|---|---|---|---|---:|---|---:|\n")
	for _, c := range controls() {
		bases := make([]string, len(c.targets))
		ratios := make([]string, len(c.targets))
		for i, tg := range c.targets {
			bases[i] = armTitle(tg.base)
			ratios[i] = fmt.Sprintf("%.2f", tg.recorded)
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s | **%s** | %.0f · %.0f |\n",
			c.id, engineTitle(c.engine), mdCell(c.title), mdCell(strings.Join(bases, " · ")),
			strings.Join(ratios, " · "), c.want, c.allocs.count, c.allocs.bytes)
	}
	b.WriteString("\nWhat each distance is made of, as measured:\n\n")
	for _, c := range controls() {
		note := c.note
		if note == "" {
			note = "Within the target of every baseline it is judged against."
		}
		fmt.Fprintf(&b, "- **`%s`** (%s) — %s\n", c.id, c.op, mdCell(note))
	}
	return b.String()
}

// armTitle is an arm's name as prose shows it: the package in code font.
func armTitle(a string) string {
	return strings.Replace(a, "database/sql", "`database/sql`", 1)
}

func engineTitle(e string) string {
	switch e {
	case "postgres":
		return "PostgreSQL"
	case "mysql":
		return "MySQL"
	}
	return e
}

// mdCell keeps a pipe inside a cell from ending it, and a brace or an angle
// bracket from being read by MDX as JSX.
func mdCell(s string) string {
	r := strings.NewReplacer("|", "\\|", "\n", " ", "{", "\\{", "}", "\\}", "<", "&lt;")
	return r.Replace(s)
}
