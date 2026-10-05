// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package guide holds the frameworks guide to the code of the fixtures.
//
// The guide (website/docs/guides/frameworks.mdx) used to show code that
// compiled nowhere: it pointed the reader at runnable examples that had left
// the tree, and nothing noticed. Here every Go block of a section is checked
// against the package that section describes, line for line, by a test of
// that package — so a block edited on the page and not in the code, or the
// other way round, fails the fixture's own run.
//
// The comparison is exact up to indentation. The page indents with four
// spaces and gofmt with tabs, so leading tabs count as four spaces; and a
// block may be shown dedented — a handler's body without the function around
// it — as long as every line keeps the same relative indentation. There is
// no elision: a block that says "// ..." where the code says something else
// does not match, because that is how the page drifted the first time.
package guide

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Page is the guide's path from the repository root.
const Page = "website/docs/guides/frameworks.mdx"

var (
	headingRE = regexp.MustCompile(`(?m)^## `)
	goBlockRE = regexp.MustCompile("(?ms)^```go\n(.*?)^```")
)

// Check fails t unless the section of the guide under the level-two heading
// "## <heading>" shows at least one Go block, and every Go block it shows is
// code of the given files (paths relative to the test's package directory).
// heading "" names the text above the first level-two heading.
func Check(t testing.TB, heading string, files ...string) {
	t.Helper()
	raw, err := read()
	if err != nil {
		t.Fatal(err)
	}
	section, ok := Section(raw, heading)
	if !ok {
		t.Fatalf("%s has no section %q: the fixture is the code that section shows", Page, "## "+heading)
	}
	blocks := Blocks(section)
	if len(blocks) == 0 {
		t.Fatalf("the section %q of %s shows no Go block, so there is nothing to hold to this fixture", "## "+heading, Page)
	}

	var source []string
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		source = append(source, normalize(string(b))...)
		source = append(source, "") // a block never spans two files
	}

	for i, block := range blocks {
		if msg := match(normalize(block), source); msg != "" {
			t.Errorf("Go block %d of the section %q of %s is not code of %s: %s",
				i+1, "## "+heading, Page, strings.Join(files, ", "), msg)
		}
	}
}

// Section returns the text under "## <heading>" up to the next level-two
// heading, or the text above the first one when heading is "".
func Section(raw, heading string) (string, bool) {
	parts := headingRE.Split(raw, -1)
	if heading == "" {
		return parts[0], true
	}
	for _, p := range parts[1:] {
		if strings.HasPrefix(p, heading+"\n") {
			return p, true
		}
	}
	return "", false
}

// Blocks returns the bodies of the ```go fences of a section, in order.
func Blocks(section string) []string {
	var out []string
	for _, m := range goBlockRE.FindAllStringSubmatch(section, -1) {
		out = append(out, m[1])
	}
	return out
}

// read finds the guide by walking up from the working directory — the
// package directory under `go test` — to the repository root.
func read() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		b, err := os.ReadFile(filepath.Join(dir, Page))
		if err == nil {
			return string(b), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no %s above the working directory: the fixtures are checked against the guide of the tree they live in", Page)
		}
		dir = parent
	}
}

// normalize splits text into lines with leading tabs expanded to four
// spaces and trailing blanks removed, dropping the trailing empty lines.
func normalize(text string) []string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		n := len(l) - len(strings.TrimLeft(l, "\t"))
		lines[i] = strings.TrimRight(strings.Repeat("    ", n)+l[n:], " \t\r")
	}
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}

func indent(l string) int { return len(l) - len(strings.TrimLeft(l, " ")) }

// match reports "" when block occurs in source as consecutive lines, each
// shifted by the same indentation; otherwise it describes the closest place
// it nearly occurs, so a drifted block says which line drifted.
func match(block, source []string) string {
	if len(block) == 0 {
		return "the block is empty"
	}
	bestLen, bestAt, bestShift := -1, -1, 0
	for at := range source {
		shift := indent(source[at]) - indent(block[0])
		if shift < 0 || !lineEq(source[at], block[0], shift) {
			continue
		}
		n := 1
		for n < len(block) && at+n < len(source) && lineEq(source[at+n], block[n], shift) {
			n++
		}
		if n == len(block) {
			return ""
		}
		if n > bestLen {
			bestLen, bestAt, bestShift = n, at, shift
		}
	}
	if bestAt < 0 {
		return fmt.Sprintf("no line of the code reads %q", strings.TrimSpace(block[0]))
	}
	code := "(the end of the file)"
	if bestAt+bestLen < len(source) {
		code = fmt.Sprintf("%q", unshift(source[bestAt+bestLen], bestShift))
	}
	return fmt.Sprintf("its first %d lines match, then the guide says %q and the code says %s",
		bestLen, block[bestLen], code)
}

func lineEq(src, blk string, shift int) bool {
	if blk == "" {
		return src == ""
	}
	return indent(src) >= shift && src[shift:] == blk
}

func unshift(l string, shift int) string {
	if indent(l) >= shift {
		return l[shift:]
	}
	return strings.TrimLeft(l, " ")
}
