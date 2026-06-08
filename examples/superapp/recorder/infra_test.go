//go:build superapp_infra

// Verificación Docker-backed de las capacidades de OBSERVABILIDAD y CACHÉ que
// Quark provee, montadas TODAS a la vez sobre un mismo Client junto al recorder
// de la superapp. Prueba que el arnés compone con la pila real del ORM, no que
// la reemplaza:
//
//   - recorder (S2)        → quark.WithMiddleware + quark.WithQueryObserver
//   - OTel (otel.New)      → quark.WithMiddleware  → spans/métricas a Jaeger real
//   - logger slog          → quark.WithLogger + WithSlowQueryThreshold(1ns)
//     (umbral mínimo ⇒ Quark narra CADA query)
//   - caché Redis real     → quark.WithCacheStore(redis.New(...))
//
// Aislado tras el build tag `superapp_infra` (no corre en `go test` normal).
// Requiere los contenedores de docs/HANDOFF: redis:7-alpine (6379) y
// jaegertracing/all-in-one (OTLP HTTP 4318 + query 16686). Override por env:
// SUPERAPP_REDIS_ADDR, SUPERAPP_OTLP_ENDPOINT, SUPERAPP_JAEGER_QUERY.
//
//	docker run -d --name superapp-redis  -p 6379:6379 redis:7-alpine
//	docker run -d --name superapp-jaeger -e COLLECTOR_OTLP_ENABLED=true \
//	    -p 16686:16686 -p 4318:4318 jaegertracing/all-in-one:1.55
//	go test -tags=superapp_infra -run TestObservabilityAndCacheInfra -v ./examples/superapp/recorder/
package recorder

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/cache/redis"
	"github.com/jcsvwinston/quark/examples/superapp/control"
	qotel "github.com/jcsvwinston/quark/otel"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	_ "modernc.org/sqlite"
)

const otelServiceName = "quark-superapp-infra"

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func TestObservabilityAndCacheInfra(t *testing.T) {
	ctx := context.Background()
	redisAddr := envOr("SUPERAPP_REDIS_ADDR", "localhost:6379")
	otlpEndpoint := envOr("SUPERAPP_OTLP_ENDPOINT", "localhost:4318")

	// --- Redis real: si no responde, la infra no está levantada (no se hace
	// skip: la regla del repo prohíbe gatear por env; el caller monta Docker). ---
	store := redis.New(redis.Options{Addr: redisAddr})
	if err := store.Ping(ctx); err != nil {
		t.Fatalf("redis no responde en %s: %v\nLevanta: docker run -d --name superapp-redis -p 6379:6379 redis:7-alpine", redisAddr, err)
	}

	// --- OTel real: exporter OTLP/HTTP → Jaeger, instalado como TracerProvider
	// global (el otel.Middleware de Quark usa otel.GetTracerProvider()). ---
	exp, err := otlptracehttp.New(ctx,
		otlptracehttp.WithEndpoint(otlpEndpoint),
		otlptracehttp.WithInsecure(),
	)
	if err != nil {
		t.Fatalf("otlp exporter (%s): %v", otlpEndpoint, err)
	}
	res := resource.NewSchemaless(attribute.String("service.name", otelServiceName))
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res))
	prevTP := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		_ = tp.ForceFlush(ctx)
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = tp.Shutdown(shutCtx)
		otel.SetTracerProvider(prevTP)
	})

	// --- Logger slog a un buffer, sólo WARN+ (la slow-query es WARN). ---
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	// --- recorder de la superapp (S2) ---
	rec := New(control.SQLite)

	l := quark.DefaultLimits()
	l.SafeMigrations = false

	// SQLite en fichero (no :memory:) para que el pool comparta el mismo schema
	// entre conexiones.
	dsn := filepath.Join(t.TempDir(), "app.db")
	opts := []any{
		quark.WithMiddleware(rec),
		quark.WithQueryObserver(rec),
		quark.WithMiddleware(qotel.New(qotel.WithDBSystem("sqlite"))),
		quark.WithLogger(logger),
		quark.WithSlowQueryThreshold(time.Nanosecond), // narra CADA query
		quark.WithCacheStore(store),
		quark.WithLimits(l),
	}
	client, err := quark.New("sqlite", dsn, opts...)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer client.Close()

	if err := client.Migrate(ctx, &widget{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	rec.Reset()    // descarta el DDL del migrate
	logBuf.Reset() // y sus logs

	// --- Seed ---
	for _, w := range []*widget{{Name: "alpha", Score: 10}, {Name: "beta", Score: 20}} {
		cctx := rec.Mark(ctx, symCreate)
		if err := quark.For[widget](cctx, client).Create(w); err != nil {
			t.Fatalf("create %s: %v", w.Name, err)
		}
	}

	// --- CACHÉ REDIS: la 2ª List idéntica debe ser un hit (0 SQL) ---
	before := rec.Count()
	lctx := rec.Mark(ctx, symList)
	got1, err := quark.For[widget](lctx, client).Cache(time.Minute).Limit(10).List()
	if err != nil {
		t.Fatalf("list#1: %v", err)
	}
	afterFirst := rec.Count()
	got2, err := quark.For[widget](ctx, client).Cache(time.Minute).Limit(10).List()
	if err != nil {
		t.Fatalf("list#2: %v", err)
	}
	afterSecond := rec.Count()

	if afterFirst != before+1 {
		t.Errorf("list#1 debía pegar a la BD (+1 statement): before=%d afterFirst=%d", before, afterFirst)
	}
	if afterSecond != afterFirst {
		t.Errorf("list#2 debía ser CACHE HIT (0 SQL): afterFirst=%d afterSecond=%d", afterFirst, afterSecond)
	}
	if len(got1) != len(got2) || len(got1) != 2 {
		t.Errorf("paridad cache miss/hit rota: got1=%d got2=%d (esperaba 2/2)", len(got1), len(got2))
	}

	// --- LOGGER + REDACCIÓN: query con bind arg secreto; el log debe llevar el
	// SQL parametrizado pero NUNCA el valor del bind. ---
	const secret = "alpha-SECRET-7f3c"
	_, _ = quark.For[widget](rec.Mark(ctx, symFirst), client).
		Where("name", "=", secret).First()

	logs := logBuf.String()
	if !strings.Contains(logs, `"msg":"slow query"`) {
		t.Errorf("el logger no emitió ninguna línea de slow-query; buf=%q", logs)
	}
	if !strings.Contains(logs, `"name\" = ?`) && !strings.Contains(logs, `name" = ?`) {
		t.Errorf("la línea de slow-query no llevaba el SQL parametrizado esperado; buf=%q", logs)
	}
	if strings.Contains(logs, secret) {
		t.Errorf("FUGA DE REDACCIÓN: el valor del bind %q apareció en el log", secret)
	}

	// --- OTel telemetry sanity vía el observer del recorder (las filas reales) ---
	tele := rec.Telemetry()
	t.Logf("recorder telemetry: queries=%d rows=%d byOp=%v", tele.Queries, tele.Rows, tele.ByOp)
	t.Logf("slow-query log lines:\n%s", logs)
	t.Logf("OTel service=%q exportado a %s — verifica en http://localhost:16686 (o /api/traces?service=%s)",
		otelServiceName, otlpEndpoint, otelServiceName)

	// Forzar el flush de spans ANTES de que el test acabe, para que el curl de
	// verificación posterior los encuentre en Jaeger.
	if err := tp.ForceFlush(ctx); err != nil {
		t.Errorf("force flush de spans OTel: %v", err)
	}
}
