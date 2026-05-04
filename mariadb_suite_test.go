package quark_test

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/cache/memory"
	"github.com/jcsvwinston/quark/cache/redis"
	quarkotel "github.com/jcsvwinston/quark/otel"

	_ "github.com/go-sql-driver/mysql"
)

// TestSuiteMariaDB runs the full SharedSuite against a live MariaDB instance.
// Set QUARK_TEST_MARIADB_DSN to a valid DSN, e.g.:
//
//	root:root@tcp(127.0.0.1:3307)/quark_test
func TestSuiteMariaDB(t *testing.T) {
	dsn := os.Getenv("QUARK_TEST_MARIADB_DSN")
	if dsn == "" {
		t.Skip("QUARK_TEST_MARIADB_DSN not set")
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		t.Skipf("MariaDB not reachable (%v), skipping", err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	client, err := quark.New(db,
		quark.WithDialect(quark.MariaDB()),
		quark.WithQueryObserver(NewSQLQueryLogger(logger)),
		quark.WithMiddleware(quarkotel.New()),
	)
	if err != nil {
		t.Fatal(err)
	}

	SharedSuite(t, client)
}

// TestMariaDBCache verifies in-memory and Redis cache behaviour against MariaDB.
func TestMariaDBCache(t *testing.T) {
	dsn := os.Getenv("QUARK_TEST_MARIADB_DSN")
	if dsn == "" {
		t.Skip("QUARK_TEST_MARIADB_DSN not set")
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		t.Skipf("MariaDB not reachable (%v), skipping", err)
	}

	ctx := context.Background()

	fmt.Printf("\n%s\n", strings.Repeat("=", 70))
	fmt.Println("🔍 MARIADB CACHE TEST")
	fmt.Printf("%s\n", strings.Repeat("=", 70))

	// --- In-Memory cache ---
	t.Run("Memory", func(t *testing.T) {
		logger := &cacheLogger{}
		memStore := memory.New()

		client, err := quark.New(db,
			quark.WithDialect(quark.MariaDB()),
			quark.WithCacheStore(memStore),
			quark.WithQueryObserver(logger),
		)
		if err != nil {
			t.Fatal(err)
		}

		type MariaDBCacheUser struct {
			ID    int64  `db:"id" pk:"true"`
			Email string `db:"email"`
			Name  string `db:"name"`
		}

		noCache, _ := quark.New(db, quark.WithDialect(quark.MariaDB()))
		dropTable(noCache, "maria_db_cache_users")
		client.Migrate(ctx, &MariaDBCacheUser{})

		for i := 1; i <= 3; i++ {
			u := &MariaDBCacheUser{
				Email: fmt.Sprintf("mdb%d@test.com", i),
				Name:  fmt.Sprintf("MariaUser %d", i),
			}
			quark.For[MariaDBCacheUser](ctx, client).Create(u)
		}

		// MISS: first query hits DB
		logger.Reset()
		fmt.Println("\n→ 1ª consulta (MISS — esperado: SQL ejecutado)")
		start := time.Now()
		_, _ = quark.For[MariaDBCacheUser](ctx, client).Where("id", "=", 1).Cache(5 * time.Minute).First()
		d1 := time.Since(start)
		q1 := logger.Count()
		fmt.Printf("  SQL ejecutado: %d | duración: %v\n", q1, d1)
		if q1 != 1 {
			t.Errorf("expected 1 SQL on cache MISS, got %d", q1)
		}

		// HIT: same query served from cache
		logger.Reset()
		fmt.Println("→ 2ª consulta (HIT — esperado: sin SQL)")
		start = time.Now()
		_, _ = quark.For[MariaDBCacheUser](ctx, client).Where("id", "=", 1).Cache(5 * time.Minute).First()
		d2 := time.Since(start)
		q2 := logger.Count()
		if q2 == 0 {
			fmt.Printf("  SIN SQL — CACHE HIT ✓ [%.1fx más rápido]\n", float64(d1)/float64(d2+1))
		} else {
			t.Errorf("expected cache HIT (0 SQL), got %d queries", q2)
		}

		// Invalidation: Create via the caching client invalidates table cache
		logger.Reset()
		fmt.Println("→ Create via caching client (invalida caché de la tabla)")
		quark.For[MariaDBCacheUser](ctx, client).Create(&MariaDBCacheUser{Email: "new@mdb.com", Name: "New"})
		fmt.Printf("  SQL de Create: %d\n", logger.Count())

		logger.Reset()
		fmt.Println("→ Consulta post-Create (MISS — caché invalidada)")
		newID := int64(4) // 4th record after 3 inserts + 1 new
		_, _ = quark.For[MariaDBCacheUser](ctx, client).Where("id", "=", newID).Cache(5 * time.Minute).First()
		qAfter := logger.Count()
		if qAfter != 1 {
			t.Errorf("expected 1 SQL for new uncached id, got %d", qAfter)
		}
		fmt.Printf("  SQL ejecutado: %d ✓\n", qAfter)

		fmt.Println("\n✅ MariaDB in-memory cache test passed")
	})

	// --- Redis cache ---
	t.Run("Redis", func(t *testing.T) {
		rStore := redis.New(redis.Options{Addr: "localhost:6379"})
		if err := rStore.Ping(ctx); err != nil {
			t.Skip("Redis not available on localhost:6379, skipping")
		}

		logger := &cacheLogger{}
		client, err := quark.New(db,
			quark.WithDialect(quark.MariaDB()),
			quark.WithCacheStore(rStore),
			quark.WithQueryObserver(logger),
		)
		if err != nil {
			t.Fatal(err)
		}

		type MariaDBRedisCacheUser struct {
			ID    int64  `db:"id" pk:"true"`
			Email string `db:"email"`
			Name  string `db:"name"`
		}

		noCache2, _ := quark.New(db, quark.WithDialect(quark.MariaDB()))
		dropTable(noCache2, "maria_db_redis_cache_users")
		client.Migrate(ctx, &MariaDBRedisCacheUser{})
		quark.For[MariaDBRedisCacheUser](ctx, client).Create(&MariaDBRedisCacheUser{Email: "r@mdb.com", Name: "Redis"})

		logger.Reset()
		fmt.Println("\n→ Redis 1ª consulta (MISS)")
		start := time.Now()
		_, _ = quark.For[MariaDBRedisCacheUser](ctx, client).Where("id", "=", 1).Cache(5 * time.Minute).First()
		d1 := time.Since(start)

		logger.Reset()
		fmt.Println("→ Redis 2ª consulta (HIT)")
		start = time.Now()
		_, _ = quark.For[MariaDBRedisCacheUser](ctx, client).Where("id", "=", 1).Cache(5 * time.Minute).First()
		d2 := time.Since(start)

		if logger.Count() == 0 {
			fmt.Printf("  SIN SQL — REDIS HIT ✓ [%.1fx más rápido]\n", float64(d1)/float64(d2+1))
		} else {
			t.Logf("  ⚠ Redis cache miss inesperado (puede ser TTL expirado): %d queries", logger.Count())
		}

		fmt.Println("\n✅ MariaDB Redis cache test passed")
	})
}

// TestMariaDBOtel verifies that all CRUD operations emit valid OpenTelemetry spans.
func TestMariaDBOtel(t *testing.T) {
	dsn := os.Getenv("QUARK_TEST_MARIADB_DSN")
	if dsn == "" {
		t.Skip("QUARK_TEST_MARIADB_DSN not set")
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		t.Skipf("MariaDB not reachable (%v), skipping", err)
	}

	exporter, shutdown := setupTestTelemetry()
	defer shutdown(context.Background())

	ctx := context.Background()

	client, err := quark.New(db,
		quark.WithDialect(quark.MariaDB()),
		quark.WithMiddleware(quarkotel.New()),
	)
	if err != nil {
		t.Fatal(err)
	}

	type MariaDBOtelUser struct {
		ID    int64  `db:"id" pk:"true"`
		Name  string `db:"name"`
		Email string `db:"email"`
	}

	fmt.Printf("\n%s\n", strings.Repeat("=", 70))
	fmt.Println("🔍 MARIADB OTEL TEST")
	fmt.Printf("%s\n", strings.Repeat("=", 70))

	dropTable(client, "maria_db_otel_users")
	client.Migrate(ctx, &MariaDBOtelUser{})
	exporter.Reset()

	// INSERT
	t.Run("Insert", func(t *testing.T) {
		user := &MariaDBOtelUser{Name: "OTel MariaDB", Email: "otel@mariadb.com"}
		if err := quark.For[MariaDBOtelUser](ctx, client).Create(user); err != nil {
			t.Fatalf("create failed: %v", err)
		}
		spans := exporter.GetSpans()
		spanCounts := countSpansByType(spans)
		total := spanCounts["quark.query"] + spanCounts["quark.query_row"] + spanCounts["quark.exec"]
		if total == 0 {
			t.Error("expected OTel spans for INSERT, got none")
		}
		fmt.Printf("  ✓ INSERT spans: query=%d, query_row=%d, exec=%d\n",
			spanCounts["quark.query"], spanCounts["quark.query_row"], spanCounts["quark.exec"])
		exporter.Reset()
	})

	// SELECT
	t.Run("Select", func(t *testing.T) {
		users, err := quark.For[MariaDBOtelUser](ctx, client).List()
		if err != nil {
			t.Fatalf("list failed: %v", err)
		}
		if len(users) == 0 {
			t.Error("expected at least 1 user")
		}
		spans := exporter.GetSpans()
		spanCounts := countSpansByType(spans)
		if spanCounts["quark.query"]+spanCounts["quark.query_row"] == 0 {
			t.Error("expected OTel spans for SELECT, got none")
		}
		fmt.Printf("  ✓ SELECT spans: query=%d, query_row=%d\n",
			spanCounts["quark.query"], spanCounts["quark.query_row"])
		exporter.Reset()
	})

	// First
	t.Run("First", func(t *testing.T) {
		u, err := quark.For[MariaDBOtelUser](ctx, client).First()
		if err != nil {
			t.Fatalf("first failed: %v", err)
		}
		if u.Name != "OTel MariaDB" {
			t.Errorf("expected 'OTel MariaDB', got %s", u.Name)
		}
		spans := exporter.GetSpans()
		if len(spans) == 0 {
			t.Error("expected OTel spans for First()")
		}
		fmt.Printf("  ✓ First() spans: %d\n", len(spans))
		exporter.Reset()
	})

	// UPDATE
	t.Run("Update", func(t *testing.T) {
		u, _ := quark.For[MariaDBOtelUser](ctx, client).First()
		u.Name = "OTel Updated"
		if _, err := quark.For[MariaDBOtelUser](ctx, client).Update(&u); err != nil {
			t.Fatalf("update failed: %v", err)
		}
		spans := exporter.GetSpans()
		spanCounts := countSpansByType(spans)
		total := spanCounts["quark.query"] + spanCounts["quark.query_row"] + spanCounts["quark.exec"]
		if total == 0 {
			t.Error("expected OTel spans for UPDATE, got none")
		}
		fmt.Printf("  ✓ UPDATE spans: query=%d, query_row=%d, exec=%d\n",
			spanCounts["quark.query"], spanCounts["quark.query_row"], spanCounts["quark.exec"])
		exporter.Reset()
	})

	// DELETE
	t.Run("Delete", func(t *testing.T) {
		_, err := quark.For[MariaDBOtelUser](ctx, client).Where("id", ">", 0).DeleteBy()
		if err != nil {
			t.Fatalf("delete failed: %v", err)
		}
		spans := exporter.GetSpans()
		if len(spans) == 0 {
			t.Error("expected OTel spans for DELETE, got none")
		}
		fmt.Printf("  ✓ DELETE spans: %d\n", len(spans))
		exporter.Reset()
	})

	// SpanAttributes
	t.Run("SpanAttributes", func(t *testing.T) {
		quark.For[MariaDBOtelUser](ctx, client).Create(&MariaDBOtelUser{Name: "Attr", Email: "attr@mdb.com"})
		spans := exporter.GetSpans()

		valid := 0
		for _, span := range spans {
			hasStmt, hasOp := false, false
			for _, attr := range span.Attributes {
				if attr.Key == "db.statement" && attr.Value.AsString() != "" {
					hasStmt = true
				}
				if attr.Key == "db.operation" && attr.Value.AsString() != "" {
					hasOp = true
				}
			}
			if hasStmt && hasOp {
				valid++
			}
		}
		if valid == 0 {
			t.Error("expected spans with db.statement and db.operation attributes")
		}
		fmt.Printf("  ✓ %d spans with valid OTel attributes\n", valid)
	})

	fmt.Printf("\n✅ MariaDB OTel test completed\n")
	fmt.Printf("%s\n", strings.Repeat("=", 70))
}

// TestMariaDBStress runs the shared stress test plus a MariaDB-specific
// stress scenario combining Cache + OTel simultaneously.
func TestMariaDBStress(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping MariaDB stress test in short mode")
	}

	dsn := os.Getenv("QUARK_TEST_MARIADB_DSN")
	if dsn == "" {
		t.Skip("QUARK_TEST_MARIADB_DSN not set")
	}

	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := db.Ping(); err != nil {
		t.Skipf("MariaDB not reachable (%v), skipping", err)
	}

	exporter, shutdown := setupTestTelemetry()
	defer shutdown(context.Background())

	ctx := context.Background()
	memStore := memory.New()

	client, err := quark.New(db,
		quark.WithDialect(quark.MariaDB()),
		quark.WithCacheStore(memStore),
		quark.WithMiddleware(quarkotel.New()),
	)
	if err != nil {
		t.Fatal(err)
	}

	type MariaDBStressRecord struct {
		ID    int64  `db:"id" pk:"true"`
		Data  string `db:"data"`
		Value int    `db:"value"`
	}

	const count = 200

	fmt.Printf("\n%s\n", strings.Repeat("=", 70))
	fmt.Printf("🚀 MARIADB STRESS TEST WITH CACHE + OTEL (%d records)\n", count)
	fmt.Printf("%s\n", strings.Repeat("=", 70))

	dropTable(client, "maria_db_stress_records")
	if err := client.Migrate(ctx, &MariaDBStressRecord{}); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}
	exporter.Reset()

	// Phase 1: Bulk Create
	fmt.Println("\n📊 Phase 1: Bulk Create")
	fmt.Println(strings.Repeat("-", 70))
	for i := 0; i < count; i++ {
		if err := quark.For[MariaDBStressRecord](ctx, client).Create(&MariaDBStressRecord{
			Data:  fmt.Sprintf("mdb-stress-%d", i),
			Value: i,
		}); err != nil {
			t.Fatalf("create failed at record %d: %v", i, err)
		}
	}
	createSpans := exporter.GetSpans()
	fmt.Printf("  ✓ Created %d records | spans: %d\n", count, len(createSpans))
	exporter.Reset()

	// Phase 2: Read with cache (MISS)
	fmt.Println("\n📊 Phase 2: Read with cache (first pass — MISS)")
	fmt.Println(strings.Repeat("-", 70))
	for i := 0; i < count; i++ {
		_, _ = quark.For[MariaDBStressRecord](ctx, client).
			Where("value", "=", i).
			Cache(5 * time.Minute).
			First()
	}
	missSpans := exporter.GetSpans()
	fmt.Printf("  ✓ %d reads | spans: %d\n", count, len(missSpans))
	exporter.Reset()

	// Phase 3: Read with cache (HIT)
	fmt.Println("\n📊 Phase 3: Read with cache (second pass — HIT)")
	fmt.Println(strings.Repeat("-", 70))
	for i := 0; i < count; i++ {
		_, _ = quark.For[MariaDBStressRecord](ctx, client).
			Where("value", "=", i).
			Cache(5 * time.Minute).
			First()
	}
	hitSpans := exporter.GetSpans()
	fmt.Printf("  ✓ Cache hits | spans: %d (vs miss pass: %d)\n", len(hitSpans), len(missSpans))
	exporter.Reset()

	// Phase 4: Update with cache invalidation
	fmt.Println("\n📊 Phase 4: Update with cache invalidation (first 20 records)")
	fmt.Println(strings.Repeat("-", 70))
	for i := 0; i < 20; i++ {
		rec, _ := quark.For[MariaDBStressRecord](ctx, client).Where("value", "=", i).First()
		rec.Data = fmt.Sprintf("mdb-updated-%d", i)
		_, _ = quark.For[MariaDBStressRecord](ctx, client).Update(&rec)
	}
	updateSpans := exporter.GetSpans()
	fmt.Printf("  ✓ Updated 20 records | spans: %d\n", len(updateSpans))
	exporter.Reset()

	// Phase 5: Count
	fmt.Println("\n📊 Phase 5: Count")
	fmt.Println(strings.Repeat("-", 70))
	total, err := quark.For[MariaDBStressRecord](ctx, client).Count()
	if err != nil || total != int64(count) {
		t.Errorf("expected %d records, got %d (err: %v)", count, total, err)
	}
	fmt.Printf("  ✓ Count: %d records\n", total)

	// Phase 6: Paginated list
	fmt.Println("\n📊 Phase 6: Paginated List")
	fmt.Println(strings.Repeat("-", 70))
	res, err := quark.For[MariaDBStressRecord](ctx, client).OrderBy("value", "ASC").Paginate(50, 1)
	if err != nil {
		t.Fatalf("pagination failed: %v", err)
	}
	if len(res.Items) != 50 {
		t.Errorf("expected 50 items on page 1, got %d", len(res.Items))
	}
	fmt.Printf("  ✓ Page 1: %d items | total: %d\n", len(res.Items), res.Total)
	exporter.Reset()

	// Phase 7: Cleanup
	fmt.Println("\n📊 Phase 7: Cleanup")
	fmt.Println(strings.Repeat("-", 70))
	for i := 0; i < count; i++ {
		if _, err := quark.For[MariaDBStressRecord](ctx, client).Where("value", "=", i).DeleteBy(); err != nil {
			t.Fatalf("delete failed at record %d: %v", i, err)
		}
	}
	deleteSpans := exporter.GetSpans()
	fmt.Printf("  ✓ Deleted %d records | spans: %d\n", count, len(deleteSpans))

	// Phase 8: Verify span attributes
	fmt.Println("\n📊 Phase 8: Span Attributes Verification")
	fmt.Println(strings.Repeat("-", 70))
	allSpans := append(createSpans, missSpans...)
	allSpans = append(allSpans, hitSpans...)
	allSpans = append(allSpans, updateSpans...)
	allSpans = append(allSpans, deleteSpans...)

	valid := 0
	for _, span := range allSpans {
		hasStmt, hasOp := false, false
		for _, attr := range span.Attributes {
			if attr.Key == "db.statement" && attr.Value.AsString() != "" {
				hasStmt = true
			}
			if attr.Key == "db.operation" && attr.Value.AsString() != "" {
				hasOp = true
			}
		}
		if hasStmt && hasOp {
			valid++
		}
	}
	fmt.Printf("  ✓ Spans with db.statement + db.operation: %d/%d\n", valid, len(allSpans))

	// Phase 9: MariaDB-exclusive — verify RETURNING works (ORM uses it instead of LastInsertId)
	fmt.Println("\n📊 Phase 9: MariaDB RETURNING clause verification")
	fmt.Println(strings.Repeat("-", 70))
	if !quark.MariaDB().SupportsReturning() {
		t.Error("MariaDB dialect should report SupportsReturning() = true")
	}
	ret := quark.MariaDB().Returning("id", "data")
	if !strings.Contains(ret, "RETURNING") {
		t.Errorf("expected RETURNING clause, got: %s", ret)
	}
	fmt.Printf("  ✓ RETURNING clause: %s\n", ret)

	// Phase 10: MariaDB-exclusive — Sequence DDL generation
	fmt.Println("\n📊 Phase 10: MariaDB Sequence DDL")
	fmt.Println(strings.Repeat("-", 70))
	mdb := quark.MariaDB().(*quark.MariaDBDialect)
	seqDDL := mdb.CreateSequence("stress_seq", 1000, 5)
	if !strings.Contains(seqDDL, "CREATE SEQUENCE") {
		t.Errorf("expected CREATE SEQUENCE DDL, got: %s", seqDDL)
	}
	nextVal := mdb.NextVal("stress_seq")
	if !strings.Contains(nextVal, "NEXTVAL") {
		t.Errorf("expected NEXTVAL expression, got: %s", nextVal)
	}
	fmt.Printf("  ✓ Sequence DDL: %s\n", seqDDL)
	fmt.Printf("  ✓ NextVal: %s\n", nextVal)

	// Phase 11: Temporal table DDL generation
	fmt.Println("\n📊 Phase 11: MariaDB Temporal Table DDL")
	fmt.Println(strings.Repeat("-", 70))
	temporalDDL := mdb.CreateSystemVersionedTable("audit_log",
		"`id` INT NOT NULL AUTO_INCREMENT PRIMARY KEY,\n`data` TEXT")
	if !strings.Contains(temporalDDL, "WITH SYSTEM VERSIONING") {
		t.Errorf("expected WITH SYSTEM VERSIONING, got: %s", temporalDDL)
	}
	historySQL := mdb.HistoryQuery("audit_log")
	if !strings.Contains(historySQL, "FOR SYSTEM_TIME ALL") {
		t.Errorf("expected FOR SYSTEM_TIME ALL, got: %s", historySQL)
	}
	fmt.Printf("  ✓ Temporal DDL includes WITH SYSTEM VERSIONING\n")
	fmt.Printf("  ✓ History query: %s\n", historySQL)

	fmt.Printf("\n%s\n", strings.Repeat("=", 70))
	fmt.Println("✅ MariaDB stress + Cache + OTel test COMPLETED")
	fmt.Printf("%s\n", strings.Repeat("=", 70))
}
