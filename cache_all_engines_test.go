package quark_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/cache/memory"
	"github.com/jcsvwinston/quark/cache/redis"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/microsoft/go-mssqldb"
	_ "github.com/sijms/go-ora/v2"
	_ "modernc.org/sqlite"
)

// cacheLogger cuenta y loguea cada consulta SQL ejecutada
type cacheLogger struct {
	mu      sync.Mutex
	queries []string
	count   int32
}

func (l *cacheLogger) ObserveQuery(e quark.QueryEvent) {
	atomic.AddInt32(&l.count, 1)
	l.mu.Lock()
	l.queries = append(l.queries, fmt.Sprintf("[SQL EXECUTED] %s | args=%v | duration=%v", e.SQL, e.Args, e.Duration))
	l.mu.Unlock()
}

func (l *cacheLogger) PrintQueries() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, q := range l.queries {
		fmt.Println(q)
	}
}

func (l *cacheLogger) Count() int {
	return int(atomic.LoadInt32(&l.count))
}

func (l *cacheLogger) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.queries = nil
	atomic.StoreInt32(&l.count, 0)
}

type CacheTestUser struct {
	ID    int64  `db:"id" pk:"true"`
	Email string `db:"email"`
	Name  string `db:"name"`
}

// TestCacheAllEngines prueba la caché con todos los engines disponibles
func TestCacheAllEngines(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cache test in short mode")
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
			db, err := sql.Open(eng.drv, eng.dsn)
			if err != nil {
				t.Fatalf("failed to open %s: %v", eng.name, err)
			}
			defer db.Close()

			logger := &cacheLogger{}
			ctx := context.Background()

			fmt.Printf("\n%s\n", strings.Repeat("=", 70))
			fmt.Printf("🔍 ENGINE: %s\n", eng.name)
			fmt.Printf("%s\n", strings.Repeat("=", 70))

			// Limpiar tabla si existe
			db.Exec("DROP TABLE IF EXISTS cache_test_users")

			// Cliente SIN caché
			clientNoCache, _ := quark.New(db, quark.WithDialect(eng.dial), quark.WithQueryObserver(logger))
			clientNoCache.Migrate(ctx, &CacheTestUser{})

			// Insertar datos
			for i := 1; i <= 3; i++ {
				u := &CacheTestUser{Email: fmt.Sprintf("user%d@test.com", i), Name: fmt.Sprintf("User %d", i)}
				quark.For[CacheTestUser](ctx, clientNoCache).Create(u)
			}

			// --- FASE 1: Sin Caché ---
			fmt.Println("\n📊 FASE 1: Consultas SIN caché (baseline)")
			fmt.Println(strings.Repeat("-", 70))
			logger.Reset()

			fmt.Println("→ Primera consulta: quark.For[User](ctx, client).Where(\"id\", \"=\", 1).First()")
			_, _ = quark.For[CacheTestUser](ctx, clientNoCache).Where("id", "=", 1).First()

			fmt.Println("→ Segunda consulta (misma): quark.For[User](ctx, client).Where(\"id\", \"=\", 1).First()")
			_, _ = quark.For[CacheTestUser](ctx, clientNoCache).Where("id", "=", 1).First()

			logger.PrintQueries()
			fmt.Printf("\n✓ Total queries SQL: %d (esperado: 2 - sin caché)\n", logger.Count())

			// --- FASE 2: Con Caché In-Memory ---
			fmt.Println("\n" + strings.Repeat("=", 70))
			fmt.Println("📊 FASE 2: Consultas CON caché In-Memory")
			fmt.Println(strings.Repeat("-", 70))

			memStore := memory.New()
			logger.Reset()

			clientWithCache, _ := quark.New(db,
				quark.WithDialect(eng.dial),
				quark.WithCacheStore(memStore),
				quark.WithQueryObserver(logger),
			)

			// Test 1: Primera consulta (MISS)
			fmt.Println("→ 1ª consulta caché: quark.For[User](ctx, client).Where(\"id\", \"=\", 1).Cache(5m).First()")
			fmt.Println("  [Esperado: MISS - SQL ejecutado y guardado en caché]")
			start := time.Now()
			_, _ = quark.For[CacheTestUser](ctx, clientWithCache).Where("id", "=", 1).Cache(5 * time.Minute).First()
			duration1 := time.Since(start)

			logger.PrintQueries()
			queriesAfterFirst := logger.Count()
			fmt.Printf("✓ Primera consulta: %v (SQL ejecutado: %d)\n", duration1, queriesAfterFirst)

			// Test 2: Segunda consulta (HIT)
			logger.Reset()
			fmt.Println("\n→ 2ª consulta caché: quark.For[User](ctx, client).Where(\"id\", \"=\", 1).Cache(5m).First()")
			fmt.Println("  [Esperado: HIT - Lee de memoria, NO ejecuta SQL]")
			start = time.Now()
			_, _ = quark.For[CacheTestUser](ctx, clientWithCache).Where("id", "=", 1).Cache(5 * time.Minute).First()
			duration2 := time.Since(start)

			queriesAfterSecond := logger.Count()
			fmt.Printf("✓ Segunda consulta: %v (", duration2)
			if queriesAfterSecond == 0 {
				fmt.Printf("SIN SQL - CACHE HIT ✓) [%.1fx más rápido]\n", float64(duration1)/float64(duration2))
			} else {
				fmt.Printf("⚠ SQL ejecutado - CACHE MISS)\n")
			}

			// Test 3: Tercera consulta (HIT)
			logger.Reset()
			fmt.Println("\n→ 3ª consulta caché: quark.For[User](ctx, client).Where(\"id\", \"=\", 1).Cache(5m).First()")
			fmt.Println("  [Esperado: HIT - Lee de memoria, NO ejecuta SQL]")
			start = time.Now()
			_, _ = quark.For[CacheTestUser](ctx, clientWithCache).Where("id", "=", 1).Cache(5 * time.Minute).First()
			duration3 := time.Since(start)

			queriesAfterThird := logger.Count()
			fmt.Printf("✓ Tercera consulta: %v (", duration3)
			if queriesAfterThird == 0 {
				fmt.Printf("SIN SQL - CACHE HIT ✓) [%.1fx más rápido]\n", float64(duration1)/float64(duration3))
			} else {
				fmt.Printf("⚠ SQL ejecutado - CACHE MISS)\n")
			}

			// --- FASE 3: Invalidación de Caché ---
			fmt.Println("\n" + strings.Repeat("=", 70))
			fmt.Println("📊 FASE 3: Invalidación de Caché por Create")
			fmt.Println(strings.Repeat("-", 70))

			logger.Reset()
			fmt.Println("→ Consulta previa a Create (debería usar caché):")
			_, _ = quark.For[CacheTestUser](ctx, clientWithCache).Where("id", "=", 1).Cache(5 * time.Minute).First()
			hitsBefore := logger.Count()
			fmt.Printf("   Queries SQL: %d (esperado: 0 - cache hit)\n", hitsBefore)

			logger.Reset()
			fmt.Println("\n→ Crear nuevo registro (invalida caché de la tabla):")
			newUser := &CacheTestUser{Email: "new@test.com", Name: "New User"}
			quark.For[CacheTestUser](ctx, clientWithCache).Create(newUser)
			fmt.Printf("   Queries SQL de Create: %d\n", logger.Count())

			logger.Reset()
			fmt.Println("\n→ Reconsultar después de Create (caché invalidada):")
			fmt.Println("  [La operación CREATE invalida automáticamente la caché]")
			_, _ = quark.For[CacheTestUser](ctx, clientWithCache).Where("id", "=", 1).Cache(5 * time.Minute).First()
			hitsAfterCreate := logger.Count()
			fmt.Printf("   Queries SQL: %d (esperado: 1 - caché invalidada por Create)\n", hitsAfterCreate)

			// --- FASE 4: Redis (solo si está disponible) ---
			if eng.name == "SQLite" { // Solo probar Redis una vez
				fmt.Println("\n" + strings.Repeat("=", 70))
				fmt.Println("📊 FASE 4: Caché Redis")
				fmt.Println(strings.Repeat("-", 70))

				redisStore := redis.New(redis.Options{Addr: "localhost:6379"})
				if err := redisStore.Ping(ctx); err != nil {
					fmt.Printf("⚠ Redis no disponible (%v), saltando test Redis\n", err)
				} else {
					logger.Reset()
					clientRedis, _ := quark.New(db,
						quark.WithDialect(eng.dial),
						quark.WithCacheStore(redisStore),
						quark.WithQueryObserver(logger),
					)

					fmt.Println("→ 1ª consulta Redis: quark.For[User](ctx, client).Where(\"id\", \"=\", 2).Cache(5m).First()")
					start = time.Now()
					_, _ = quark.For[CacheTestUser](ctx, clientRedis).Where("id", "=", 2).Cache(5 * time.Minute).First()
					durationRedis1 := time.Since(start)

					logger.Reset()
					fmt.Println("→ 2ª consulta Redis: quark.For[User](ctx, client).Where(\"id\", \"=\", 2).Cache(5m).First()")
					start = time.Now()
					_, _ = quark.For[CacheTestUser](ctx, clientRedis).Where("id", "=", 2).Cache(5 * time.Minute).First()
					durationRedis2 := time.Since(start)

					fmt.Printf("✓ Redis 1ª consulta: %v (MISS)\n", durationRedis1)
					if logger.Count() == 0 {
						fmt.Printf("✓ Redis 2ª consulta: %v (HIT - SIN SQL) [%.1fx más rápido]\n", durationRedis2, float64(durationRedis1)/float64(durationRedis2))
					} else {
						fmt.Printf("⚠ Redis 2ª consulta: SQL ejecutado (miss inesperado)\n")
					}
				}
			}

			fmt.Printf("\n%s\n", strings.Repeat("✅", 35))
			fmt.Printf("✅ %s CACHE TEST COMPLETADO\n", strings.ToUpper(eng.name))
			fmt.Printf("%s\n", strings.Repeat("✅", 35))
		})
	}
}
