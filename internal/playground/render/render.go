// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package render turns a playground form specification into the SQL Quark
// emits for each of its six dialects, together with the Go chain that
// produces it.
//
// It executes nothing. Each dialect gets a Quark client over a
// database/sql driver that records the statement it is handed and answers
// an empty result; the package reads the statement back. The same code
// compiles to WebAssembly for the documentation site (internal/playground)
// and runs natively in the test that keeps the site's static example in
// step with what the browser renders.
package render

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jcsvwinston/quark"
)

// Spec is the form the playground page submits. The zero value is not a
// valid query; DefaultSpec is the one the page shows first.
type Spec struct {
	// Model names one of the example models: User, Order, Product,
	// Category or OrderItem.
	Model string `json:"model"`
	// Columns to project. Empty selects every column.
	Columns []string `json:"columns,omitempty"`
	// Filters are ANDed WHERE conditions.
	Filters []Filter `json:"filters,omitempty"`
	// OrderBy clauses, applied in order.
	OrderBy []Sort `json:"orderBy,omitempty"`
	// Limit of rows. Zero omits Limit(); Quark then caps List() at 100.
	Limit int `json:"limit"`
	// Offset of rows to skip. Zero omits Offset().
	Offset int `json:"offset"`
	// WithTrashed includes soft-deleted rows on a model with deleted_at.
	WithTrashed bool `json:"withTrashed"`
	// Preset, when set, renders one of the fixed queries from the catalog
	// instead of the form fields above.
	Preset string `json:"preset,omitempty"`
}

// Filter is one WHERE condition. Value is the text typed in the form; it
// is converted to the column's Go type. IN and NOT IN take a
// comma-separated list, BETWEEN takes two comma-separated values, IS NULL
// and IS NOT NULL ignore it.
type Filter struct {
	Column string `json:"column"`
	Op     string `json:"op"`
	Value  string `json:"value"`
}

// Sort is one ORDER BY clause.
type Sort struct {
	Column    string `json:"column"`
	Direction string `json:"direction"`
}

// Result is what the playground page receives.
type Result struct {
	// Go is the fluent chain, as source text, that produces the SQL.
	Go string `json:"go"`
	// Dialects holds one entry per dialect, in the order of Dialects.
	Dialects []DialectSQL `json:"dialects"`
	// Error is set when Quark refused the query; Dialects is then empty.
	Error string `json:"error,omitempty"`
	// Catalog describes the models, operators and presets the form can
	// offer, so the page never carries its own copy.
	Catalog Catalog `json:"catalog"`
}

// DialectSQL is the statement one dialect emits and the arguments bound
// to its placeholders.
type DialectSQL struct {
	Name string `json:"name"`
	SQL  string `json:"sql"`
	Args []any  `json:"args"`
}

// Catalog lists what the form may choose from.
type Catalog struct {
	Models    []Model    `json:"models"`
	Operators []string   `json:"operators"`
	Presets   []PresetID `json:"presets"`
}

// Model describes one example model.
type Model struct {
	Name       string   `json:"name"`
	Table      string   `json:"table"`
	Columns    []Column `json:"columns"`
	SoftDelete bool     `json:"softDelete"`
}

// Column is a model column and the kind of value it takes.
type Column struct {
	Name string `json:"name"`
	// Type is one of string, int, float, bool, time.
	Type string `json:"type"`
}

// PresetID names a fixed query in the catalog.
type PresetID struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Family      string `json:"family"`
	Description string `json:"description"`
}

// Operators the form offers. They are the ones Where accepts on every
// engine; the PostgreSQL-only containment and network operators are left
// out because the other five dialects refuse them.
var Operators = []string{"=", "!=", "<", "<=", ">", ">=", "LIKE", "NOT LIKE", "IN", "NOT IN", "BETWEEN", "IS NULL", "IS NOT NULL"}

// Models in the order the form lists them.
var Models = []Model{
	{Name: "User", Table: "users", SoftDelete: true, Columns: []Column{
		{"id", "int"}, {"email", "string"}, {"country", "string"}, {"profile", "string"}, {"created_at", "time"}, {"deleted_at", "time"},
	}},
	{Name: "Order", Table: "orders", Columns: []Column{
		{"id", "int"}, {"user_id", "int"}, {"total", "float"}, {"status", "string"}, {"placed_at", "time"},
	}},
	{Name: "Product", Table: "products", Columns: []Column{
		{"id", "int"}, {"category_id", "int"}, {"name", "string"}, {"price", "float"}, {"stock", "int"}, {"attrs", "string"}, {"active", "bool"},
	}},
	{Name: "Category", Table: "categories", Columns: []Column{
		{"id", "int"}, {"name", "string"}, {"parent_id", "int"},
	}},
	{Name: "OrderItem", Table: "order_items", Columns: []Column{
		{"id", "int"}, {"order_id", "int"}, {"product_id", "int"}, {"qty", "int"}, {"unit_price", "float"},
	}},
}

// DefaultSpec is the query the page shows before anyone touches the form,
// and the one the static example in the documentation is rendered from.
func DefaultSpec() Spec {
	return Spec{
		Model:   "User",
		Columns: []string{"id", "email", "country"},
		Filters: []Filter{
			{Column: "country", Op: "=", Value: "ES"},
			{Column: "created_at", Op: ">=", Value: "2026-01-01"},
		},
		OrderBy: []Sort{{Column: "created_at", Direction: "DESC"}},
		Limit:   10,
		Offset:  20,
	}
}

// target is one dialect with its recording client.
type target struct {
	dialect quark.Dialect
	client  *quark.Client
	rec     *recorder
}

// Renderer holds one Quark client per dialect. Build it once with New and
// call Render as often as the form changes.
type Renderer struct {
	targets []target
}

// New opens a recording client for each of the six dialects.
func New() (*Renderer, error) {
	dialects := []quark.Dialect{
		quark.PostgreSQL(), quark.MySQL(), quark.MariaDB(),
		quark.SQLite(), quark.MSSQL(), quark.Oracle(),
	}
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	r := &Renderer{}
	for _, d := range dialects {
		rec := &recorder{}
		db := sql.OpenDB(rec)
		c, err := quark.NewWithDB(d.Name(), db, quark.WithDialect(d), quark.WithLogger(quiet))
		if err != nil {
			return nil, fmt.Errorf("playground: open %s client: %w", d.Name(), err)
		}
		r.targets = append(r.targets, target{dialect: d, client: c, rec: rec})
	}
	return r, nil
}

// Dialects returns the dialect names in the order Result.Dialects uses.
func (r *Renderer) Dialects() []string {
	out := make([]string, len(r.targets))
	for i, t := range r.targets {
		out[i] = t.dialect.Name()
	}
	return out
}

// RenderJSON is Render over JSON, for the WebAssembly export. A spec that
// does not parse yields a Result whose Error says so.
func (r *Renderer) RenderJSON(specJSON string) string {
	var spec Spec
	var res Result
	if strings.TrimSpace(specJSON) == "" {
		spec = DefaultSpec()
	} else if err := json.Unmarshal([]byte(specJSON), &spec); err != nil {
		res = Result{Error: "spec is not valid JSON: " + err.Error(), Catalog: catalog()}
		b, _ := json.Marshal(res)
		return string(b)
	}
	res = r.Render(spec)
	b, err := json.Marshal(res)
	if err != nil {
		b, _ = json.Marshal(Result{Error: "result could not be encoded: " + err.Error(), Catalog: catalog()})
	}
	return string(b)
}

// Render builds the query the spec describes against every dialect and
// returns the SQL each one emits.
func (r *Renderer) Render(spec Spec) Result {
	res := Result{Catalog: catalog()}
	var err error
	if spec.Preset != "" {
		res.Go, res.Dialects, err = r.renderPreset(spec.Preset)
	} else {
		res.Go, res.Dialects, err = r.renderForm(spec)
	}
	if err != nil {
		res.Error = err.Error()
		res.Dialects = nil
	}
	return res
}

func catalog() Catalog {
	ids := make([]PresetID, 0, len(presets))
	for _, p := range presets {
		ids = append(ids, p.PresetID)
	}
	return Catalog{Models: Models, Operators: Operators, Presets: ids}
}

func findModel(name string) (Model, bool) {
	for _, m := range Models {
		if m.Name == name {
			return m, true
		}
	}
	return Model{}, false
}

func (m Model) column(name string) Column {
	for _, c := range m.Columns {
		if c.Name == name {
			return c
		}
	}
	// Unknown to the catalog: Quark still validates it as an identifier,
	// and a text value is the least surprising guess.
	return Column{Name: name, Type: "string"}
}

// base builds the query a render starts from: quark.For over the model
// type, or quark.For over the row shape pointed at the model's table.
type base[T any] func(ctx context.Context, c *quark.Client) *quark.Query[T]

// model is the base the Go text shows: quark.For[T](ctx, client).
func model[T any]() base[T] {
	return func(ctx context.Context, c *quark.Client) *quark.Query[T] {
		return quark.For[T](ctx, c)
	}
}

// fromTable is the base the WebAssembly build uses for the models without a
// soft-delete column. Every generic instantiation of quark.Query costs about
// 130 KB compressed in the .wasm, so the four plain models share one
// instantiation — the row shape — and FromTable names the table. The SQL is
// the same as the model type's own, and TestRowShapeRendersTheModelsSQL
// holds that true for every spec it tries on every dialect.
func fromTable[T any](table string) base[T] {
	return func(ctx context.Context, c *quark.Client) *quark.Query[T] {
		return quark.For[T](ctx, c).FromTable(table)
	}
}

// row is the shape the plain models render through. It carries only the
// primary key, which is what the dialects that need a deterministic ORDER
// BY for OFFSET … FETCH read from the model.
type row struct {
	ID int64 `db:"id" pk:"true"`
}

// renderForm picks the base for the model. User keeps its own type because
// its deleted_at column is what adds the soft-delete predicate, and
// FromTable drops that predicate by design.
func (r *Renderer) renderForm(spec Spec) (string, []DialectSQL, error) {
	m, ok := findModel(spec.Model)
	if !ok {
		return "", nil, fmt.Errorf("unknown model %q; the catalog has %s", spec.Model, modelNames())
	}
	if m.SoftDelete {
		return renderModel(r, spec, m, model[User]())
	}
	return renderModel(r, spec, m, fromTable[row](m.Table))
}

func modelNames() string {
	names := make([]string, len(Models))
	for i, m := range Models {
		names[i] = m.Name
	}
	return strings.Join(names, ", ")
}

// step is one method of the fluent chain: how it reads as Go, and what it
// does to the query. Keeping both together is what guarantees the text the
// reader learns from is the call that produced the SQL.
type step[T any] struct {
	text  string
	apply func(*quark.Query[T]) *quark.Query[T]
}

func renderModel[T any](r *Renderer, spec Spec, m Model, from base[T]) (string, []DialectSQL, error) {
	steps, err := formSteps[T](spec, m)
	if err != nil {
		return "", nil, err
	}

	var code strings.Builder
	fmt.Fprintf(&code, "quark.For[%s](ctx, client).", m.Name)
	for _, s := range steps {
		code.WriteString("\n\t" + s.text + ".")
	}
	code.WriteString("\n\tList()")

	out, err := r.capture(func(ctx context.Context, c *quark.Client) error {
		q := from(ctx, c)
		for _, s := range steps {
			q = s.apply(q)
		}
		_, err := q.List()
		return err
	})
	return code.String(), out, err
}

func formSteps[T any](spec Spec, m Model) ([]step[T], error) {
	var steps []step[T]

	if spec.WithTrashed && m.SoftDelete {
		steps = append(steps, step[T]{
			text:  "WithTrashed()",
			apply: func(q *quark.Query[T]) *quark.Query[T] { return q.WithTrashed() },
		})
	}

	if len(spec.Columns) > 0 {
		cols := append([]string(nil), spec.Columns...)
		steps = append(steps, step[T]{
			text:  "Select(" + quoteList(cols) + ")",
			apply: func(q *quark.Query[T]) *quark.Query[T] { return q.Select(cols...) },
		})
	}

	for i, f := range spec.Filters {
		s, err := filterStep[T](f, m)
		if err != nil {
			return nil, fmt.Errorf("filter %d: %w", i+1, err)
		}
		steps = append(steps, s)
	}

	for _, o := range spec.OrderBy {
		col, dir := o.Column, strings.ToUpper(strings.TrimSpace(o.Direction))
		if dir == "" {
			dir = "ASC"
		}
		steps = append(steps, step[T]{
			text:  fmt.Sprintf("OrderBy(%s, %s)", strconv.Quote(col), strconv.Quote(dir)),
			apply: func(q *quark.Query[T]) *quark.Query[T] { return q.OrderBy(col, dir) },
		})
	}

	if spec.Limit > 0 {
		n := spec.Limit
		steps = append(steps, step[T]{
			text:  fmt.Sprintf("Limit(%d)", n),
			apply: func(q *quark.Query[T]) *quark.Query[T] { return q.Limit(n) },
		})
	}
	if spec.Offset > 0 {
		n := spec.Offset
		steps = append(steps, step[T]{
			text:  fmt.Sprintf("Offset(%d)", n),
			apply: func(q *quark.Query[T]) *quark.Query[T] { return q.Offset(n) },
		})
	}
	return steps, nil
}

func filterStep[T any](f Filter, m Model) (step[T], error) {
	col := f.Column
	op := strings.ToUpper(strings.TrimSpace(f.Op))
	c := m.column(col)
	qcol := strconv.Quote(col)

	switch op {
	case "IS NULL", "IS NOT NULL":
		return step[T]{
			text:  fmt.Sprintf("Where(%s, %s, nil)", qcol, strconv.Quote(op)),
			apply: func(q *quark.Query[T]) *quark.Query[T] { return q.Where(col, op, nil) },
		}, nil

	case "IN", "NOT IN":
		vals, lits, err := coerceList(c, f.Value)
		if err != nil {
			return step[T]{}, err
		}
		if len(vals) == 0 {
			return step[T]{}, fmt.Errorf("%s needs at least one comma-separated value", op)
		}
		list := "[]any{" + strings.Join(lits, ", ") + "}"
		if op == "IN" {
			return step[T]{
				text:  fmt.Sprintf("WhereIn(%s, %s)", qcol, list),
				apply: func(q *quark.Query[T]) *quark.Query[T] { return q.WhereIn(col, vals) },
			}, nil
		}
		return step[T]{
			text:  fmt.Sprintf("Where(%s, \"NOT IN\", %s)", qcol, list),
			apply: func(q *quark.Query[T]) *quark.Query[T] { return q.Where(col, "NOT IN", vals) },
		}, nil

	case "BETWEEN":
		vals, lits, err := coerceList(c, f.Value)
		if err != nil {
			return step[T]{}, err
		}
		if len(vals) != 2 {
			return step[T]{}, fmt.Errorf("BETWEEN needs exactly two comma-separated values, got %d", len(vals))
		}
		return step[T]{
			text:  fmt.Sprintf("WhereBetween(%s, %s, %s)", qcol, lits[0], lits[1]),
			apply: func(q *quark.Query[T]) *quark.Query[T] { return q.WhereBetween(col, vals[0], vals[1]) },
		}, nil

	default:
		val, lit, err := coerce(c, f.Value)
		if err != nil {
			return step[T]{}, err
		}
		return step[T]{
			text:  fmt.Sprintf("Where(%s, %s, %s)", qcol, strconv.Quote(op), lit),
			apply: func(q *quark.Query[T]) *quark.Query[T] { return q.Where(col, op, val) },
		}, nil
	}
}

// coerce converts the form text to the column's Go type and returns the
// value with its Go literal.
func coerce(c Column, raw string) (any, string, error) {
	raw = strings.TrimSpace(raw)
	switch c.Type {
	case "int":
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return nil, "", fmt.Errorf("%s takes an integer, got %q", c.Name, raw)
		}
		return n, strconv.FormatInt(n, 10), nil
	case "float":
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, "", fmt.Errorf("%s takes a number, got %q", c.Name, raw)
		}
		lit := strconv.FormatFloat(f, 'f', -1, 64)
		if !strings.Contains(lit, ".") {
			lit += ".0"
		}
		return f, lit, nil
	case "bool":
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, "", fmt.Errorf("%s takes true or false, got %q", c.Name, raw)
		}
		return b, strconv.FormatBool(b), nil
	case "time":
		t, err := parseTime(raw)
		if err != nil {
			return nil, "", fmt.Errorf("%s takes a date (2006-01-02) or an RFC 3339 timestamp, got %q", c.Name, raw)
		}
		return t, timeLiteral(t), nil
	default:
		return raw, strconv.Quote(raw), nil
	}
}

func coerceList(c Column, raw string) ([]any, []string, error) {
	var vals []any
	var lits []string
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		v, lit, err := coerce(c, part)
		if err != nil {
			return nil, nil, err
		}
		vals = append(vals, v)
		lits = append(lits, lit)
	}
	return vals, lits, nil
}

func parseTime(raw string) (time.Time, error) {
	for _, layout := range []string{"2006-01-02", time.RFC3339, "2006-01-02T15:04", "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("not a date")
}

func timeLiteral(t time.Time) string {
	return fmt.Sprintf("time.Date(%d, time.%s, %d, %d, %d, %d, 0, time.UTC)",
		t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second())
}

func quoteList(ss []string) string {
	qs := make([]string, len(ss))
	for i, s := range ss {
		qs[i] = strconv.Quote(s)
	}
	return strings.Join(qs, ", ")
}

// jsonArgs makes the bound arguments JSON-friendly: times as RFC 3339
// strings, []byte as text, everything else as it is.
func jsonArgs(args []any) []any {
	out := make([]any, len(args))
	for i, a := range args {
		switch v := a.(type) {
		case time.Time:
			out[i] = v.UTC().Format(time.RFC3339)
		case []byte:
			out[i] = string(v)
		default:
			out[i] = v
		}
	}
	if out == nil {
		out = []any{}
	}
	return out
}
