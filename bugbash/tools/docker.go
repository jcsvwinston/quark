// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package tools holds engine-agnostic plumbing shared by the bug-bash
// phases: container boot/teardown, DSN resolution, readiness polling. It
// carries zero domain logic and does not import bugbash/domain.
//
// Containers are started with `docker run` (not testcontainers) to match
// the proven CI path — testcontainers' reaper makes the Oracle image exit
// within seconds on hosted runners (ADR-0018 / PR #127). A phase that
// already has a database can short-circuit the boot by exporting
// BUGBASH_DSN_<ENGINE> (e.g. BUGBASH_DSN_POSTGRES); Up then skips Docker
// for that engine entirely.
package tools

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Engine identifiers. These are the bug-bash engine names, distinct from
// the database/sql driver names (see EngineConn.Driver).
const (
	SQLite   = "sqlite"
	Postgres = "postgres"
	MySQL    = "mysql"
	MariaDB  = "mariadb"
	MSSQL    = "mssql"
	Oracle   = "oracle"
)

// AllEngines is the canonical six-engine set in boot order.
var AllEngines = []string{SQLite, Postgres, MySQL, MariaDB, MSSQL, Oracle}

// EngineConn is what a phase needs to open a client: the database/sql
// driver name and a ready-to-use DSN.
type EngineConn struct {
	Driver string
	DSN    string
}

// engineSpec describes how to boot one containerized engine and how to
// build its DSN. SQLite has no spec (it is file-based, no container).
type engineSpec struct {
	driver        string
	container     string
	image         string
	hostPort      string
	containerPort string
	env           []string
	dsn           func(hostPort string) string
	// readyTimeout bounds how long Up waits for the engine to accept a
	// ping. Oracle/MSSQL boot far slower than the others.
	readyTimeout time.Duration
}

var specs = map[string]engineSpec{
	Postgres: {
		driver: "pgx", container: "bugbash-postgres",
		image: "postgres:16-alpine", hostPort: "5432", containerPort: "5432",
		env:          []string{"POSTGRES_PASSWORD=quark"},
		dsn:          func(p string) string { return "postgres://postgres:quark@localhost:" + p + "/postgres?sslmode=disable" },
		readyTimeout: 60 * time.Second,
	},
	MySQL: {
		driver: "mysql", container: "bugbash-mysql",
		image: "mysql:8", hostPort: "3306", containerPort: "3306",
		env: []string{"MYSQL_ROOT_PASSWORD=quark"},
		dsn: func(p string) string {
			return "root:quark@tcp(localhost:" + p + ")/mysql?parseTime=true&multiStatements=true"
		},
		readyTimeout: 120 * time.Second,
	},
	MariaDB: {
		driver: "mysql", container: "bugbash-mariadb",
		image: "mariadb:11", hostPort: "3307", containerPort: "3306",
		env: []string{"MARIADB_ROOT_PASSWORD=quark"},
		dsn: func(p string) string {
			return "root:quark@tcp(localhost:" + p + ")/mysql?parseTime=true&multiStatements=true"
		},
		readyTimeout: 120 * time.Second,
	},
	MSSQL: {
		driver: "sqlserver", container: "bugbash-mssql",
		image: "mcr.microsoft.com/mssql/server:2022-latest", hostPort: "1433", containerPort: "1433",
		env:          []string{"ACCEPT_EULA=Y", "MSSQL_SA_PASSWORD=Quark!2026"},
		dsn:          func(p string) string { return "sqlserver://sa:Quark!2026@localhost:" + p + "?database=master" },
		readyTimeout: 180 * time.Second,
	},
	Oracle: {
		driver: "oracle", container: "bugbash-oracle",
		image: "gvenzl/oracle-free:23-slim", hostPort: "1521", containerPort: "1521",
		env:          []string{"ORACLE_PASSWORD=quark"},
		dsn:          func(p string) string { return "oracle://system:quark@localhost:" + p + "/FREEPDB1" },
		readyTimeout: 300 * time.Second,
	},
}

// Up brings up the requested engines and returns a connection descriptor
// per engine. SQLite resolves to a temp file with no container. For every
// other engine, an explicit BUGBASH_DSN_<ENGINE> env var wins over Docker;
// otherwise the container is started (or reused if already running) and Up
// blocks until the engine answers a ping.
//
// Up is best-effort idempotent: a container already running under the
// expected name is reused, not recreated.
func Up(ctx context.Context, engines []string) (map[string]EngineConn, error) {
	out := make(map[string]EngineConn, len(engines))
	for _, eng := range engines {
		if eng == SQLite {
			out[eng] = EngineConn{Driver: "sqlite", DSN: sqliteDSN()}
			continue
		}
		spec, ok := specs[eng]
		if !ok {
			return out, fmt.Errorf("unknown engine %q", eng)
		}
		if dsn := os.Getenv(envVar(eng)); dsn != "" {
			out[eng] = EngineConn{Driver: spec.driver, DSN: dsn}
			continue
		}
		if !dockerAvailable() {
			return out, fmt.Errorf("engine %q needs Docker but the daemon is not reachable (or set %s)", eng, envVar(eng))
		}
		if err := ensureContainer(spec); err != nil {
			return out, fmt.Errorf("boot %s: %w", eng, err)
		}
		dsn := spec.dsn(spec.hostPort)
		if err := waitReady(ctx, spec.driver, dsn, spec.readyTimeout); err != nil {
			return out, fmt.Errorf("engine %q never became ready: %w", eng, err)
		}
		if eng == Oracle {
			grantOracleLock(spec.container)
		}
		out[eng] = EngineConn{Driver: spec.driver, DSN: dsn}
	}
	return out, nil
}

// Down removes the containers for the requested engines (SQLite is a no-op
// here; its temp file is cleaned by the caller). Errors are ignored —
// teardown is best-effort.
func Down(engines ...string) {
	for _, eng := range engines {
		spec, ok := specs[eng]
		if !ok {
			continue
		}
		_ = exec.Command("docker", "rm", "-f", spec.container).Run()
	}
}

func envVar(engine string) string { return "BUGBASH_DSN_" + strings.ToUpper(engine) }

func sqliteDSN() string {
	return filepath.Join(os.TempDir(), fmt.Sprintf("bugbash-f0-%d.db", time.Now().UnixNano()))
}

func dockerAvailable() bool {
	return exec.Command("docker", "info").Run() == nil
}

func containerRunning(name string) bool {
	out, err := exec.Command("docker", "ps", "--filter", "name=^/"+name+"$", "--format", "{{.Names}}").Output()
	return err == nil && strings.TrimSpace(string(out)) == name
}

// ensureContainer reuses a running container of the expected name, or
// `docker run`s a fresh one. A stopped container of the same name is
// removed first so the run does not collide.
func ensureContainer(spec engineSpec) error {
	if containerRunning(spec.container) {
		return nil
	}
	_ = exec.Command("docker", "rm", "-f", spec.container).Run() // clear any stopped leftover
	args := []string{"run", "-d", "--name", spec.container, "-p", spec.hostPort + ":" + spec.containerPort}
	for _, e := range spec.env {
		args = append(args, "-e", e)
	}
	args = append(args, spec.image)
	if out, err := exec.Command("docker", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("docker run: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// waitReady polls Open+Ping until the engine answers or the timeout
// elapses. The driver must be registered by a blank import in the test
// binary.
func waitReady(ctx context.Context, driver, dsn string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		db, err := sql.Open(driver, dsn)
		if err == nil {
			db.SetMaxOpenConns(1) // bound the pool while polling readiness
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
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timed out after %s: %w", timeout, lastErr)
}

// grantOracleLock grants EXECUTE ON DBMS_LOCK, which Quark's distributed
// migration lock needs on Oracle (ADR-0018). Best-effort: F0 install does
// not strictly require it, and later phases surface a hard failure if the
// grant did not take. The `-i` flag is mandatory — without stdin attached
// the grant is a silent no-op.
//
// The receiver is hardcoded to `system` because F0 connects as `system`
// (so the grant is technically a redundant self-grant). If a later phase
// switches Oracle to a dedicated app user, derive the receiver from the
// parsed DSN instead of hardcoding it here.
func grantOracleLock(container string) {
	cmd := exec.Command("docker", "exec", "-i", container,
		"sqlplus", "-s", "system/quark@//localhost:1521/FREEPDB1")
	cmd.Stdin = strings.NewReader("GRANT EXECUTE ON DBMS_LOCK TO system;\nEXIT;\n")
	_ = cmd.Run()
}
