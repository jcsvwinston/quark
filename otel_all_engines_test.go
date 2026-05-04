package quark_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
	quarkotel "github.com/jcsvwinston/quark/otel"

	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/microsoft/go-mssqldb"
	_ "github.com/sijms/go-ora/v2"
	_ "modernc.org/sqlite"
)

type OtelTestUser struct {
	ID    int64  `db:"id" pk:"true"`
	Name  string `db:"name"`
	Email string `db:"email"`
}

// TestOtelAllEngines prueba OpenTelemetry con todos los engines disponibles
func TestOtelAllEngines(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping otel engine test in short mode")
	}

	engines := []struct {
		name string
		dsn  string
		drv  string
		dial quark.Dialect
	}{
		{"SQLite", ":memory:", "sqlite", quark.SQLite()},
		{"Postgres", os.Getenv("QUARK_TEST_POSTGRES_DSN"), "pgx", quark.PostgreSQL()},
		{"MySQL", os.Getenv("QUARK_TEST_MYSQL_DSN"), "mysql", quark.MySQL()},
		{"MariaDB", os.Getenv("QUARK_TEST_MARIADB_DSN"), "mysql", quark.MariaDB()},
		{"MSSQL", os.Getenv("QUARK_TEST_MSSQL_DSN"), "sqlserver", quark.MSSQL()},
		{"Oracle", os.Getenv("QUARK_TEST_ORACLE_DSN"), "oracle", quark.Oracle()},
	}

	for _, eng := range engines {
		if eng.dsn == "" && eng.name != "SQLite" {
			continue
		}

		t.Run(eng.name, func(t *testing.T) {
			exporter, shutdown := setupTestTelemetry()
			defer shutdown(context.Background())

			db, err := sql.Open(eng.drv, eng.dsn)
			if err != nil {
				t.Fatalf("failed to open %s: %v", eng.name, err)
			}
			defer db.Close()

			ctx := context.Background()

			fmt.Printf("\n%s\n", strings.Repeat("=", 70))
			fmt.Printf("🔍 OTEL ENGINE: %s\n", eng.name)
			fmt.Printf("%s\n", strings.Repeat("=", 70))

			// Crear cliente con middleware OTel
			client, err := quark.New(db,
				quark.WithDialect(eng.dial),
				quark.WithMiddleware(quarkotel.New()),
			)
			if err != nil {
				t.Fatalf("failed to create client for %s: %v", eng.name, err)
			}

			// Limpiar tabla si existe
			db.Exec("DROP TABLE IF EXISTS otel_test_users")

			// Migrar
			if err := client.Migrate(ctx, &OtelTestUser{}); err != nil {
				t.Fatalf("migrate failed for %s: %v", eng.name, err)
			}

			// Limpiar spans de migración
			exporter.Reset()

			// Test 1: INSERT genera spans
			fmt.Println("\n📊 Test 1: INSERT operation")
			fmt.Println(strings.Repeat("-", 70))
			user := &OtelTestUser{Name: "Test User", Email: "test@example.com"}
			if err := quark.For[OtelTestUser](ctx, client).Create(user); err != nil {
				t.Fatalf("create failed for %s: %v", eng.name, err)
			}

			insertSpans := countSpansByType(exporter.GetSpans())
			fmt.Printf("  ✓ INSERT spans: query=%d, query_row=%d, exec=%d\n",
				insertSpans["quark.query"], insertSpans["quark.query_row"], insertSpans["quark.exec"])
			if insertSpans["quark.query"]+insertSpans["quark.query_row"]+insertSpans["quark.exec"] == 0 {
				t.Errorf("%s: expected spans for INSERT, got none", eng.name)
			}

			// Limpiar para siguiente test
			exporter.Reset()

			// Test 2: SELECT genera spans
			fmt.Println("\n📊 Test 2: SELECT operation")
			fmt.Println(strings.Repeat("-", 70))
			users, err := quark.For[OtelTestUser](ctx, client).List()
			if err != nil {
				t.Fatalf("list failed for %s: %v", eng.name, err)
			}
			if len(users) != 1 {
				t.Errorf("%s: expected 1 user, got %d", eng.name, len(users))
			}

			selectSpans := countSpansByType(exporter.GetSpans())
			fmt.Printf("  ✓ SELECT spans: query=%d, query_row=%d\n",
				selectSpans["quark.query"], selectSpans["quark.query_row"])
			if selectSpans["quark.query"]+selectSpans["quark.query_row"] == 0 {
				t.Errorf("%s: expected spans for SELECT, got none", eng.name)
			}

			// Limpiar para siguiente test
			exporter.Reset()

			// Test 3: First() genera spans
			fmt.Println("\n📊 Test 3: First() operation")
			fmt.Println(strings.Repeat("-", 70))
			found, err := quark.For[OtelTestUser](ctx, client).First()
			if err != nil {
				t.Fatalf("first failed for %s: %v", eng.name, err)
			}
			if found.Name != "Test User" {
				t.Errorf("%s: expected 'Test User', got %s", eng.name, found.Name)
			}

			firstSpans := countSpansByType(exporter.GetSpans())
			fmt.Printf("  ✓ First() spans: query=%d, query_row=%d\n",
				firstSpans["quark.query"], firstSpans["quark.query_row"])
			if firstSpans["quark.query"]+firstSpans["quark.query_row"] == 0 {
				t.Errorf("%s: expected spans for First(), got none", eng.name)
			}

			// Limpiar para siguiente test
			exporter.Reset()

			// Test 4: UPDATE genera spans
			fmt.Println("\n📊 Test 4: UPDATE operation")
			fmt.Println(strings.Repeat("-", 70))
			found.Name = "Updated User"
			_, err = quark.For[OtelTestUser](ctx, client).Update(&found)
			if err != nil {
				t.Fatalf("update failed for %s: %v", eng.name, err)
			}

			updateSpans := countSpansByType(exporter.GetSpans())
			fmt.Printf("  ✓ UPDATE spans: query=%d, query_row=%d, exec=%d\n",
				updateSpans["quark.query"], updateSpans["quark.query_row"], updateSpans["quark.exec"])
			if updateSpans["quark.query"]+updateSpans["quark.query_row"]+updateSpans["quark.exec"] == 0 {
				t.Errorf("%s: expected spans for UPDATE, got none", eng.name)
			}

			// Limpiar para siguiente test
			exporter.Reset()

			// Test 5: Verificar atributos de trazas
			fmt.Println("\n📊 Test 5: Span attributes verification")
			fmt.Println(strings.Repeat("-", 70))
			_, _ = quark.For[OtelTestUser](ctx, client).List()

			spans := exporter.GetSpans()
			hasValidAttributes := false
			for _, span := range spans {
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
					hasValidAttributes = true
					fmt.Printf("  ✓ Span '%s' has db.statement and db.operation\n", span.Name)
				}
			}

			if !hasValidAttributes {
				t.Errorf("%s: expected spans with db.statement and db.operation attributes", eng.name)
			}

			fmt.Printf("\n✅ %s: All OTel tests passed\n", eng.name)
			fmt.Println(strings.Repeat("=", 70))
		})
	}
}

func countSpansByType(spans []tracetest.SpanStub) map[string]int {
	counts := map[string]int{
		"quark.query":     0,
		"quark.query_row": 0,
		"quark.exec":      0,
	}
	for _, span := range spans {
		if count, ok := counts[span.Name]; ok {
			counts[span.Name] = count + 1
		}
	}
	return counts
}
