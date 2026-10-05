// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package guide

import (
	"strings"
	"testing"
)

const code = "package x\n\nfunc f() {\n\tif ok {\n\t\treturn\n\t}\n\n\tg()\n}\n"

func TestMatch(t *testing.T) {
	src := normalize(code)
	for _, tc := range []struct {
		name, block, want string // want: "" for a match, else a substring of the reason
	}{
		{"whole function", "func f() {\n    if ok {\n        return\n    }\n\n    g()\n}\n", ""},
		{"dedented body", "if ok {\n    return\n}\n\ng()\n", ""},
		{"a line the code does not have", "if ok {\n    return nil\n}\n", `the guide says "    return nil" and the code says "    return"`},
		{"an elision", "func f() {\n    // ...\n}\n", `the guide says "    // ..."`},
		{"a first line nowhere", "h()\n", `no line of the code reads "h()"`},
		{"indentation that changes", "if ok {\nreturn\n}\n", "its first 1 lines match"},
		{"a blank line the code does not have", "if ok {\n\n    return\n}\n", `the guide says ""`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := match(normalize(tc.block), src)
			if tc.want == "" && got != "" {
				t.Fatalf("want a match, got %q", got)
			}
			if tc.want != "" && !strings.Contains(got, tc.want) {
				t.Fatalf("want a reason containing %q, got %q", tc.want, got)
			}
		})
	}
}

func TestSection(t *testing.T) {
	page := "# T\n\nintro\n\n## chi\n\n```go\na()\n```\n\n## Echo\n\n```go\nb()\n```\n```bash\nc\n```\n"
	if s, ok := Section(page, "chi"); !ok || len(Blocks(s)) != 1 || Blocks(s)[0] != "a()\n" {
		t.Fatalf("chi section: %q %v", s, ok)
	}
	if s, _ := Section(page, "Echo"); len(Blocks(s)) != 1 {
		t.Fatalf("Echo section has one Go block and one bash block, got Go blocks %q", Blocks(s))
	}
	if _, ok := Section(page, "Gin"); ok {
		t.Fatal("a heading the page does not have was found")
	}
	if _, ok := Section(page, "ch"); ok {
		t.Fatal("a heading matched by prefix")
	}
}
