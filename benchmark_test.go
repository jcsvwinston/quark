package quark_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"

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

// TestBenchmarkEngines: smoke de volumen (10k inserts + lecturas + 5k
// updates/deletes) por engine.
//
// QK6-1: cada pata resuelve su DSN DENTRO del subtest vía resolve<Engine>DSN.
// Con `-tags=integration` el resolver levanta el contenedor solo para la pata
// que `-run` selecciona (así cada lane de CI ejecuta la suya); sin el tag, la
// pata hace Skip explícito con motivo — nunca un `continue` silencioso. La
// pata MariaDB faltaba (único engine sin ella); añadida para que la lane
// mariadb no seleccione un test vacío.
func TestBenchmarkEngines(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping benchmark in short mode")
	}

	engines := []struct {
		name    string
		env     string
		resolve func(*testing.T) string
		drv     string
		dial    quark.Dialect
	}{
		{"SQLite", "", func(*testing.T) string { return ":memory:" }, "sqlite", quark.SQLite()},
		{"Postgres", "QUARK_TEST_POSTGRES_DSN", resolvePostgresDSN, "pgx", quark.PostgreSQL()},
		{"MySQL", "QUARK_TEST_MYSQL_DSN", resolveMySQLDSN, "mysql", quark.MySQL()},
		{"MariaDB", "QUARK_TEST_MARIADB_DSN", resolveMariaDBDSN, "mysql", quark.MariaDB()},
		{"MSSQL", "QUARK_TEST_MSSQL_DSN", resolveMSSQLDSN, "sqlserver", quark.MSSQL()},
		{"Oracle", "QUARK_TEST_ORACLE_DSN", resolveOracleDSN, "oracle", quark.Oracle()},
	}

	for _, eng := range engines {
		t.Run(eng.name, func(t *testing.T) {
			dsn := eng.resolve(t)
			if dsn == "" {
				t.Skipf("%s not set (rebuild with -tags=integration to spin up a container); %s leg skipped", eng.env, eng.name)
			}

			obs := &metricsObserver{}
			client, err := quark.New(eng.drv, dsn, quark.WithQueryObserver(obs))
			if err != nil {
				t.Fatalf("failed to create client for %s: %v", eng.name, err)
			}
			defer client.Close()
			ctx := context.Background()

			client.Exec(ctx, "DROP TABLE IF EXISTS bench_models")
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
