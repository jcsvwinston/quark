package exercise

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/examples/superapp/recorder"
	quarkotel "github.com/jcsvwinston/quark/otel"

	otelapi "go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// obsProbe es la sonda de observabilidad: filas con un valor "secreto" en el
// bind para los asserts de redacción (el secreto NUNCA debe aparecer en spans
// redactados ni en el log de Quark; SÍ debe aparecer bajo IncludeArgs, que es
// el opt-in documentado de debugging).
type obsProbe struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name" quark:"not_null"`
	Val  int64  `db:"val" default:"0"`
}

func (obsProbe) TableName() string { return "obs_probes" }

// obsMissing apunta a una tabla que NUNCA se migra: el error "no such table"
// del motor atraviesa el pipeline (y por tanto el middleware OTel) en los 6
// motores. Nota: una COLUMNA inexistente no sirve como trigger portable — en
// SQLite `"col_falsa"` degrada a literal string (la misfeature DQS) y la query
// "funciona" con 0 filas en vez de fallar.
type obsMissing struct {
	ID int64 `db:"id" pk:"true"`
}

func (obsMissing) TableName() string { return "obs_missing_xyz" }

// OBSERVABILITY ejerce la pila de observabilidad EN PROCESO, sin backends
// externos (la versión Docker-real con Jaeger+Redis vive en
// recorder/infra_test.go, tag superapp_infra):
//
//   - otel.Middleware contra providers GLOBALES in-memory del SDK
//     (tracetest.SpanRecorder + sdkmetric.ManualReader), instalados al entrar
//     y restaurados SIEMPRE al salir — el middleware resuelve el tracer por
//     llamada y los instrumentos por sync.Once desde los globales.
//   - Redacción de spans: con el default RedactArgs el bind secreto no llega a
//     ningún atributo (db.statement lleva el SQL parametrizado, sin
//     db.statement.args); con IncludeArgs el secreto SÍ viaja en
//     db.statement.args — ambos lados del contrato, asertados.
//   - db.system (WithDBSystem) en cada span; un error SQL real marca el span
//     con codes.Error; las métricas quark.queries.total suman cada operación.
//   - Logger de Quark (WithLogger + WithSlowQueryThreshold(1ns)): narra cada
//     query con SQL parametrizado y SIN el valor del bind.
//
// Corre en los 6 motores: no necesita aprovisionar nada.
var OBSERVABILITY = Exerciser{Name: "observability", Fn: func(ctx context.Context, client *quark.Client, rec *recorder.Recorder, conn Conn) error {
	// --- Providers globales in-memory, con restore garantizado. -------------
	prevTP := otelapi.GetTracerProvider()
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	otelapi.SetTracerProvider(tp)
	defer func() {
		// Orden deliberado: restaurar el global ANTES del Shutdown — cualquier
		// código concurrente ya ve el provider anterior mientras tp vacía sus
		// spans pendientes (mismo patrón que otel/otel_test.go).
		otelapi.SetTracerProvider(prevTP)
		_ = tp.Shutdown(context.Background())
	}()
	prevMP := otelapi.GetMeterProvider()
	mreader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(mreader))
	otelapi.SetMeterProvider(mp)
	defer func() {
		otelapi.SetMeterProvider(prevMP)
		_ = mp.Shutdown(context.Background())
	}()

	// --- Client instrumentado: OTel (redactado) + logger narrando todo. -----
	rec.Note(OTL("New"), OTL("WithDBSystem"), OTL("Middleware"), OTL("Option"),
		OTL("RedactionMode"), OTL("RedactArgs"),
		QF("WithMiddleware"), QF("WithLogger"), QF("WithSlowQueryThreshold"))
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	l := quark.DefaultLimits()
	l.SafeMigrations = false
	obsClient, err := quark.New(conn.Driver, conn.DSN, append(rec.Options(),
		quark.WithMiddleware(quarkotel.New(quarkotel.WithDBSystem(string(conn.Engine)))),
		quark.WithLogger(logger),
		quark.WithSlowQueryThreshold(time.Nanosecond),
		quark.WithLimits(l))...)
	if err != nil {
		return fmt.Errorf("client observado: %w", err)
	}
	defer obsClient.Close()

	if err := obsClient.Migrate(ctx, &obsProbe{}); err != nil {
		return fmt.Errorf("migrate obs_probes: %w", err)
	}
	defer func() { _, _ = obsClient.Raw().ExecContext(context.Background(), "DROP TABLE obs_probes") }()
	_, _ = obsClient.Raw().ExecContext(ctx, "DELETE FROM obs_probes")

	// Operaciones sobre las TRES vías del pipeline (exec / query / query_row),
	// con un bind secreto que la redacción debe retener.
	const secret = "obs-SECRET-93c1"
	rec.Note(OTL("(*Middleware).WrapExec"), OTL("(*Middleware).WrapQuery"), OTL("(*Middleware).WrapQueryRow"))
	row := &obsProbe{Name: secret, Val: 1}
	if err := quark.For[obsProbe](rec.Mark(ctx, QM("Create")), obsClient).Create(row); err != nil {
		return fmt.Errorf("create: %w", err)
	}
	got, err := quark.For[obsProbe](rec.Mark(ctx, QM("First")), obsClient).Where("name", "=", secret).First()
	if err != nil {
		return fmt.Errorf("first: %w", err)
	}
	got.Val = 2
	if _, err := quark.For[obsProbe](rec.Mark(ctx, QM("Update")), obsClient).Update(&got); err != nil {
		return fmt.Errorf("update: %w", err)
	}
	if _, err := quark.For[obsProbe](rec.Mark(ctx, QM("List")), obsClient).Limit(5).List(); err != nil {
		return fmt.Errorf("list: %w", err)
	}
	// Un error SQL real (tabla inexistente) debe marcar su span con
	// codes.Error en cualquier motor. Tiene que ir por el path QUERY (List):
	// query_row devuelve *sql.Row, que difiere el error al Scan — el span ya
	// cerró cuando el fallo aflora, así que ahí no puede marcarse.
	if _, err := quark.For[obsMissing](ctx, obsClient).Limit(1).List(); err == nil {
		return fmt.Errorf("esperaba error del motor por tabla inexistente")
	}

	// --- Asserts de spans (lado redactado). ----------------------------------
	spans := sr.Ended()
	if len(spans) < 5 {
		return fmt.Errorf("esperaba ≥5 spans (create/first/update/list/error), got %d", len(spans))
	}
	// Set exhaustivo HOY: el middleware sólo envuelve las 3 vías del pipeline
	// (los pings/Begin del driver no pasan por él). Si quark añadiera
	// WrapPing/WrapBeginTx al contrato Middleware, ampliar este set.
	validNames := map[string]bool{"quark.exec": true, "quark.query": true, "quark.query_row": true}
	var sawStatement, sawError bool
	for _, s := range spans {
		if !validNames[s.Name()] {
			return fmt.Errorf("span con nombre inesperado %q", s.Name())
		}
		var hasSystem bool
		for _, kv := range s.Attributes() {
			switch string(kv.Key) {
			case "db.statement":
				sawStatement = true
				if strings.Contains(kv.Value.AsString(), secret) {
					return fmt.Errorf("FUGA: el bind secreto apareció en db.statement del span %s", s.Name())
				}
			case "db.statement.args":
				return fmt.Errorf("FUGA: db.statement.args presente bajo RedactArgs (span %s)", s.Name())
			case "db.system":
				hasSystem = true
				if kv.Value.AsString() != string(conn.Engine) {
					return fmt.Errorf("db.system=%q, esperaba %q", kv.Value.AsString(), conn.Engine)
				}
			}
		}
		if !hasSystem {
			return fmt.Errorf("span %s sin atributo db.system", s.Name())
		}
		if s.Status().Code == codes.Error {
			sawError = true
		}
	}
	if !sawStatement {
		return fmt.Errorf("ningún span llevó db.statement")
	}
	if !sawError {
		return fmt.Errorf("el error del motor no marcó ningún span con codes.Error")
	}

	// --- Asserts de métricas: quark.queries.total suma las operaciones. ------
	var rm metricdata.ResourceMetrics
	if err := mreader.Collect(ctx, &rm); err != nil {
		return fmt.Errorf("collect métricas: %w", err)
	}
	var total int64
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "quark.queries.total" {
				continue
			}
			if sum, ok := m.Data.(metricdata.Sum[int64]); ok {
				for _, dp := range sum.DataPoints {
					total += dp.Value
				}
			}
		}
	}
	if total < int64(len(spans)) {
		return fmt.Errorf("quark.queries.total=%d, esperaba ≥ %d (un punto por operación)", total, len(spans))
	}

	// --- Asserts del logger de Quark: narra TODO (threshold 1ns) sin binds. --
	logs := logBuf.String()
	if logs == "" {
		return fmt.Errorf("el logger de Quark no narró nada con SlowQueryThreshold=1ns")
	}
	if strings.Contains(logs, secret) {
		return fmt.Errorf("FUGA DE REDACCIÓN: el bind secreto apareció en el log de Quark")
	}
	if !strings.Contains(logs, "obs_probes") {
		return fmt.Errorf("el log no menciona la tabla consultada (¿narró las queries?)")
	}

	// --- IncludeArgs: el opt-in de debugging SÍ expone los args. -------------
	rec.Note(OTL("WithSpanRedaction"), OTL("IncludeArgs"))
	// Sin WithDBSystem a propósito: este client sólo asierta el opt-in de
	// db.statement.args; sus spans NO entran en el assert de db.system de
	// arriba (que itera sólo los spans previos a `before`).
	dbgClient, err := quark.New(conn.Driver, conn.DSN,
		quark.WithMiddleware(quarkotel.New(quarkotel.WithSpanRedaction(quarkotel.IncludeArgs))),
		quark.WithLimits(l))
	if err != nil {
		return fmt.Errorf("client debug: %w", err)
	}
	defer dbgClient.Close()
	const secret2 = "obs-DEBUG-41aa"
	before := len(sr.Ended())
	if _, err := quark.For[obsProbe](ctx, dbgClient).Where("name", "=", secret2).Count(); err != nil {
		return fmt.Errorf("count debug: %w", err)
	}
	var sawArgs bool
	for _, s := range sr.Ended()[before:] {
		for _, kv := range s.Attributes() {
			if string(kv.Key) == "db.statement.args" {
				sawArgs = true
				if !strings.Contains(fmt.Sprintf("%v", kv.Value.AsStringSlice()), secret2) {
					return fmt.Errorf("IncludeArgs activo pero el arg no está en db.statement.args")
				}
			}
		}
	}
	if !sawArgs {
		return fmt.Errorf("IncludeArgs no expuso db.statement.args en ningún span nuevo")
	}
	return nil
}}
