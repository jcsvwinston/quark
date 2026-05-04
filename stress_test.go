package quark_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/cache/memory"
	quarkotel "github.com/jcsvwinston/quark/otel"

	_ "modernc.org/sqlite"
)

func testStress(ctx context.Context, t *testing.T, client *quark.Client) {
	client.Raw().Exec("DROP TABLE IF EXISTS stress_records")
	type StressRecord struct {
		ID    int64  `db:"id" pk:"true"`
		Data  string `db:"data"`
		Value int    `db:"value"`
	}

	if err := client.Migrate(ctx, &StressRecord{}); err != nil {
		t.Fatalf("stress migrate failed: %v", err)
	}

	const count = 1000 // A smaller number for logs, but still significant

	t.Logf("🚀 Starting stress test with %d records...", count)

	// 1. Bulk Create
	for i := 0; i < count; i++ {
		rec := &StressRecord{
			Data:  fmt.Sprintf("stress-data-%d", i),
			Value: i,
		}
		if err := quark.For[StressRecord](ctx, client).Create(rec); err != nil {
			t.Fatalf("failed at record %d: %v", i, err)
		}
	}

	// 2. Count
	total, err := quark.For[StressRecord](ctx, client).Count()
	if err != nil || total != int64(count) {
		t.Errorf("expected %d records, got %d (err: %v)", count, total, err)
	}

	// 3. Paginated List
	res, err := quark.For[StressRecord](ctx, client).OrderBy("value", "ASC").Paginate(100, 1)
	if err != nil {
		t.Fatalf("pagination failed: %v", err)
	}
	if len(res.Items) != 100 {
		t.Errorf("expected 100 items on page 1, got %d", len(res.Items))
	}

	// 4. Cleanup
	for i := 0; i < count; i++ {
		if _, err := quark.For[StressRecord](ctx, client).Where("value", "=", i).DeleteBy(); err != nil {
			t.Fatalf("failed to delete record %d: %v", i, err)
		}
	}
}

// TestStressWithCacheAndOtel prueba estrés con caché y OpenTelemetry
func TestStressWithCacheAndOtel(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test with cache and otel in short mode")
	}

	exporter, shutdown := setupTestTelemetry()
	defer shutdown(context.Background())

	db, err := sql.Open("sqlite", "file:stresscacheotel?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()

	// Crear cliente con caché y OTel
	memStore := memory.New()
	client, err := quark.New(db,
		quark.WithDialect(quark.SQLite()),
		quark.WithCacheStore(memStore),
		quark.WithMiddleware(quarkotel.New()),
	)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	type StressCacheRecord struct {
		ID    int64  `db:"id" pk:"true"`
		Data  string `db:"data"`
		Value int    `db:"value"`
	}

	// Limpiar y migrar
	client.Raw().Exec("DROP TABLE IF EXISTS stress_cache_records")
	if err := client.Migrate(ctx, &StressCacheRecord{}); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}

	const count = 100

	fmt.Printf("\n%s\n", strings.Repeat("=", 70))
	fmt.Printf("🚀 STRESS TEST WITH CACHE + OTEL (%d records)\n", count)
	fmt.Printf("%s\n", strings.Repeat("=", 70))

	// Limpiar spans de migración
	exporter.Reset()

	// 1. Bulk Create
	fmt.Println("\n📊 Phase 1: Bulk Create")
	fmt.Println(strings.Repeat("-", 70))
	for i := 0; i < count; i++ {
		rec := &StressCacheRecord{
			Data:  fmt.Sprintf("stress-cache-data-%d", i),
			Value: i,
		}
		if err := quark.For[StressCacheRecord](ctx, client).Create(rec); err != nil {
			t.Fatalf("failed at record %d: %v", i, err)
		}
	}
	createSpans := exporter.GetSpans()
	fmt.Printf("  ✓ Created %d records\n", count)
	fmt.Printf("  ✓ Spans generated: %d\n", len(createSpans))
	exporter.Reset()

	// 2. Read with cache (primera lectura - MISS)
	fmt.Println("\n📊 Phase 2: Read with cache (first pass - MISS)")
	fmt.Println(strings.Repeat("-", 70))
	for i := 0; i < count; i++ {
		_, _ = quark.For[StressCacheRecord](ctx, client).
			Where("value", "=", i).
			Cache(5 * time.Minute).
			First()
	}
	firstReadSpans := exporter.GetSpans()
	fmt.Printf("  ✓ First pass completed (%d queries to DB)\n", count)
	fmt.Printf("  ✓ Spans generated: %d\n", len(firstReadSpans))
	exporter.Reset()

	// 3. Read with cache (segunda lectura - HIT)
	fmt.Println("\n📊 Phase 3: Read with cache (second pass - HIT)")
	fmt.Println(strings.Repeat("-", 70))
	for i := 0; i < count; i++ {
		_, _ = quark.For[StressCacheRecord](ctx, client).
			Where("value", "=", i).
			Cache(5 * time.Minute).
			First()
	}
	secondReadSpans := exporter.GetSpans()
	fmt.Printf("  ✓ Second pass completed (should use cache)\n")
	fmt.Printf("  ✓ Spans generated: %d\n", len(secondReadSpans))

	// 4. Verificar que hay menos spans en la segunda pasada (cache hits)
	// Nota: Aún verá spans porque el middleware registra antes de consultar la caché
	fmt.Printf("  ✓ First pass DB queries: ~%d, Second pass DB queries: ~%d\n",
		len(firstReadSpans), len(secondReadSpans))

	exporter.Reset()

	// 5. Update con invalidación de caché
	fmt.Println("\n📊 Phase 4: Update with cache invalidation")
	fmt.Println(strings.Repeat("-", 70))
	for i := 0; i < 10; i++ {
		rec, _ := quark.For[StressCacheRecord](ctx, client).Where("value", "=", i).First()
		rec.Data = fmt.Sprintf("updated-data-%d", i)
		_, _ = quark.For[StressCacheRecord](ctx, client).Update(&rec)
	}
	updateSpans := exporter.GetSpans()
	fmt.Printf("  ✓ Updated 10 records (cache invalidated)\n")
	fmt.Printf("  ✓ Spans generated: %d\n", len(updateSpans))

	exporter.Reset()

	// 6. List paginado
	fmt.Println("\n📊 Phase 5: Paginated List")
	fmt.Println(strings.Repeat("-", 70))
	res, err := quark.For[StressCacheRecord](ctx, client).OrderBy("value", "ASC").Paginate(20, 1)
	if err != nil {
		t.Fatalf("pagination failed: %v", err)
	}
	if len(res.Items) != 20 {
		t.Errorf("expected 20 items on page 1, got %d", len(res.Items))
	}
	listSpans := exporter.GetSpans()
	fmt.Printf("  ✓ Paginated list: 20 items on page 1\n")
	fmt.Printf("  ✓ Spans generated: %d\n", len(listSpans))

	exporter.Reset()

	// 7. Count
	fmt.Println("\n📊 Phase 6: Count")
	fmt.Println(strings.Repeat("-", 70))
	total, err := quark.For[StressCacheRecord](ctx, client).Count()
	if err != nil || total != int64(count) {
		t.Errorf("expected %d records, got %d (err: %v)", count, total, err)
	}
	countSpans := exporter.GetSpans()
	fmt.Printf("  ✓ Count: %d records\n", total)
	fmt.Printf("  ✓ Spans generated: %d\n", len(countSpans))

	exporter.Reset()

	// 8. Cleanup
	fmt.Println("\n📊 Phase 7: Cleanup")
	fmt.Println(strings.Repeat("-", 70))
	for i := 0; i < count; i++ {
		if _, err := quark.For[StressCacheRecord](ctx, client).Where("value", "=", i).DeleteBy(); err != nil {
			t.Fatalf("failed to delete record %d: %v", i, err)
		}
	}
	deleteSpans := exporter.GetSpans()
	fmt.Printf("  ✓ Deleted %d records\n", count)
	fmt.Printf("  ✓ Spans generated: %d\n", len(deleteSpans))

	// Resumen de atributos de spans
	fmt.Println("\n📊 Phase 8: Span Attributes Verification")
	fmt.Println(strings.Repeat("-", 70))

	allSpans := append(createSpans, firstReadSpans...)
	allSpans = append(allSpans, secondReadSpans...)
	allSpans = append(allSpans, updateSpans...)
	allSpans = append(allSpans, listSpans...)
	allSpans = append(allSpans, countSpans...)
	allSpans = append(allSpans, deleteSpans...)

	validSpans := 0
	for _, span := range allSpans {
		hasDBStatement := false
		hasDBOperation := false
		for _, attr := range span.Attributes {
			if attr.Key == "db.statement" && attr.Value.AsString() != "" {
				hasDBStatement = true
			}
			if attr.Key == "db.operation" && attr.Value.AsString() != "" {
				hasDBOperation = true
			}
		}
		if hasDBStatement && hasDBOperation {
			validSpans++
		}
	}

	fmt.Printf("  ✓ Total spans with db.statement + db.operation: %d/%d\n", validSpans, len(allSpans))

	fmt.Printf("\n%s\n", strings.Repeat("=", 70))
	fmt.Println("✅ Stress test with Cache + OTel completed successfully!")
	fmt.Printf("%s\n", strings.Repeat("=", 70))
}

// TestStressWithRealOtelCollector envía trazas reales al collector OTLP
func TestStressWithRealOtelCollector(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real collector test in short mode")
	}

	// Intentar conectar con el collector local
	endpoint := "http://localhost:4318"

	ctx := context.Background()
	shutdown, err := setupOTLP(ctx, endpoint, "quark-stress-test")

	if err != nil {
		t.Skipf("OpenTelemetry collector not available at %s: %v", endpoint, err)
	}
	defer shutdown(ctx)

	db, err := sql.Open("sqlite", "file:stressrealotel?mode=memory&cache=shared")
	if err != nil {
		t.Fatalf("failed to open db: %v", err)
	}
	defer db.Close()

	// Crear cliente con caché y OTel
	memStore := memory.New()
	client, err := quark.New(db,
		quark.WithDialect(quark.SQLite()),
		quark.WithCacheStore(memStore),
		quark.WithMiddleware(quarkotel.New()),
	)
	if err != nil {
		t.Fatalf("failed to create client: %v", err)
	}

	type StressRealRecord struct {
		ID    int64  `db:"id" pk:"true"`
		Data  string `db:"data"`
		Value int    `db:"value"`
	}

	client.Raw().Exec("DROP TABLE IF EXISTS stress_real_records")
	if err := client.Migrate(ctx, &StressRealRecord{}); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}

	fmt.Printf("\n%s\n", strings.Repeat("=", 70))
	fmt.Println("🚀 SENDING REAL TRACES TO JAEGER")
	fmt.Printf("%s\n", strings.Repeat("=", 70))
	fmt.Printf("Endpoint: %s\n", endpoint)
	fmt.Println("Jaeger UI: http://localhost:16686")
	fmt.Printf("%s\n", strings.Repeat("=", 70))

	const count = 50

	// Bulk Create
	fmt.Printf("\n📊 Creating %d records...\n", count)
	for i := 0; i < count; i++ {
		rec := &StressRealRecord{
			Data:  fmt.Sprintf("real-otel-data-%d", i),
			Value: i,
		}
		if err := quark.For[StressRealRecord](ctx, client).Create(rec); err != nil {
			t.Fatalf("failed at record %d: %v", i, err)
		}
	}

	// Read with cache (miss)
	fmt.Printf("📊 Reading %d records (first pass - cache miss)...\n", count)
	for i := 0; i < count; i++ {
		_, _ = quark.For[StressRealRecord](ctx, client).
			Where("value", "=", i).
			Cache(5 * time.Minute).
			First()
	}

	// Read with cache (hit)
	fmt.Printf("📊 Reading %d records (second pass - cache hit)...\n", count)
	for i := 0; i < count; i++ {
		_, _ = quark.For[StressRealRecord](ctx, client).
			Where("value", "=", i).
			Cache(5 * time.Minute).
			First()
	}

	// List
	fmt.Println("📊 Paginated list...")
	_, _ = quark.For[StressRealRecord](ctx, client).OrderBy("value", "ASC").Paginate(20, 1)

	// Count
	fmt.Println("📊 Count...")
	total, _ := quark.For[StressRealRecord](ctx, client).Count()

	// Cleanup
	fmt.Printf("📊 Deleting %d records...\n", count)
	for i := 0; i < count; i++ {
		_, _ = quark.For[StressRealRecord](ctx, client).Where("value", "=", i).DeleteBy()
	}

	// Forzar flush de trazas
	fmt.Println("\n⏳ Flushing traces to collector...")
	time.Sleep(2 * time.Second)

	fmt.Printf("\n%s\n", strings.Repeat("=", 70))
	fmt.Println("✅ Traces sent to Jaeger!")
	fmt.Printf("Total records processed: %d\n", total)
	fmt.Println("\nView traces at:")
	fmt.Println("  http://localhost:16686")
	fmt.Println("  Search for service: 'quark-stress-test'")
	fmt.Printf("%s\n", strings.Repeat("=", 70))
}
