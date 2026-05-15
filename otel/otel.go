// Package otel provides a Quark Middleware that emits OpenTelemetry
// tracing spans and metrics for every database operation.
//
// Activation:
//
//	client, _ := quark.New("pgx", dsn, quark.WithMiddleware(otel.New()))
//
// The default is span-redaction ON (bind arguments never reach the span;
// only the parameterised SQL does) and no db.system attribute. Use the
// Options to opt out of redaction or to tag spans/metrics with a system
// name:
//
//	otel.New(otel.WithSpanRedaction(otel.IncludeArgs), otel.WithDBSystem("postgres"))
package otel

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"time"

	"github.com/jcsvwinston/quark"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"
)

const (
	tracerName = "github.com/jcsvwinston/quark"
	meterName  = "github.com/jcsvwinston/quark"
)

// RedactionMode controls whether bind arguments are exposed on the span
// attributes that the OTel Middleware emits.
type RedactionMode int

const (
	// RedactArgs is the default. Bind arguments are NOT placed on any
	// span attribute — only the parameterised SQL (which already uses
	// ?, $N, @pN or :N placeholders) reaches db.statement. Use this in
	// production: a tracing backend MUST NOT see user values it has no
	// authority to retain.
	RedactArgs RedactionMode = iota

	// IncludeArgs places the bind arguments on the optional
	// db.statement.args attribute (a string slice). Opt in only for
	// local debugging — values render via fmt.Sprintf("%v", arg), with
	// no scrubbing.
	IncludeArgs
)

// Option configures the OTel Middleware.
type Option func(*Middleware)

// WithSpanRedaction sets whether bind args are exposed on spans. The
// default is RedactArgs (args never reach the span).
func WithSpanRedaction(mode RedactionMode) Option {
	return func(m *Middleware) {
		m.redaction = mode
	}
}

// WithDBSystem sets the value of the db.system attribute on every span
// and every metric data point (e.g. "postgres", "mysql", "mariadb",
// "mssql", "oracle", "sqlite"). The Middleware does not introspect the
// Quark Client; if you want this attribute populated, pass the dialect
// name when constructing the Middleware. When unset (the default), the
// attribute is omitted.
func WithDBSystem(name string) Option {
	return func(m *Middleware) {
		m.dbSystem = name
	}
}

// Middleware implements quark.Middleware with OpenTelemetry tracing
// (spans) and metrics. Instruments are resolved lazily from the global
// MeterProvider on first use, so the middleware uses whatever provider
// is registered at query-execution time.
type Middleware struct {
	quark.BaseMiddleware

	redaction RedactionMode
	dbSystem  string

	once       sync.Once
	queries    metric.Int64Counter
	durations  metric.Float64Histogram
	rows       metric.Int64Histogram
	metricsErr error
}

// New creates a new OTel Middleware. Defaults: span redaction ON, no
// db.system attribute.
func New(opts ...Option) *Middleware {
	m := &Middleware{redaction: RedactArgs}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

// tracer returns the current global tracer, falling back to a no-op if
// the provider is nil or panics (e.g. after a test shutdown).
func (m *Middleware) tracer() trace.Tracer {
	var t trace.Tracer
	func() {
		defer func() {
			if recover() != nil {
				t = trace.NewNoopTracerProvider().Tracer(tracerName)
			}
		}()
		t = otel.GetTracerProvider().Tracer(tracerName)
	}()
	if t == nil {
		t = trace.NewNoopTracerProvider().Tracer(tracerName)
	}
	return t
}

// meter is the metrics counterpart to tracer: lazy lookup against the
// global MeterProvider with the same panic-safe fallback.
func (m *Middleware) meter() metric.Meter {
	var mt metric.Meter
	func() {
		defer func() {
			if recover() != nil {
				mt = metricnoop.NewMeterProvider().Meter(meterName)
			}
		}()
		mt = otel.GetMeterProvider().Meter(meterName)
	}()
	if mt == nil {
		mt = metricnoop.NewMeterProvider().Meter(meterName)
	}
	return mt
}

// initInstruments creates the counter and histograms once per Middleware
// instance. If instrument creation fails (which the SDK only does on
// hard misconfiguration), metricsErr is set and recordOp turns into a
// no-op for the rest of the process — the tracing path keeps working.
func (m *Middleware) initInstruments() {
	m.once.Do(func() {
		mt := m.meter()
		var err error
		m.queries, err = mt.Int64Counter(
			"quark.queries.total",
			metric.WithDescription("Total number of database operations issued by Quark"),
		)
		if err != nil {
			m.metricsErr = err
			return
		}
		m.durations, err = mt.Float64Histogram(
			"quark.queries.duration",
			metric.WithDescription("Duration of a Quark database operation"),
			metric.WithUnit("ms"),
		)
		if err != nil {
			m.metricsErr = err
			return
		}
		m.rows, err = mt.Int64Histogram(
			"quark.queries.rows",
			metric.WithDescription("Rows affected by an Exec — emitted only when sql.Result.RowsAffected succeeds"),
		)
		if err != nil {
			m.metricsErr = err
			return
		}
	})
}

// commonAttrs returns the attribute slice shared by every span and every
// metric data point. db.system is added when set; db.operation is always
// added with the caller-supplied label.
//
// db.table is intentionally omitted: the Middleware sits below the query
// builder and only sees the parameterised SQL string, not the parsed
// table. Adding it would require parsing or threading the table down
// through the Executor contract — out of scope for F4-1.
func (m *Middleware) commonAttrs(op string) []attribute.KeyValue {
	attrs := make([]attribute.KeyValue, 0, 2)
	attrs = append(attrs, attribute.String("db.operation", op))
	if m.dbSystem != "" {
		attrs = append(attrs, attribute.String("db.system", m.dbSystem))
	}
	return attrs
}

// startSpan opens a span for one operation and applies the redaction
// policy: with RedactArgs, only the parameterised SQL reaches the span;
// with IncludeArgs, the args are rendered onto db.statement.args.
func (m *Middleware) startSpan(ctx context.Context, name, op, sqlStr string, args []any) (context.Context, trace.Span) {
	attrs := m.commonAttrs(op)
	attrs = append(attrs, attribute.String("db.statement", sqlStr))
	if m.redaction == IncludeArgs && len(args) > 0 {
		attrs = append(attrs, attribute.StringSlice("db.statement.args", argsToStrings(args)))
	}
	return m.tracer().Start(ctx, name,
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attrs...),
	)
}

// recordOp publishes one metric data point: increments quark.queries.total,
// records duration in ms, and — when hasRows is true — records the rows
// histogram. Called once per operation from the deferred tails of the
// WrapXxx wrappers so the duration covers the full call.
func (m *Middleware) recordOp(ctx context.Context, op string, start time.Time, rows int64, hasRows bool) {
	m.initInstruments()
	if m.metricsErr != nil {
		return
	}
	attrSet := metric.WithAttributes(m.commonAttrs(op)...)
	m.queries.Add(ctx, 1, attrSet)
	m.durations.Record(ctx, float64(time.Since(start).Microseconds())/1000.0, attrSet)
	if hasRows {
		m.rows.Record(ctx, rows, attrSet)
	}
}

func argsToStrings(args []any) []string {
	out := make([]string, len(args))
	for i, a := range args {
		out[i] = fmt.Sprintf("%v", a)
	}
	return out
}

// WrapExec instruments INSERT / UPDATE / DELETE / DDL: span + counter +
// duration histogram + rows histogram (from sql.Result.RowsAffected when
// available).
func (m *Middleware) WrapExec(next quark.ExecFunc) quark.ExecFunc {
	return func(ctx context.Context, exec quark.Executor, sqlStr string, args []any) (res sql.Result, err error) {
		start := time.Now()
		ctx, span := m.startSpan(ctx, "quark.exec", "EXEC", sqlStr, args)
		defer func() {
			if err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
			}
			span.End()

			var (
				rowsAffected int64
				hasRows      bool
			)
			if err == nil && res != nil {
				if ra, raErr := res.RowsAffected(); raErr == nil {
					rowsAffected, hasRows = ra, true
				}
			}
			m.recordOp(ctx, "EXEC", start, rowsAffected, hasRows)
		}()
		return next(ctx, exec, sqlStr, args)
	}
}

// WrapQuery instruments a multi-row SELECT: span + counter + duration
// histogram. The rows histogram is NOT emitted here — counting rows
// would require wrapping *sql.Rows (consumed by the caller via Next /
// Scan) and is out of scope for F4-1.
func (m *Middleware) WrapQuery(next quark.QueryFunc) quark.QueryFunc {
	return func(ctx context.Context, exec quark.Executor, sqlStr string, args []any) (rows *sql.Rows, err error) {
		start := time.Now()
		ctx, span := m.startSpan(ctx, "quark.query", "SELECT", sqlStr, args)
		defer func() {
			if err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
			}
			span.End()
			m.recordOp(ctx, "SELECT", start, 0, false)
		}()
		return next(ctx, exec, sqlStr, args)
	}
}

// WrapQueryRow instruments a single-row SELECT: span + counter + duration
// histogram. Errors surface only on Scan, so the span has no error
// status here (mirrors the historical comment). The rows histogram is
// not emitted (no result handle to consult).
func (m *Middleware) WrapQueryRow(next quark.QueryRowFunc) quark.QueryRowFunc {
	return func(ctx context.Context, exec quark.Executor, sqlStr string, args []any) *sql.Row {
		start := time.Now()
		ctx, span := m.startSpan(ctx, "quark.query_row", "SELECT_ROW", sqlStr, args)
		// Spans for QueryRow are tricky: the error is observable only
		// on the caller's Scan, not here. End the span on return and let
		// the counter/duration metrics cover the latency.
		defer func() {
			span.End()
			m.recordOp(ctx, "SELECT_ROW", start, 0, false)
		}()
		return next(ctx, exec, sqlStr, args)
	}
}
