package quark_test

import (
	"github.com/jcsvwinston/quark"
	"context"
	"database/sql"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/microsoft/go-mssqldb"
	_ "github.com/sijms/go-ora/v2"
	_ "modernc.org/sqlite"
)

type BenchModel struct {
	ID    int64  `db:"id" pk:"true"`
	Data  string `db:"data"`
	Value int    `db:"value"`
}

type metricsObserver struct {
	mu      sync.Mutex
	results map[string][]time.Duration
}

func (o *metricsObserver) ObserveQuery(e quark.QueryEvent) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.results == nil {
		o.results = make(map[string][]time.Duration)
	}
	o.results[e.Operation] = append(o.results[e.Operation], e.Duration)

	// Visual log for progress every 1000 operations
	count := len(o.results[e.Operation])
	if count%1000 == 0 {
		fmt.Printf("  [VISUAL LOG] %s: processed %d records (last duration: %v)\n", e.Operation, count, e.Duration)
	}
}

func (o *metricsObserver) Summary() {
	fmt.Println("\n--- BENCHMARK METRICS SUMMARY ---")
	for op, durs := range o.results {
		var total time.Duration
		for _, d := range durs {
			total += d
		}
		avg := total / time.Duration(len(durs))
		fmt.Printf("[%s] Total Ops: %d, Avg Time: %v, Total Time: %v\n", op, len(durs), avg, total)
	}
}

func TestBenchmarkEngines(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping benchmark in short mode")
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
		{"MSSQL", os.Getenv("QUARK_TEST_MSSQL_DSN"), "sqlserver", quark.MSSQL()},
		{"Oracle", os.Getenv("QUARK_TEST_ORACLE_DSN"), "oracle", quark.Oracle()},
	}

	for _, eng := range engines {
		if eng.dsn == "" && eng.name != "SQLite" {
			continue
		}

		t.Run(eng.name, func(t *testing.T) {
			db, err := sql.Open(eng.drv, eng.dsn)
			if err != nil {
				t.Fatalf("failed to open %s: %v", eng.name, err)
			}
			defer db.Close()

			obs := &metricsObserver{}
			client, err := quark.New(db, quark.WithDialect(eng.dial), quark.WithQueryObserver(obs))
			if err != nil {
				t.Fatalf("failed to create client for %s: %v", eng.name, err)
			}
			ctx := context.Background()

			client.Raw().Exec("DROP TABLE IF EXISTS bench_models")
			client.Migrate(ctx, &BenchModel{})

			// 1. Bulk Insert (10,000 records)
			fmt.Printf("[%s] Inserting 10,000 records...\n", eng.name)
			for i := 0; i < 10000; i++ {
				m := &BenchModel{Data: fmt.Sprintf("data-%d", i), Value: i}
				quark.For[BenchModel](ctx, client).Create(m)
			}

			// 2. Bulk Select (List)
			fmt.Printf("[%s] Selecting all records (List)...\n", eng.name)
			quark.For[BenchModel](ctx, client).Limit(10000).List()

			// 3. Bulk Select (Iter - Streaming)
			fmt.Printf("[%s] Streaming all records (Iter)...\n", eng.name)
			quark.For[BenchModel](ctx, client).Iter(func(m BenchModel) error {
				return nil
			})

			// 4. Update Half
			fmt.Printf("[%s] Updating 5,000 records...\n", eng.name)
			for i := 0; i < 5000; i++ {
				m := &BenchModel{Data: "updated"}
				quark.For[BenchModel](ctx, client).Where("value", "=", i).Update(m)
			}

			// 5. Delete Half
			fmt.Printf("[%s] Deleting 5,000 records...\n", eng.name)
			for i := 5000; i < 10000; i++ {
				quark.For[BenchModel](ctx, client).Where("value", "=", i).Delete(&BenchModel{})
			}

			obs.Summary()
		})
	}
}
