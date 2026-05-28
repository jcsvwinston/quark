// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

//go:build bugbash
// +build bugbash

// Package f00_install is bug-bash phase F0: install & boot. It verifies a
// fresh consumer can open Quark on each engine and migrate the whole
// domain. The `bugbash` build tag keeps it out of the library's default
// `go test ./...`.
package f00_install

import (
	"context"
	"flag"
	"os"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/bugbash/domain"
	"github.com/jcsvwinston/quark/bugbash/tools"

	// database/sql drivers for every engine F0 can target. Blank-imported
	// here so tools.waitReady and quark.New can resolve the driver name.
	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/microsoft/go-mssqldb"
	_ "github.com/sijms/go-ora/v2"
	_ "modernc.org/sqlite"
)

var engineFlag = flag.String("engines", "sqlite",
	"comma-separated engines (sqlite,postgres,mysql,mariadb,mssql,oracle) or 'all'")

func selectedEngines() []string {
	v := strings.TrimSpace(*engineFlag)
	if v == "" || v == "all" {
		return tools.AllEngines
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// TestInstallAndMigrate is F0's core: per selected engine, open a client,
// ping, and migrate the entire bug-bash domain. A failure on any engine is
// reported without aborting the others (subtests), per the bug-bash rule
// that the value is in the aggregate, not the first failure.
func TestInstallAndMigrate(t *testing.T) {
	engines := selectedEngines()
	ctx := context.Background()

	conns, err := tools.Up(ctx, engines)
	if err != nil {
		t.Fatalf("bring up engines %v: %v", engines, err)
	}
	t.Cleanup(func() {
		var containerEngines []string
		for _, e := range engines {
			if e != tools.SQLite {
				containerEngines = append(containerEngines, e)
			}
		}
		tools.Down(containerEngines...)
	})

	for _, eng := range engines {
		conn := conns[eng]
		t.Run(eng, func(t *testing.T) {
			client, err := quark.New(conn.Driver, conn.DSN)
			if err != nil {
				t.Errorf("quark.New(%q): %v", conn.Driver, err)
				return
			}
			t.Cleanup(func() {
				_ = client.Close()
				if eng == tools.SQLite {
					_ = os.Remove(conn.DSN)
				}
			})

			if err := client.Raw().PingContext(ctx); err != nil {
				t.Errorf("ping %s: %v", eng, err)
				return
			}

			if err := client.Migrate(ctx, domain.AllModels()...); err != nil {
				t.Errorf("migrate whole domain on %s: %v", eng, err)
			}
		})
	}
}
