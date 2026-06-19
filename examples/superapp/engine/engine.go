// Package engine arranca y teardownea los 6 motores para el arnés del superapp,
// y verifica que no haya fugas (goroutines, conexiones del pool) tras cerrar.
//
// Espeja el patrón PROBADO de bugbash/tools/docker.go: contenedores por
// `docker run` (NO testcontainers — su reaper tumba la imagen de Oracle en
// runners hosted, ADR-0018/PR #127), SQLite in-process, y override por env
// `SUPERAPP_DSN_<ENGINE>` que cortocircuita Docker (para reusar un contenedor ya
// levantado: el `quark-oracle` persistente, el postgres de Lantia, etc.). No
// importa el módulo bugbash (es otro módulo `-tags=bugbash`), así que replica.
//
// Contenedores namespaced `superapp-<engine>` en puertos propios para no
// colisionar con los de bugbash ni con el postgres de Lantia (5432).
package engine

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jcsvwinston/quark/examples/superapp/control"
)

// Conn es lo que hace falta para abrir un Client: el driver de database/sql y un
// DSN listo. El driver lo registra un blank-import del binario consumidor.
type Conn struct {
	Engine control.Engine
	Driver string
	DSN    string
}

type spec struct {
	driver        string
	container     string
	image         string
	hostPort      string
	containerPort string
	env           []string
	dsn           func(hostPort string) string
	// serverDSN, si no es nil, es el DSN de nivel-servidor contra el que se hace
	// el ping de readiness y se crea la base dedicada (ensureDBSQL). Para los
	// motores que NO deben operar sobre su base de sistema (MySQL/MariaDB → `mysql`,
	// MSSQL → `master`): el migrator del arnés introspecciona y borra dentro de la
	// base conectada, y sobre la base de sistema eso intentaría tocar tablas del
	// catálogo. Si es nil, el ping va contra dsn (PG sobre `postgres` está bien).
	serverDSN    func(hostPort string) string
	ensureDBSQL  string
	readyTimeout time.Duration
}

// appDB es la base de datos dedicada del arnés en los motores cuyo DSN por defecto
// caería en una base de sistema. Aislada del catálogo → el migrator/cleanup de los
// exercisers nunca toca tablas de sistema. Las bases por-tenant (DBPerTenant) y los
// schemas (SchemaPerTenant) usan sufijos propios (`superapp_dbt_*`/`superapp_spt_*`)
// y no colisionan con este nombre.
const appDB = "superapp"

// specs: imágenes idénticas a las de la CI/bugbash; puertos host PROPIOS del
// superapp para coexistir con bugbash y con el postgres de Lantia (5432).
var specs = map[control.Engine]spec{
	control.Postgres: {
		driver: "pgx", container: "superapp-postgres",
		image: "postgres:16-alpine", hostPort: "5435", containerPort: "5432",
		env:          []string{"POSTGRES_PASSWORD=quark"},
		dsn:          func(p string) string { return "postgres://postgres:quark@localhost:" + p + "/postgres?sslmode=disable" },
		readyTimeout: 60 * time.Second,
	},
	control.MySQL: {
		driver: "mysql", container: "superapp-mysql",
		image: "mysql:8", hostPort: "3310", containerPort: "3306",
		env: []string{"MYSQL_ROOT_PASSWORD=quark"},
		dsn: func(p string) string {
			return "root:quark@tcp(localhost:" + p + ")/" + appDB + "?parseTime=true&multiStatements=true"
		},
		serverDSN:    func(p string) string { return "root:quark@tcp(localhost:" + p + ")/" },
		ensureDBSQL:  "CREATE DATABASE IF NOT EXISTS " + appDB,
		readyTimeout: 120 * time.Second,
	},
	control.MariaDB: {
		driver: "mysql", container: "superapp-mariadb",
		image: "mariadb:11", hostPort: "3311", containerPort: "3306",
		env: []string{"MARIADB_ROOT_PASSWORD=quark"},
		dsn: func(p string) string {
			return "root:quark@tcp(localhost:" + p + ")/" + appDB + "?parseTime=true&multiStatements=true"
		},
		serverDSN:    func(p string) string { return "root:quark@tcp(localhost:" + p + ")/" },
		ensureDBSQL:  "CREATE DATABASE IF NOT EXISTS " + appDB,
		readyTimeout: 120 * time.Second,
	},
	control.MSSQL: {
		driver: "sqlserver", container: "superapp-mssql",
		image: "mcr.microsoft.com/mssql/server:2022-latest", hostPort: "1435", containerPort: "1433",
		env:          []string{"ACCEPT_EULA=Y", "MSSQL_SA_PASSWORD=Quark!2026"},
		dsn:          func(p string) string { return "sqlserver://sa:Quark!2026@localhost:" + p + "?database=" + appDB },
		serverDSN:    func(p string) string { return "sqlserver://sa:Quark!2026@localhost:" + p + "?database=master" },
		ensureDBSQL:  "IF DB_ID('" + appDB + "') IS NULL CREATE DATABASE " + appDB,
		readyTimeout: 180 * time.Second,
	},
	control.Oracle: {
		driver: "oracle", container: "superapp-oracle",
		image: "gvenzl/oracle-free:23-slim", hostPort: "1523", containerPort: "1521",
		// Connect as a non-privileged APP_USER (gvenzl provisions it), NOT system:
		// as system, schema introspection sees Oracle internal tables (aq$_schedules,
		// …) and the MIGRATE converge tries to DROP them → ErrInvalidIdentifier. The
		// app user only sees its own schema. Mirrors the `integration` CI job.
		env:          []string{"ORACLE_PASSWORD=quark", "APP_USER=quark", "APP_USER_PASSWORD=quark"},
		dsn:          func(p string) string { return "oracle://quark:quark@localhost:" + p + "/FREEPDB1" },
		readyTimeout: 300 * time.Second,
	},
}

// EnvVar es el override de DSN por motor: SUPERAPP_DSN_POSTGRES, etc.
func EnvVar(e control.Engine) string { return "SUPERAPP_DSN_" + strings.ToUpper(string(e)) }

// Up levanta los motores pedidos y devuelve un Conn por motor. SQLite resuelve a
// un fichero temporal sin contenedor. Para el resto, un SUPERAPP_DSN_<ENGINE>
// explícito gana a Docker; si no, se arranca (o reusa) el contenedor y se espera
// a que responda un ping. Idempotente: reusa un contenedor ya corriendo.
func Up(ctx context.Context, engines ...control.Engine) (map[control.Engine]Conn, error) {
	out := make(map[control.Engine]Conn, len(engines))
	for _, e := range engines {
		if e == control.SQLite {
			out[e] = Conn{Engine: e, Driver: "sqlite", DSN: sqliteDSN()}
			continue
		}
		sp, ok := specs[e]
		if !ok {
			return out, fmt.Errorf("motor desconocido %q", e)
		}
		if dsn := os.Getenv(EnvVar(e)); dsn != "" {
			// Override: el caller gestiona el contenedor. Para MySQL/MariaDB/MSSQL
			// el DSN debe apuntar ya a una base dedicada existente (no `mysql`/
			// `master`): se salta ensureDBSQL igual que se salta el boot.
			out[e] = Conn{Engine: e, Driver: sp.driver, DSN: dsn}
			continue
		}
		if !dockerAvailable() {
			return out, fmt.Errorf("el motor %q necesita Docker (o exporta %s)", e, EnvVar(e))
		}
		if err := ensureContainer(sp); err != nil {
			return out, fmt.Errorf("boot %s: %w", e, err)
		}
		dsn := sp.dsn(sp.hostPort)
		readyDSN := dsn
		if sp.serverDSN != nil {
			readyDSN = sp.serverDSN(sp.hostPort)
		}
		if err := waitReady(ctx, sp.driver, readyDSN, sp.readyTimeout); err != nil {
			return out, fmt.Errorf("el motor %q nunca estuvo listo: %w", e, err)
		}
		if sp.ensureDBSQL != "" {
			if err := ensureDatabase(ctx, sp.driver, readyDSN, sp.ensureDBSQL); err != nil {
				return out, fmt.Errorf("crear base dedicada de %s: %w", e, err)
			}
		}
		if e == control.Oracle {
			grantOracleLock(sp.container)
		}
		out[e] = Conn{Engine: e, Driver: sp.driver, DSN: dsn}
	}
	return out, nil
}

// Down elimina los contenedores de los motores pedidos (SQLite es no-op; su
// fichero temporal lo limpia el caller). Best-effort. NO toca contenedores
// resueltos por SUPERAPP_DSN_<ENGINE> (no los gestionamos nosotros).
func Down(engines ...control.Engine) {
	for _, e := range engines {
		if os.Getenv(EnvVar(e)) != "" {
			continue
		}
		if sp, ok := specs[e]; ok {
			_ = exec.Command("docker", "rm", "-f", sp.container).Run()
		}
	}
}

func sqliteDSN() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("superapp-%d.db", time.Now().UnixNano()))
}

func dockerAvailable() bool { return exec.Command("docker", "info").Run() == nil }

func containerRunning(name string) bool {
	out, err := exec.Command("docker", "ps", "--filter", "name=^/"+name+"$", "--format", "{{.Names}}").Output()
	return err == nil && strings.TrimSpace(string(out)) == name
}

func ensureContainer(sp spec) error {
	if containerRunning(sp.container) {
		return nil
	}
	_ = exec.Command("docker", "rm", "-f", sp.container).Run() // limpia un parado del mismo nombre
	args := []string{"run", "-d", "--name", sp.container, "-p", sp.hostPort + ":" + sp.containerPort}
	for _, e := range sp.env {
		args = append(args, "-e", e)
	}
	args = append(args, sp.image)
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("docker run: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// waitReady sondea Open+Ping hasta que el motor responde o expira el timeout. El
// driver debe estar registrado por un blank-import en el binario de test.
func waitReady(ctx context.Context, driver, dsn string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		db, err := sql.Open(driver, dsn)
		if err == nil {
			db.SetMaxOpenConns(1)
			pingCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
			lastErr = db.PingContext(pingCtx)
			cancel()
			_ = db.Close()
			if lastErr == nil {
				return nil
			}
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
	return fmt.Errorf("timeout tras %s: %w", timeout, lastErr)
}

// ensureDatabase crea la base dedicada del arnés ejecutando ensureDBSQL contra el
// DSN de nivel-servidor (serverDSN). Idempotente por construcción (el DDL lleva
// IF NOT EXISTS / IF DB_ID(...) IS NULL). Se corre tras el ping de readiness; el
// CREATE DATABASE va por db.ExecContext directo (sin tx: PG/MSSQL lo exigen, y
// aquí no hay Client de Quark todavía).
func ensureDatabase(ctx context.Context, driver, serverDSN, ddl string) error {
	db, err := sql.Open(driver, serverDSN)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err = db.ExecContext(ctx, ddl)
	return err
}

// grantOracleLock concede EXECUTE ON DBMS_LOCK al app-user quark (lo necesita el
// lock de migración distribuido de Quark en Oracle, ADR-0018). DBMS_LOCK lo posee
// SYS, así que el grant a otro usuario va como sysdba (mismo patrón que el job
// `integration`). El `-i` es obligatorio: sin stdin el grant es un no-op
// silencioso. Best-effort (un grant fallido aflora luego como AcquireMigrationLock
// rojo en el gate).
func grantOracleLock(container string) {
	cmd := exec.Command("docker", "exec", "-i", container,
		"sqlplus", "-s", "sys/quark@//localhost:1521/FREEPDB1", "as", "sysdba")
	cmd.Stdin = strings.NewReader("GRANT EXECUTE ON DBMS_LOCK TO quark;\nEXIT;\n")
	_ = cmd.Run()
}
