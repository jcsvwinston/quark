package otel

import (
	"context"
	"database/sql"

	"github.com/jcsvwinston/quark"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "github.com/jcsvwinston/quark"

// Middleware implements quark.Middleware to provide native OpenTelemetry tracing.
type Middleware struct {
	quark.BaseMiddleware
}

// New creates a new OTel middleware for Quark.
// The tracer is resolved lazily from the global TracerProvider at query-execution
// time, so the middleware always uses whatever provider is currently registered.
func New() *Middleware {
	return &Middleware{}
}

// tracer returns the current global tracer, falling back to a no-op if the
// provider is nil or panics (e.g. after a test shutdown).
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

func (m *Middleware) WrapExec(next quark.ExecFunc) quark.ExecFunc {
	return func(ctx context.Context, exec quark.Executor, sqlStr string, args []any) (res sql.Result, err error) {
		ctx, span := m.tracer().Start(ctx, "quark.exec",
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(
				attribute.String("db.statement", sqlStr),
				attribute.String("db.operation", "EXEC"),
			),
		)
		defer func() {
			if err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
			}
			span.End()
		}()

		return next(ctx, exec, sqlStr, args)
	}
}

func (m *Middleware) WrapQuery(next quark.QueryFunc) quark.QueryFunc {
	return func(ctx context.Context, exec quark.Executor, sqlStr string, args []any) (rows *sql.Rows, err error) {
		ctx, span := m.tracer().Start(ctx, "quark.query",
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(
				attribute.String("db.statement", sqlStr),
				attribute.String("db.operation", "SELECT"),
			),
		)
		defer func() {
			if err != nil {
				span.RecordError(err)
				span.SetStatus(codes.Error, err.Error())
			}
			span.End()
		}()

		return next(ctx, exec, sqlStr, args)
	}
}

func (m *Middleware) WrapQueryRow(next quark.QueryRowFunc) quark.QueryRowFunc {
	return func(ctx context.Context, exec quark.Executor, sqlStr string, args []any) *sql.Row {
		ctx, span := m.tracer().Start(ctx, "quark.query_row",
			trace.WithSpanKind(trace.SpanKindClient),
			trace.WithAttributes(
				attribute.String("db.statement", sqlStr),
				attribute.String("db.operation", "SELECT_ROW"),
			),
		)
		// Note: spans for QueryRow are tricky as we don't see the error until Scan.
		defer span.End()

		return next(ctx, exec, sqlStr, args)
	}
}
