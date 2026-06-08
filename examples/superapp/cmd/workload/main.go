// Command workload ejerce el dominio de la superapp a alto volumen y emite un
// informe ejecutivo, métricas y el log de la aplicación, para contrastar cómo
// corre Quark con datos relacionados, consultas, transacciones y caché.
//
//	go run ./examples/superapp/cmd/workload                 # SQLite, escala ×3
//	go run ./examples/superapp/cmd/workload -scale=10       # más volumen
//	go run ./examples/superapp/cmd/workload -driver=pgx -dsn="$QUARK_TEST_POSTGRES_DSN"
//
// Artefactos en -out (por defecto ./examples/superapp/REPORTS/workload-<stamp>/):
// executive-report.md, metrics.json y quark.log.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/cache/memory"
	"github.com/jcsvwinston/quark/examples/superapp/control"
	"github.com/jcsvwinston/quark/examples/superapp/domain"
	"github.com/jcsvwinston/quark/examples/superapp/recorder"
	"github.com/jcsvwinston/quark/examples/superapp/workload"

	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

func main() {
	var (
		scale   = flag.Int("scale", 3, "multiplicador de volumen (>=1)")
		driver  = flag.String("driver", "sqlite", "driver SQL (sqlite|pgx|mysql|...)")
		dsn     = flag.String("dsn", "", "DSN; vacío en sqlite usa un fichero en -out")
		outFlag = flag.String("out", "", "directorio de salida; vacío usa REPORTS/workload-<stamp>")
		slowMs  = flag.Int("slow-ms", 2, "umbral de slow-query log en ms (0 desactiva)")
	)
	flag.Parse()

	stamp := time.Now().Format("20060102-150405")
	outDir := *outFlag
	if outDir == "" {
		outDir = filepath.Join("examples", "superapp", "REPORTS", "workload-"+stamp)
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fail("creando -out: %v", err)
	}

	// Log de la aplicación a fichero (slog JSON): fases + slow-query (WARN).
	logPath := filepath.Join(outDir, "quark.log")
	logFile, err := os.Create(logPath)
	if err != nil {
		fail("creando log: %v", err)
	}
	defer logFile.Close()
	logger := slog.New(slog.NewJSONHandler(logFile, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// DSN sqlite por defecto: fichero en el out dir.
	dataSource := *dsn
	if dataSource == "" {
		if *driver != "sqlite" {
			fail("driver %q requiere -dsn", *driver)
		}
		dataSource = filepath.Join(outDir, "app.db")
	}

	rec := recorder.New(engineLabel(*driver))
	cache := memory.New()
	defer cache.Close()

	limits := quark.DefaultLimits()
	limits.SafeMigrations = false
	limits.MaxResults = 1_000_000 // el volumen alto supera el cap por defecto

	opts := append(rec.Options(),
		quark.WithLogger(logger),
		quark.WithCacheStore(cache),
		quark.WithLimits(limits),
	)
	if *slowMs > 0 {
		opts = append(opts, quark.WithSlowQueryThreshold(time.Duration(*slowMs)*time.Millisecond))
	}

	client, err := quark.New(*driver, dataSource, opts...)
	if err != nil {
		fail("conectando (%s): %v", *driver, err)
	}
	defer client.Close()

	ctx := context.Background()
	if err := client.Migrate(ctx, domain.AllModels()...); err != nil {
		fail("migrate: %v", err)
	}

	fmt.Printf("▶ corriendo workload: driver=%s scale=×%d out=%s\n", *driver, *scale, outDir)
	res, err := workload.Run(ctx, workload.Config{Engine: string(engineLabel(*driver)), Scale: *scale}, client, rec, logger)
	if err != nil {
		fail("workload: %v", err)
	}

	art, err := res.WriteArtifacts(outDir, logPath)
	if err != nil {
		fail("escribiendo artefactos: %v", err)
	}

	// Resumen en consola.
	fmt.Printf("\n✓ %s filas · %s statements · %d tx · %d errores · %s (cache hit %.1f%%)\n",
		grp(res.TotalRows), grp(int64(res.TotalStmts)), res.Transactions, res.Errors,
		res.TotalDuration.Round(time.Millisecond), res.CacheHitRate()*100)
	fmt.Printf("  informe : %s\n", art.ReportPath)
	fmt.Printf("  métricas: %s\n", art.MetricsPath)
	fmt.Printf("  log     : %s\n", art.LogPath)
}

func engineLabel(driver string) control.Engine {
	switch driver {
	case "pgx", "postgres", "postgresql":
		return control.Postgres
	case "mysql":
		return control.MySQL
	case "sqlserver", "mssql":
		return control.MSSQL
	case "oracle", "godror":
		return control.Oracle
	default:
		return control.SQLite
	}
}

func grp(n int64) string {
	s := fmt.Sprintf("%d", n)
	out := ""
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out += ","
		}
		out += string(c)
	}
	return out
}

func fail(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "workload: "+format+"\n", a...)
	os.Exit(1)
}
