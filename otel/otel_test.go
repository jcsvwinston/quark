// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package otel

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	_ "modernc.org/sqlite"
)

// fakeResult satisfies sql.Result with a fixed RowsAffected so the WrapExec
// path can be exercised without a real database.
type fakeResult struct{ rows int64 }

func (r fakeResult) LastInsertId() (int64, error) { return 0, nil }
func (r fakeResult) RowsAffected() (int64, error) { return r.rows, nil }

// NOTE: setupTracerRecorder and setupMetricReader both mutate the global
// OTel TracerProvider / MeterProvider. Tests in this file therefore must
// NOT call t.Parallel(): a concurrent test in this same process would
// race against the cleanup-restore boundary. Run sequentially.

// setupTracerRecorder installs a SpanRecorder as the global TracerProvider
// for the duration of one test, returns the recorder, and restores the
// previous provider on cleanup.
func setupTracerRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	rec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(rec))
	prev := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(prev) })
	return rec
}

// setupMetricReader installs a ManualReader as the global MeterProvider
// and returns it so tests can Collect() and inspect the data points.
func setupMetricReader(t *testing.T) *sdkmetric.ManualReader {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	prev := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)
	t.Cleanup(func() { otel.SetMeterProvider(prev) })
	return reader
}

// findAttr returns the value of the named attribute on the span, or "" if
// it isn't present. Helper for the redaction assertions.
func findAttr(span sdktrace.ReadOnlySpan, name string) attribute.Value {
	for _, kv := range span.Attributes() {
		if string(kv.Key) == name {
			return kv.Value
		}
	}
	return attribute.Value{}
}

// TestNew_Defaults pins the constructor contract: redaction defaults to
// RedactArgs (args never reach a span) and db.system is unset (the
// Middleware does not introspect the connection).
func TestNew_Defaults(t *testing.T) {
	m := New()
	if m.redaction != RedactArgs {
		t.Errorf("default redaction = %v, want RedactArgs", m.redaction)
	}
	if m.dbSystem != "" {
		t.Errorf("default dbSystem = %q, want empty", m.dbSystem)
	}
}

func TestOptions_Apply(t *testing.T) {
	m := New(WithSpanRedaction(IncludeArgs), WithDBSystem("postgres"))
	if m.redaction != IncludeArgs {
		t.Errorf("WithSpanRedaction did not apply: redaction = %v", m.redaction)
	}
	if m.dbSystem != "postgres" {
		t.Errorf("WithDBSystem did not apply: dbSystem = %q", m.dbSystem)
	}
}

// TestSpan_DefaultRedactionExcludesArgs is the F4-2 headline contract:
// with the default redaction, no span carries the bind arguments.
func TestSpan_DefaultRedactionExcludesArgs(t *testing.T) {
	rec := setupTracerRecorder(t)
	m := New()

	exec := m.WrapExec(func(ctx context.Context, _ quark.Executor, _ string, _ []any) (sql.Result, error) {
		return fakeResult{rows: 3}, nil
	})
	if _, err := exec(context.Background(), nil, "INSERT INTO t(v) VALUES (?)", []any{"secret"}); err != nil {
		t.Fatalf("exec: %v", err)
	}

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if v := findAttr(spans[0], "db.statement.args"); v.Type() != attribute.INVALID {
		t.Errorf("default redaction must hide args; found db.statement.args = %v", v.AsInterface())
	}
	if got := findAttr(spans[0], "db.statement").AsString(); got != "INSERT INTO t(v) VALUES (?)" {
		t.Errorf("db.statement = %q, want the parameterised SQL", got)
	}
}

// TestSpan_IncludeArgsAttachesArgs is the opt-out half of F4-2: with
// IncludeArgs the args show up under db.statement.args.
func TestSpan_IncludeArgsAttachesArgs(t *testing.T) {
	rec := setupTracerRecorder(t)
	m := New(WithSpanRedaction(IncludeArgs))

	q := m.WrapQuery(func(ctx context.Context, _ quark.Executor, _ string, _ []any) (*sql.Rows, error) {
		return nil, nil
	})
	if _, err := q(context.Background(), nil, "SELECT * FROM t WHERE id = ?", []any{42}); err != nil {
		t.Fatalf("query: %v", err)
	}

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	v := findAttr(spans[0], "db.statement.args")
	if v.Type() == attribute.INVALID {
		t.Fatal("IncludeArgs must attach db.statement.args")
	}
	got := v.AsStringSlice()
	if len(got) != 1 || got[0] != "42" {
		t.Errorf("db.statement.args = %v, want [42]", got)
	}
}

// TestSpan_DBSystemAttribute pins that WithDBSystem propagates to the span
// (and, by extension, to metrics — same commonAttrs codepath).
func TestSpan_DBSystemAttribute(t *testing.T) {
	rec := setupTracerRecorder(t)
	m := New(WithDBSystem("postgres"))

	exec := m.WrapExec(func(ctx context.Context, _ quark.Executor, _ string, _ []any) (sql.Result, error) {
		return fakeResult{rows: 1}, nil
	})
	if _, err := exec(context.Background(), nil, "INSERT INTO t VALUES (?)", []any{1}); err != nil {
		t.Fatalf("exec: %v", err)
	}

	spans := rec.Ended()
	if len(spans) != 1 {
		t.Fatalf("expected 1 span, got %d", len(spans))
	}
	if got := findAttr(spans[0], "db.system").AsString(); got != "postgres" {
		t.Errorf("db.system = %q, want postgres", got)
	}
}

// TestMetrics_CounterAndDurationEmit is the F4-1 contract: every wrapped
// op increments quark.queries.total and records into quark.queries.duration.
// Exec additionally records into quark.queries.rows; Query/QueryRow do not.
func TestMetrics_CounterAndDurationEmit(t *testing.T) {
	reader := setupMetricReader(t)
	_ = setupTracerRecorder(t) // span side must coexist with metric side
	m := New(WithDBSystem("sqlite"))

	exec := m.WrapExec(func(ctx context.Context, _ quark.Executor, _ string, _ []any) (sql.Result, error) {
		return fakeResult{rows: 5}, nil
	})
	q := m.WrapQuery(func(ctx context.Context, _ quark.Executor, _ string, _ []any) (*sql.Rows, error) {
		return nil, nil
	})

	if _, err := exec(context.Background(), nil, "DELETE FROM t", nil); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if _, err := q(context.Background(), nil, "SELECT 1", nil); err != nil {
		t.Fatalf("query: %v", err)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	want := map[string]bool{
		"quark.queries.total":    false,
		"quark.queries.duration": false,
		"quark.queries.rows":     false,
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if _, ok := want[m.Name]; ok {
				want[m.Name] = true
			}
		}
	}
	for name, present := range want {
		if !present {
			t.Errorf("metric %s was not emitted", name)
		}
	}
}

// TestWrapQueryRow_EmitsSpanAndCounter pins the QueryRow contract: error
// status isn't set (errors surface only on the caller's Scan), but the
// span IS emitted and the counter increments with op "SELECT_ROW". The
// rows histogram does not see a data point (no result handle to consult).
func TestWrapQueryRow_EmitsSpanAndCounter(t *testing.T) {
	rec := setupTracerRecorder(t)
	reader := setupMetricReader(t)
	m := New()

	qr := m.WrapQueryRow(func(ctx context.Context, _ quark.Executor, _ string, _ []any) *sql.Row {
		return nil
	})
	_ = qr(context.Background(), nil, "SELECT 1 FROM t WHERE id = ?", []any{1})

	spans := rec.Ended()
	if len(spans) != 1 || spans[0].Name() != "quark.query_row" {
		t.Fatalf("expected one quark.query_row span, got %d spans (%v)", len(spans), spans)
	}
	if got := findAttr(spans[0], "db.operation").AsString(); got != "SELECT_ROW" {
		t.Errorf("db.operation = %q, want SELECT_ROW", got)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	var totalSum int64
	for _, sm := range rm.ScopeMetrics {
		for _, mt := range sm.Metrics {
			if mt.Name != "quark.queries.total" {
				continue
			}
			sum, ok := mt.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("quark.queries.total is %T, want Sum[int64]", mt.Data)
			}
			for _, dp := range sum.DataPoints {
				totalSum += dp.Value
			}
		}
	}
	if totalSum != 1 {
		t.Errorf("quark.queries.total = %d after one QueryRow, want 1", totalSum)
	}
}

// TestMetrics_RowsHistogramOnlyOnExec pins the deliberate gap: counting
// SELECT rows would require wrapping *sql.Rows, which is out of scope.
// The rows histogram must therefore see only the Exec value, not the
// SELECT.
func TestMetrics_RowsHistogramOnlyOnExec(t *testing.T) {
	reader := setupMetricReader(t)
	_ = setupTracerRecorder(t)
	m := New()

	exec := m.WrapExec(func(ctx context.Context, _ quark.Executor, _ string, _ []any) (sql.Result, error) {
		return fakeResult{rows: 7}, nil
	})
	q := m.WrapQuery(func(ctx context.Context, _ quark.Executor, _ string, _ []any) (*sql.Rows, error) {
		return nil, nil
	})

	for i := 0; i < 3; i++ {
		if _, err := q(context.Background(), nil, "SELECT 1", nil); err != nil {
			t.Fatalf("query: %v", err)
		}
	}
	if _, err := exec(context.Background(), nil, "DELETE FROM t", nil); err != nil {
		t.Fatalf("exec: %v", err)
	}

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	var rowsCount uint64
	for _, sm := range rm.ScopeMetrics {
		for _, mt := range sm.Metrics {
			if mt.Name != "quark.queries.rows" {
				continue
			}
			hist, ok := mt.Data.(metricdata.Histogram[int64])
			if !ok {
				t.Fatalf("quark.queries.rows is %T, want Histogram[int64]", mt.Data)
			}
			for _, dp := range hist.DataPoints {
				rowsCount += dp.Count
			}
		}
	}
	if rowsCount != 1 {
		t.Errorf("quark.queries.rows should see exactly 1 data point (the Exec), got %d", rowsCount)
	}
}

// kindRow is the model the client-level tests migrate.
type kindRow struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

func (kindRow) TableName() string { return "otel_kind_rows" }

// TestSpans_SchemaWorkAndSavepoints drives a real client (SQLite in memory)
// through the middleware: the CREATE TABLE of Migrate, the catalog reads of
// IntrospectSchema and the SAVEPOINT / ROLLBACK TO of a transaction each get
// a span, tagged with the statement's kind, beside the query builder's own.
// The data points of quark.queries.total carry the kind too.
// Before A11 Q7 none of the first three reached the middleware, so no trace
// showed them.
func TestSpans_SchemaWorkAndSavepoints(t *testing.T) {
	rec := setupTracerRecorder(t)
	reader := setupMetricReader(t)
	ctx := context.Background()

	client, err := quark.New("sqlite", "file:otel_kinds?mode=memory&cache=shared",
		quark.WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		quark.WithMiddleware(New(WithDBSystem("sqlite"))))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })

	if err := client.Migrate(ctx, &kindRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := client.IntrospectSchema(ctx); err != nil {
		t.Fatalf("introspect: %v", err)
	}
	if err := client.Tx(ctx, func(tx *quark.Tx) error {
		if err := quark.ForTx[kindRow](ctx, tx).Create(&kindRow{Name: "a"}); err != nil {
			return err
		}
		if err := tx.Savepoint("sp"); err != nil {
			return err
		}
		return tx.RollbackTo("sp")
	}); err != nil {
		t.Fatalf("tx: %v", err)
	}

	// The span of the first statement of each kind, by its statement.
	byKind := map[string]string{}
	for _, s := range rec.Ended() {
		kind := findAttr(s, StatementKindKey).AsString()
		if kind == "" {
			t.Errorf("span %s for %q carries no %s", s.Name(), findAttr(s, "db.statement").AsString(), StatementKindKey)
			continue
		}
		if _, seen := byKind[kind]; !seen {
			byKind[kind] = s.Name() + " " + findAttr(s, "db.statement").AsString()
		}
	}
	for kind, prefix := range map[string]string{
		string(quark.StatementDDL):           "quark.exec CREATE TABLE",
		string(quark.StatementIntrospection): "quark.query ",
		string(quark.StatementSavepoint):     `quark.exec SAVEPOINT "sp"`,
		// SQLite's Create reads its key back with RETURNING, through
		// QueryRowContext: a query by the method it is sent with.
		string(quark.StatementQuery): "quark.query_row INSERT INTO",
	} {
		got, ok := byKind[kind]
		if !ok {
			t.Errorf("no span of kind %s; spans by kind: %v", kind, byKind)
			continue
		}
		if !strings.HasPrefix(got, prefix) {
			t.Errorf("first span of kind %s is %q, want it to start with %q", kind, got, prefix)
		}
	}
	var rollbackTo bool
	for _, s := range rec.Ended() {
		if findAttr(s, StatementKindKey).AsString() == string(quark.StatementSavepoint) &&
			strings.HasPrefix(findAttr(s, "db.statement").AsString(), "ROLLBACK TO SAVEPOINT") {
			rollbackTo = true
		}
	}
	if !rollbackTo {
		t.Error("no savepoint span for the ROLLBACK TO SAVEPOINT")
	}

	// The counter's data points carry the kind too, so a dashboard can
	// leave schema work out.
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(ctx, &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}
	kinds := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, mt := range sm.Metrics {
			if mt.Name != "quark.queries.total" {
				continue
			}
			for _, dp := range mt.Data.(metricdata.Sum[int64]).DataPoints {
				v, _ := dp.Attributes.Value(StatementKindKey)
				kinds[v.AsString()] += dp.Value
			}
		}
	}
	for _, k := range []quark.StatementKind{quark.StatementDDL, quark.StatementIntrospection, quark.StatementSavepoint, quark.StatementQuery} {
		if kinds[string(k)] == 0 {
			t.Errorf("quark.queries.total has no data point of kind %s: %v", k, kinds)
		}
	}
}

// TestSpan_NoKindOutsideAClient pins that a middleware called directly, with
// a context that never went through a client's chain, adds no kind attribute.
func TestSpan_NoKindOutsideAClient(t *testing.T) {
	rec := setupTracerRecorder(t)
	exec := New().WrapExec(func(ctx context.Context, _ quark.Executor, _ string, _ []any) (sql.Result, error) {
		return fakeResult{rows: 1}, nil
	})
	if _, err := exec(context.Background(), nil, "DELETE FROM t", nil); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if v := findAttr(rec.Ended()[0], StatementKindKey); v.Type() != attribute.INVALID {
		t.Errorf("%s = %v on a span from outside a client", StatementKindKey, v.AsInterface())
	}
}
