// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// recipeMarker is the convention: the godoc of every exported Err* sentinel
// carries a sentence that begins with this marker and says what the caller
// should do. CONTRIBUTING.md ("Error sentinels carry a recipe") and the
// public errors reference (website/docs/reference/api/errors.mdx) document
// it; this test is what counts it, so the figure the docs publish is the
// one measured here, not one written by hand.
const recipeMarker = "What to do:"

// errorsReferencePage is the public page that lists the sentinels. The test
// holds it to the same set: every sentinel is named there, and the count it
// publishes is the count found here.
const errorsReferencePage = "website/docs/reference/api/errors.mdx"

// sentinelDecl is one exported `var Err… = …` found in a public package of
// the root module, with the godoc attached to it.
type sentinelDecl struct {
	pkg  string // import path relative to the module root ("" for the root)
	name string
	pos  string // file:line
	doc  string
}

// TestEveryExportedSentinelNamesWhatToDo walks the AST of every public
// package of the root module — the root, cache/…, migrate, otel,
// quarkdriver/…, quarkmigrate, quarktenant, quarktest, seed; not internal/,
// not the sibling modules — finds every exported Err* variable (declared
// alone, inside a var (...) block, or re-exported from another package) and
// fails unless its godoc carries a recipe sentence. It prints the count so
// the number is read from the test, not estimated.
func TestEveryExportedSentinelNamesWhatToDo(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	sentinels, err := collectExportedSentinels(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(sentinels) < 20 {
		t.Fatalf("found only %d exported sentinels; the walk lost packages (the root alone declares more)", len(sentinels))
	}

	var missing []string
	byPkg := map[string]int{}
	for _, s := range sentinels {
		byPkg[s.pkg]++
		if !hasRecipe(s.doc) {
			missing = append(missing, fmt.Sprintf("%s (%s)", s.name, s.pos))
		}
	}
	withRecipe := len(sentinels) - len(missing)
	t.Logf("%d/%d exported Err* sentinels carry a %q sentence", withRecipe, len(sentinels), recipeMarker)
	pkgs := make([]string, 0, len(byPkg))
	for p := range byPkg {
		pkgs = append(pkgs, p)
	}
	sort.Strings(pkgs)
	for _, p := range pkgs {
		name := p
		if name == "" {
			name = "(root)"
		}
		t.Logf("  %-14s %d", name, byPkg[p])
	}
	if len(missing) > 0 {
		t.Errorf("%d sentinel(s) without a recipe — add a sentence beginning %q to the godoc that says what the caller should do:\n  %s",
			len(missing), recipeMarker, strings.Join(missing, "\n  "))
	}

	// The public page lists every sentinel and publishes this count.
	page, err := os.ReadFile(filepath.Join(root, errorsReferencePage))
	if err != nil {
		t.Fatalf("read %s: %v", errorsReferencePage, err)
	}
	text := string(page)
	for _, s := range sentinels {
		if !strings.Contains(text, "`"+s.name+"`") && !strings.Contains(text, s.name+" ") && !strings.Contains(text, s.name+"\n") {
			t.Errorf("%s does not name %s (%s): add it to the sentinel list", errorsReferencePage, s.name, s.pos)
		}
	}
	want := fmt.Sprintf("%d of %d exported `Err*` sentinels", withRecipe, len(sentinels))
	if !strings.Contains(text, want) {
		t.Errorf("%s does not publish the measured figure %q: update the sentence that quotes this test", errorsReferencePage, want)
	}
}

// hasRecipe reports whether a godoc carries the recipe sentence: the marker
// followed by at least a few words.
func hasRecipe(doc string) bool {
	i := strings.Index(doc, recipeMarker)
	if i < 0 {
		return false
	}
	rest := strings.TrimSpace(doc[i+len(recipeMarker):])
	return len(strings.Fields(rest)) >= 3
}

// collectExportedSentinels parses every non-test Go file of the public
// packages under root. A directory is skipped when it is internal/, a
// testdata/ or hidden directory, the website, or a nested module (it has its
// own go.mod): those are measured by their own tests, or are not published.
func collectExportedSentinels(root string) ([]sentinelDecl, error) {
	var out []sentinelDecl
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if d.IsDir() {
			if rel == "." {
				return nil
			}
			base := d.Name()
			if base == "internal" || base == "testdata" || base == "website" || base == "node_modules" ||
				strings.HasPrefix(base, ".") || strings.HasPrefix(base, "_") {
				return filepath.SkipDir
			}
			if _, statErr := os.Stat(filepath.Join(path, "go.mod")); statErr == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if perr != nil {
			return perr
		}
		pkg := filepath.ToSlash(filepath.Dir(rel))
		if pkg == "." {
			pkg = ""
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.VAR {
				continue
			}
			for _, spec := range gd.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				doc := vs.Doc
				if doc == nil && len(gd.Specs) == 1 {
					doc = gd.Doc
				}
				for _, name := range vs.Names {
					if !name.IsExported() || !strings.HasPrefix(name.Name, "Err") {
						continue
					}
					out = append(out, sentinelDecl{
						pkg:  pkg,
						name: name.Name,
						pos:  fmt.Sprintf("%s:%d", rel, fset.Position(name.Pos()).Line),
						doc:  doc.Text(),
					})
				}
			}
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].pkg != out[j].pkg {
			return out[i].pkg < out[j].pkg
		}
		return out[i].name < out[j].name
	})
	return out, err
}
