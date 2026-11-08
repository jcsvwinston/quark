// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package render

import (
	"context"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
)

var update = flag.Bool("update", false, "rewrite the static example in website/docs/playground.mdx")

func newRenderer(t *testing.T) *Renderer {
	t.Helper()
	r, err := New()
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestDefaultSpecRendersEveryDialect(t *testing.T) {
	res := newRenderer(t).Render(DefaultSpec())
	if res.Error != "" {
		t.Fatalf("default spec: %s", res.Error)
	}
	want := map[string]string{
		"postgres": `LIMIT 10 OFFSET 20`,
		"mysql":    `LIMIT 20, 10`,
		"mariadb":  `LIMIT 10 OFFSET 20`,
		"sqlite":   `LIMIT 10 OFFSET 20`,
		"mssql":    `OFFSET 20 ROWS FETCH NEXT 10 ROWS ONLY`,
		"oracle":   `OFFSET 20 ROWS FETCH NEXT 10 ROWS ONLY`,
	}
	placeholder := map[string]string{"postgres": "$1", "mysql": "?", "mariadb": "?", "sqlite": "?", "mssql": "@p1", "oracle": ":1"}
	if len(res.Dialects) != 6 {
		t.Fatalf("got %d dialects, want 6", len(res.Dialects))
	}
	for _, d := range res.Dialects {
		if !strings.Contains(d.SQL, want[d.Name]) {
			t.Errorf("%s: %q lacks %q", d.Name, d.SQL, want[d.Name])
		}
		if !strings.Contains(d.SQL, placeholder[d.Name]) {
			t.Errorf("%s: %q lacks placeholder %q", d.Name, d.SQL, placeholder[d.Name])
		}
		if !strings.Contains(d.SQL, "IS NULL") {
			t.Errorf("%s: the soft-delete predicate is missing from %q", d.Name, d.SQL)
		}
		if len(d.Args) != 2 || d.Args[0] != "ES" {
			t.Errorf("%s: args = %v, want [ES <time>]", d.Name, d.Args)
		}
	}
	if !strings.HasPrefix(res.Go, "quark.For[User](ctx, client).") || !strings.HasSuffix(res.Go, "List()") {
		t.Errorf("Go chain does not read as a For…List chain:\n%s", res.Go)
	}
}

// The four plain models render through one generic instantiation (the row
// shape plus FromTable) to keep the .wasm small. This is the proof that the
// SQL shown is the SQL the model type itself would emit.
func TestRowShapeRendersTheModelsSQL(t *testing.T) {
	r := newRenderer(t)
	specs := map[string][]Spec{
		"Order": {
			{Model: "Order", Limit: 10},
			{Model: "Order", Columns: []string{"id", "total"}, Filters: []Filter{{"status", "=", "paid"}, {"total", ">", "99.5"}}, OrderBy: []Sort{{"placed_at", "DESC"}}, Limit: 5, Offset: 10},
			{Model: "Order", Filters: []Filter{{"status", "IN", "paid, shipped"}, {"placed_at", "BETWEEN", "2026-01-01, 2026-02-01"}}},
		},
		"Product": {
			{Model: "Product", Filters: []Filter{{"active", "=", "true"}, {"attrs", "IS NULL", ""}, {"name", "LIKE", "%ab%"}}, Limit: 3},
			{Model: "Product", Columns: []string{"name"}, Filters: []Filter{{"stock", "NOT IN", "0, 1"}}, OrderBy: []Sort{{"price", "ASC"}, {"id", "DESC"}}, Limit: 20, Offset: 40},
		},
		"Category": {
			{Model: "Category", Filters: []Filter{{"parent_id", "IS NOT NULL", ""}}, Limit: 10},
		},
		"OrderItem": {
			{Model: "OrderItem", Filters: []Filter{{"qty", ">=", "2"}, {"unit_price", "<", "10"}}, Limit: 10},
		},
	}
	real := map[string]func(Spec, Model) (string, []DialectSQL, error){
		"Order":     func(s Spec, m Model) (string, []DialectSQL, error) { return renderModel(r, s, m, model[Order]()) },
		"Product":   func(s Spec, m Model) (string, []DialectSQL, error) { return renderModel(r, s, m, model[Product]()) },
		"Category":  func(s Spec, m Model) (string, []DialectSQL, error) { return renderModel(r, s, m, model[Category]()) },
		"OrderItem": func(s Spec, m Model) (string, []DialectSQL, error) { return renderModel(r, s, m, model[OrderItem]()) },
	}
	for _, m := range Models {
		if m.SoftDelete {
			continue // User renders through its own type
		}
		if _, ok := real[m.Name]; !ok {
			t.Fatalf("model %s has no real-type render in this test", m.Name)
		}
		for i, spec := range specs[m.Name] {
			shown := r.Render(spec)
			if shown.Error != "" {
				t.Fatalf("%s spec %d: %s", m.Name, i, shown.Error)
			}
			_, want, err := real[m.Name](spec, m)
			if err != nil {
				t.Fatalf("%s spec %d through the model type: %v", m.Name, i, err)
			}
			if !reflect.DeepEqual(shown.Dialects, want) {
				t.Errorf("%s spec %d: the row shape and the model type emit different SQL\nrow:   %+v\nmodel: %+v", m.Name, i, shown.Dialects, want)
			}
		}
	}
}

// Each preset is a generic function; the page runs it over the row shape,
// this test runs it over the model types and compares.
func TestPresetsRenderTheSameSQLAsTheModelTypes(t *testing.T) {
	r := newRenderer(t)
	ordersReal := model[Order]()
	real := map[string]func(ctx context.Context, c *quark.Client) error{
		"join":   func(ctx context.Context, c *quark.Client) error { return joinPreset(ctx, c, ordersReal) },
		"group":  func(ctx context.Context, c *quark.Client) error { return groupPreset(ctx, c, ordersReal) },
		"count":  func(ctx context.Context, c *quark.Client) error { return countPreset(ctx, c, usersReal) },
		"exists": func(ctx context.Context, c *quark.Client) error { return existsPreset(ctx, c, ordersReal, usersReal) },
		"window": func(ctx context.Context, c *quark.Client) error { return windowPreset(ctx, c, ordersReal) },
		"union":  func(ctx context.Context, c *quark.Client) error { return unionPreset(ctx, c, usersReal) },
		"cte":    func(ctx context.Context, c *quark.Client) error { return ctePreset(ctx, c, ordersReal) },
	}
	for _, p := range presets {
		run, ok := real[p.ID]
		if !ok {
			t.Fatalf("preset %q has no model-type run in this test", p.ID)
		}
		shown := r.Render(Spec{Preset: p.ID})
		if shown.Error != "" {
			t.Fatalf("preset %s: %s", p.ID, shown.Error)
		}
		if len(shown.Dialects) != 6 {
			t.Fatalf("preset %s: %d dialects", p.ID, len(shown.Dialects))
		}
		want, err := r.capture(run)
		if err != nil {
			t.Fatalf("preset %s through the model types: %v", p.ID, err)
		}
		if !reflect.DeepEqual(shown.Dialects, want) {
			t.Errorf("preset %s: the row shape and the model types emit different SQL\nrow:   %+v\nmodel: %+v", p.ID, shown.Dialects, want)
		}
		if shown.Go != p.code {
			t.Errorf("preset %s: Go text not returned verbatim", p.ID)
		}
		// The text names the methods the function calls; a method in the
		// text that no preset function calls is a drift of the prose.
		for _, method := range methodNames(p.code) {
			if !strings.Contains(presetSource, method+"(") {
				t.Errorf("preset %s: the Go text calls %s(), which presets.go does not", p.ID, method)
			}
		}
	}
	for id := range real {
		if _, ok := findPreset(id); !ok {
			t.Errorf("test knows preset %q that the catalog does not list", id)
		}
	}
}

var presetSource = func() string {
	b, err := os.ReadFile("presets.go")
	if err != nil {
		panic(err)
	}
	return string(b)
}()

// methodNames pulls `.Name(` tokens out of a Go snippet.
func methodNames(code string) []string {
	var names []string
	for i := 0; i < len(code); i++ {
		if code[i] != '.' {
			continue
		}
		j := i + 1
		for j < len(code) && (code[j] == '_' || code[j] >= 'A' && code[j] <= 'Z' || code[j] >= 'a' && code[j] <= 'z' || code[j] >= '0' && code[j] <= '9') {
			j++
		}
		if j < len(code) && code[j] == '(' && j > i+1 {
			names = append(names, code[i+1:j])
		}
	}
	return names
}

func TestSpecErrorsAreReported(t *testing.T) {
	r := newRenderer(t)
	cases := map[string]Spec{
		"unknown model":    {Model: "Invoice"},
		"unknown preset":   {Preset: "nope"},
		"int takes digits": {Model: "Order", Filters: []Filter{{"user_id", "=", "abc"}}},
		"between needs 2":  {Model: "Order", Filters: []Filter{{"total", "BETWEEN", "1"}}},
		"empty IN":         {Model: "Order", Filters: []Filter{{"status", "IN", " , "}}},
		"bad operator":     {Model: "Order", Filters: []Filter{{"status", "=>", "x"}}},
		"bad direction":    {Model: "Order", OrderBy: []Sort{{"id", "SIDEWAYS"}}},
		"bad date":         {Model: "User", Filters: []Filter{{"created_at", ">", "yesterday"}}},
	}
	for name, spec := range cases {
		res := r.Render(spec)
		if res.Error == "" {
			t.Errorf("%s: no error; SQL %+v", name, res.Dialects)
		}
		if len(res.Dialects) != 0 {
			t.Errorf("%s: dialects returned alongside an error", name)
		}
		if len(res.Catalog.Models) == 0 {
			t.Errorf("%s: the catalog must travel with every result", name)
		}
	}
}

func TestRenderJSONRoundTrip(t *testing.T) {
	r := newRenderer(t)
	out := r.RenderJSON(`{"model":"Product","columns":["name","price"],"filters":[{"column":"active","op":"=","value":"true"}],"orderBy":[{"column":"price","direction":"desc"}],"limit":5}`)
	var res Result
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("result is not JSON: %v\n%s", err, out)
	}
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	if res.Dialects[0].SQL != `SELECT "name", "price" FROM "products" WHERE "active" = $1 ORDER BY "price" DESC LIMIT 5` {
		t.Errorf("postgres: %s", res.Dialects[0].SQL)
	}
	if res.Dialects[0].Args[0] != true {
		t.Errorf("args: %v", res.Dialects[0].Args)
	}
	if len(res.Catalog.Presets) != len(presets) || len(res.Catalog.Operators) != len(Operators) {
		t.Errorf("catalog incomplete: %+v", res.Catalog)
	}
	// An empty spec is the default one; junk is an error, not a panic.
	if r.RenderJSON("") != r.RenderJSON(mustJSON(DefaultSpec())) {
		t.Error("empty spec should render the default spec")
	}
	var bad Result
	_ = json.Unmarshal([]byte(r.RenderJSON("{not json")), &bad)
	if bad.Error == "" {
		t.Error("invalid JSON must be reported in Error")
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// The documentation shows the default spec's render without JavaScript.
// This test keeps that text equal to what the .wasm renders; run with
// -update to rewrite it.
func TestStaticExampleMatchesTheDocs(t *testing.T) {
	r := newRenderer(t)
	res := r.Render(DefaultSpec())
	if res.Error != "" {
		t.Fatal(res.Error)
	}
	want := StaticExampleMarkdown(res)

	path := filepath.Join("..", "..", "..", "website", "docs", "playground.mdx")
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc := string(src)
	begin := strings.Index(doc, StaticBegin)
	end := strings.Index(doc, StaticEnd)
	if begin < 0 || end < 0 || end < begin {
		t.Fatalf("%s lacks the %s … %s markers", path, StaticBegin, StaticEnd)
	}
	begin += len(StaticBegin)
	got := strings.TrimSpace(doc[begin:end])
	if got == strings.TrimSpace(want) {
		return
	}
	if *update {
		out := doc[:begin] + "\n" + want + doc[end:]
		if err := os.WriteFile(path, []byte(out), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("rewrote the static example in %s", path)
		return
	}
	t.Errorf("the static example in %s differs from what the playground renders.\nRun: go test ./internal/playground/render -run TestStaticExampleMatchesTheDocs -update\n--- in the docs ---\n%s\n--- rendered ---\n%s", path, got, want)
}
