// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package drivertemplate_test

// This file is the repository's, not the template's: it holds the guide
// "Writing a driver" to the code of this module. A driver started from the
// template deletes it.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// guidePage is the guide's path from the repository root.
const guidePage = "website/docs/guides/writing-a-driver.mdx"

var goBlock = regexp.MustCompile("(?ms)^```go\n(.*?)^```")

// TestGuideMatchesTemplate fails unless every Go block of the guide is code
// of one file of this module, line for line. The page indents with four
// spaces and gofmt with tabs, so a leading tab counts as four spaces; a block
// may be shown dedented — a function's body without the function — as long as
// every line keeps its indentation relative to the first. There is no
// elision: a block that writes "// ..." where the code says something else
// does not match.
func TestGuideMatchesTemplate(t *testing.T) {
	page, err := readGuide()
	if err != nil {
		t.Fatal(err)
	}
	blocks := goBlock.FindAllStringSubmatch(page, -1)
	if len(blocks) == 0 {
		t.Fatalf("%s shows no Go block, so there is nothing to hold to the template", guidePage)
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	sources := map[string][]string{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		sources[f] = normalize(string(b))
	}
	for i, b := range blocks {
		block := normalize(b[1])
		bestLen, best, bestFile := -1, "", ""
		for _, f := range files {
			n, msg := match(block, sources[f])
			if n > bestLen {
				bestLen, best, bestFile = n, msg, f
			}
		}
		if bestLen < len(block) {
			t.Errorf("Go block %d of %s is not code of this module; nearest, %s: %s",
				i+1, guidePage, bestFile, best)
		}
	}
}

// readGuide finds the guide by walking up from the package directory to the
// repository root.
func readGuide() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		b, err := os.ReadFile(filepath.Join(dir, guidePage))
		if err == nil {
			return string(b), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("no %s above the working directory: this test belongs to Quark's repository, and a driver started from the template deletes it", guidePage)
		}
		dir = parent
	}
}

// normalize splits text into lines with leading tabs expanded to four spaces
// and trailing blanks removed, dropping the trailing empty lines.
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

// match reports how many leading lines of block occur in source as
// consecutive lines, all shifted by the same indentation — len(block) when
// the whole block does — and, when it does not, where it nearly occurs, so a
// drifted block says which line drifted.
func match(block, source []string) (int, string) {
	if len(block) == 0 {
		return 0, "the block is empty"
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
			return n, ""
		}
		if n > bestLen {
			bestLen, bestAt, bestShift = n, at, shift
		}
	}
	if bestAt < 0 {
		return 0, fmt.Sprintf("no line of the code reads %q", strings.TrimSpace(block[0]))
	}
	code := "(the end of the file)"
	if bestAt+bestLen < len(source) {
		l := source[bestAt+bestLen]
		if indent(l) >= bestShift {
			l = l[bestShift:]
		}
		code = fmt.Sprintf("%q", l)
	}
	return bestLen, fmt.Sprintf("its first %d lines match, then the guide says %q and the code says %s", bestLen, block[bestLen], code)
}

func lineEq(src, blk string, shift int) bool {
	if blk == "" {
		return src == ""
	}
	return indent(src) >= shift && src[shift:] == blk
}
