package quark_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
	quarkotel "github.com/jcsvwinston/quark/otel"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"

	_ "modernc.org/sqlite"
)

// setupSuiteOtel builds a TracerProvider for the SharedSuite OpenTelemetry tests.
//
// When the OTLP collector is reachable at localhost:4318, a combined provider
// is created with both the OTLP exporter (→ Jaeger) and an InMemoryExporter
// (→ assertions). The service name is "quark-<dialect>".
// When the collector is unavailable the provider uses only InMemory, so the
// test never skips and assertions always work.
//
// Returns the InMemoryExporter for span assertions, a bool indicating whether
// the real collector is active, and a shutdown function.
func setupSuiteOtel(ctx context.Context, dialect string) (*tracetest.InMemoryExporter, bool, func()) {
	const otlpEndpoint = "localhost:4318"
	serviceName := fmt.Sprintf("quark-%s", dialect)

	memExporter := tracetest.NewInMemoryExporter()

	res, _ := resource.New(ctx,
		resource.WithTelemetrySDK(),
		resource.WithAttributes(semconv.ServiceName(serviceName)),
	)

	// Try OTLP exporter
	otlpExp, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(otlpEndpoint),
		otlptracehttp.WithInsecure(),
	)

	var provider *sdktrace.TracerProvider
	realCollector := err == nil
	if realCollector {
		provider = sdktrace.NewTracerProvider(
			sdktrace.WithBatcher(otlpExp),
			sdktrace.WithSyncer(memExporter),
			sdktrace.WithResource(res),
		)
	} else {
		provider = sdktrace.NewTracerProvider(
			sdktrace.WithSyncer(memExporter),
		)
	}

	otel.SetTracerProvider(provider)

	shutdown := func() {
		if realCollector {
			// Flush so all spans reach Jaeger before shutdown.
			time.Sleep(500 * time.Millisecond)
			_ = provider.ForceFlush(ctx)
		}
		_ = provider.Shutdown(ctx)
		otel.SetTracerProvider(trace.NewNoopTracerProvider())
	}

	return memExporter, realCollector, shutdown
}

// setupTestTelemetry configura OpenTelemetry con un exporter en memoria para tests
func setupTestTelemetry() (*tracetest.InMemoryExporter, func(context.Context) error) {
	exporter := tracetest.NewInMemoryExporter()

	provider := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exporter),
	)

	// Configurar como provider global para que el middleware lo use
	otel.SetTracerProvider(provider)

	shutdown := func(ctx context.Context) error {
		// Restore a no-op provider so subsequent tests don't get a nil provider.
		otel.SetTracerProvider(trace.NewNoopTracerProvider())
		return provider.Shutdown(ctx)
	}

	return exporter, shutdown
}

// TestOtel_InsertSpans verifica que las operaciones INSERT generan trazas (via QueryRow para RETURNING)
func TestOtel_InsertSpans(t *testing.T) {
	exporter, shutdown := setupTestTelemetry()
	defer shutdown(context.Background())

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	client, err := quark.New(db, quark.WithDialect(quark.SQLite()), quark.WithMiddleware(quarkotel.New()))
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	type TestUser struct {
		ID    int64  `db:"id" pk:"true"`
		Name  string `db:"name"`
		Email string `db:"email"`
	}

	// Crear tabla (genera spans via Exec)
	if err := client.Migrate(ctx, &TestUser{}); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}

	exporter.Reset()

	// Insertar usuario - en SQLite con RETURNING usa QueryRow
	user := &TestUser{Name: "Test", Email: "test@example.com"}
	if err := quark.For[TestUser](ctx, client).Create(user); err != nil {
		t.Fatalf("create failed: %v", err)
	}

	// Verificar spans - en SQLite el INSERT usa QueryRow por el RETURNING
	spans := exporter.GetSpans()
	if len(spans) == 0 {
		t.Fatal("expected spans to be recorded")
	}

	// Buscar span de query_row (SQLite usa RETURNING que requiere QueryRow)
	foundInsert := false
	for _, span := range spans {
		if span.Name == "quark.query_row" {
			for _, attr := range span.Attributes {
				if attr.Key == "db.statement" {
					stmt := attr.Value.AsString()
					if strings.Contains(stmt, "INSERT") {
						foundInsert = true
						t.Logf("Found INSERT span: %s", stmt)
					}
				}
			}
		}
	}

	if !foundInsert {
		// Debug
		t.Logf("DEBUG: Total spans: %d", len(spans))
		for i, span := range spans {
			t.Logf("DEBUG: Span[%d]: Name=%s", i, span.Name)
		}
		t.Error("expected INSERT span (via query_row due to SQLite RETURNING)")
	}
}

// TestOtel_QuerySpans verifica que las operaciones SELECT generan trazas
func TestOtel_QuerySpans(t *testing.T) {
	exporter, shutdown := setupTestTelemetry()
	defer shutdown(context.Background())

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	client, err := quark.New(db, quark.WithDialect(quark.SQLite()), quark.WithMiddleware(quarkotel.New()))
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	type TestUser struct {
		ID    int64  `db:"id" pk:"true"`
		Name  string `db:"name"`
		Email string `db:"email"`
	}

	if err := client.Migrate(ctx, &TestUser{}); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}

	// Insertar datos
	for i := 0; i < 3; i++ {
		u := &TestUser{Name: "User", Email: "user@example.com"}
		quark.For[TestUser](ctx, client).Create(u)
	}

	exporter.Reset()

	// Consultar (genera spans)
	users, err := quark.For[TestUser](ctx, client).List()
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(users) != 3 {
		t.Errorf("expected 3 users, got %d", len(users))
	}

	// Verificar spans de query
	spans := exporter.GetSpans()
	foundQuery := false
	for _, span := range spans {
		if span.Name == "quark.query" {
			foundQuery = true
			// Verificar atributos
			hasDBOperation := false
			for _, attr := range span.Attributes {
				if attr.Key == "db.operation" && attr.Value.AsString() == "SELECT" {
					hasDBOperation = true
					break
				}
			}
			if !hasDBOperation {
				t.Error("expected db.operation=SELECT attribute")
			}
		}
	}

	if !foundQuery {
		t.Logf("DEBUG: Total spans: %d", len(spans))
		for i, span := range spans {
			t.Logf("DEBUG: Span[%d]: Name=%s", i, span.Name)
		}
		t.Error("expected at least one quark.query span")
	}
}

// TestOtel_FirstOperation verifica que First() genera spans
func TestOtel_FirstOperation(t *testing.T) {
	exporter, shutdown := setupTestTelemetry()
	defer shutdown(context.Background())

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	client, err := quark.New(db, quark.WithDialect(quark.SQLite()), quark.WithMiddleware(quarkotel.New()))
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	type TestUser struct {
		ID    int64  `db:"id" pk:"true"`
		Name  string `db:"name"`
		Email string `db:"email"`
	}

	if err := client.Migrate(ctx, &TestUser{}); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}

	// Insertar y limpiar spans
	u := &TestUser{Name: "Single", Email: "single@example.com"}
	quark.For[TestUser](ctx, client).Create(u)
	exporter.Reset()

	// Buscar uno
	found, err := quark.For[TestUser](ctx, client).First()
	if err != nil {
		t.Fatalf("first failed: %v", err)
	}
	if found.Name != "Single" {
		t.Errorf("expected name 'Single', got %s", found.Name)
	}

	// Verificar spans - First() puede usar query o query_row dependiendo de la implementación
	spans := exporter.GetSpans()
	foundSpan := false
	for _, span := range spans {
		if span.Name == "quark.query" || span.Name == "quark.query_row" {
			foundSpan = true
			// Verificar que es una operación SELECT
			for _, attr := range span.Attributes {
				if attr.Key == "db.statement" {
					stmt := attr.Value.AsString()
					if strings.Contains(stmt, "SELECT") {
						t.Logf("Found SELECT span: %s", stmt)
					}
				}
			}
		}
	}

	if !foundSpan {
		t.Logf("DEBUG: Total spans: %d", len(spans))
		for i, span := range spans {
			t.Logf("DEBUG: Span[%d]: Name=%s", i, span.Name)
		}
		t.Error("expected at least one quark.query or quark.query_row span")
	}
}

// TestOtel_Attributes verifica que los spans tienen los atributos correctos
func TestOtel_Attributes(t *testing.T) {
	exporter, shutdown := setupTestTelemetry()
	defer shutdown(context.Background())

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	client, err := quark.New(db, quark.WithDialect(quark.SQLite()), quark.WithMiddleware(quarkotel.New()))
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	type TestUser struct {
		ID    int64  `db:"id" pk:"true"`
		Name  string `db:"name"`
		Email string `db:"email"`
	}

	if err := client.Migrate(ctx, &TestUser{}); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}

	exporter.Reset()

	// Operación que genera spans
	users, _ := quark.For[TestUser](ctx, client).List()
	_ = users

	spans := exporter.GetSpans()
	if len(spans) == 0 {
		t.Fatal("no spans found")
	}

	// Verificar atributos en el primer span
	span := spans[0]
	hasDBStatement := false
	hasDBOperation := false
	for _, attr := range span.Attributes {
		if attr.Key == "db.statement" {
			hasDBStatement = true
			if attr.Value.AsString() == "" {
				t.Error("db.statement should not be empty")
			}
		}
		if attr.Key == "db.operation" {
			hasDBOperation = true
		}
	}

	if !hasDBStatement {
		t.Error("expected db.statement attribute")
	}
	if !hasDBOperation {
		t.Error("expected db.operation attribute")
	}
}

// TestOtel_TransactionSpans verifica que las transacciones generan spans
func TestOtel_TransactionSpans(t *testing.T) {
	exporter, shutdown := setupTestTelemetry()
	defer shutdown(context.Background())

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	client, err := quark.New(db, quark.WithDialect(quark.SQLite()), quark.WithMiddleware(quarkotel.New()))
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	type TestUser struct {
		ID   int64  `db:"id" pk:"true"`
		Name string `db:"name"`
	}

	if err := client.Migrate(ctx, &TestUser{}); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}

	exporter.Reset()

	// Ejecutar transacción
	err = client.Tx(ctx, func(tx *quark.Tx) error {
		return quark.ForTx[TestUser](ctx, tx).Create(&TestUser{Name: "TxUser"})
	})
	if err != nil {
		t.Fatalf("transaction failed: %v", err)
	}

	// Verificar que se generaron spans
	spans := exporter.GetSpans()
	if len(spans) == 0 {
		t.Error("expected spans within transaction")
	}
}

// TestOtel_ContextPropagation verifica que el contexto se propaga correctamente
func TestOtel_ContextPropagation(t *testing.T) {
	exporter, shutdown := setupTestTelemetry()
	defer shutdown(context.Background())

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	client, err := quark.New(db, quark.WithDialect(quark.SQLite()), quark.WithMiddleware(quarkotel.New()))
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	type TestUser struct {
		ID   int64  `db:"id" pk:"true"`
		Name string `db:"name"`
	}

	if err := client.Migrate(ctx, &TestUser{}); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}

	// Verificar que no hay timeout
	user := &TestUser{Name: "ContextTest"}
	if err := quark.For[TestUser](ctx, client).Create(user); err != nil {
		t.Fatalf("create failed: %v", err)
	}

	// Los spans deben existir
	spans := exporter.GetSpans()
	if len(spans) == 0 {
		t.Fatal("expected spans with context")
	}
}

// TestIntegration_WithRealCollector test de integración con el collector real (skip en short mode)
func TestIntegration_WithRealCollector(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	// Intentar conectar con el collector local
	endpoint := "http://localhost:4318"

	ctx := context.Background()
	shutdown, err := setupOTLP(ctx, endpoint, "quark-test")

	if err != nil {
		t.Skipf("OpenTelemetry collector not available at %s: %v", endpoint, err)
	}
	defer shutdown(ctx)

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	client, err := quark.New(db, quark.WithDialect(quark.SQLite()), quark.WithMiddleware(quarkotel.New()))
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	type TestUser struct {
		ID    int64  `db:"id" pk:"true"`
		Name  string `db:"name"`
		Email string `db:"email"`
	}

	if err := client.Migrate(ctx, &TestUser{}); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}

	// Realizar operaciones que generarán trazas enviadas al collector
	for i := 0; i < 5; i++ {
		u := &TestUser{Name: "Integration", Email: "test@example.com"}
		if err := quark.For[TestUser](ctx, client).Create(u); err != nil {
			t.Fatalf("create failed: %v", err)
		}
	}

	users, err := quark.For[TestUser](ctx, client).List()
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(users) != 5 {
		t.Errorf("expected 5 users, got %d", len(users))
	}

	t.Logf("✓ Traces enviadas correctamente al collector en %s", endpoint)
	t.Log("  Ver en: http://localhost:16686 (Jaeger UI)")
}

// setupOTLP creates a real OTLP TracerProvider and registers it globally.
// Returns a shutdown function. Replaces the GoFrame observe.SetupOpenTelemetry helper.
func setupOTLP(ctx context.Context, endpoint, svcName string) (func(context.Context), error) {
	exp, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(endpoint),
		otlptracehttp.WithInsecure(),
	)
	if err != nil {
		return nil, err
	}
	res, _ := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(svcName)),
	)
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),
		sdktrace.WithResource(res),
	)
	otel.SetTracerProvider(tp)
	return func(ctx context.Context) { _ = tp.Shutdown(ctx) }, nil
}
