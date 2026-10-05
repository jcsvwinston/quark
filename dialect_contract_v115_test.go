// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

// This file is a program written against the v1.15.2 API, and it must keep
// compiling UNCHANGED: it names the dialect contract only the way v1.15.2
// spelled it — quark.Dialect, quark.LockOptions, quark.RegisterDialect, the
// optional interfaces and the schema model, all through package quark — and
// imports nothing newer. ADR-0026 moved every one of those types to
// quarkdriver and left the names here as aliases of the same type; if a name
// were dropped, or became a copy instead of an alias, this file would stop
// compiling, or the test would fail where the copy is handed back to Quark.
//
// Do not add quarkdriver names here: the identity checks that need them live
// in dialect_contract_alias_test.go. The file was compiled, as is, in a
// module that requires github.com/jcsvwinston/quark v1.15.2 (A11 Q3).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"

	_ "modernc.org/sqlite"
)

// v115Dialect is a dialect as a third party wrote one against v1.15.2: it
// wraps a built-in, answers its own name and lock policy, and implements
// every optional interface Quark asserts on a dialect.
type v115Dialect struct{ quark.Dialect }

func (v115Dialect) Name() string { return "v115compat" }

func (v115Dialect) LockSuffix(opts quark.LockOptions) (string, string, error) {
	if opts.IsZero() {
		return "", "", nil
	}
	if opts.Mode == quark.LockForShare {
		return "", "", fmt.Errorf("%w: v115compat has no shared locks", quark.ErrUnsupportedFeature)
	}
	return "", "", fmt.Errorf("%w: v115compat has no row locks", quark.ErrUnsupportedFeature)
}

// SavepointDialect: SQLite's ANSI statements, spelled out.
func (v115Dialect) SavepointStmt(name string) string { return "SAVEPOINT " + name }
func (v115Dialect) RollbackToSavepointStmt(name string) string {
	return "ROLLBACK TO SAVEPOINT " + name
}
func (v115Dialect) ReleaseSavepointStmt(name string) string { return "RELEASE SAVEPOINT " + name }

// ColumnTypeMapper: SQLite accepts every type as written.
func (v115Dialect) MapColumnType(t string) string { return t }

// IntrospectSchema forwards to SQLite's introspector and rebuilds the answer
// with every type of the schema model, named through package quark.
func (d v115Dialect) IntrospectSchema(ctx context.Context, exec quark.Executor) (quark.Schema, error) {
	in, err := d.Dialect.(quark.SchemaIntrospector).IntrospectSchema(ctx, exec)
	if err != nil {
		return quark.Schema{}, err
	}
	var out quark.Schema
	for _, t := range in.Tables {
		table := quark.Table{Name: t.Name}
		for _, c := range t.Columns {
			table.Columns = append(table.Columns, quark.Column{
				Name: c.Name, Type: c.Type, Nullable: c.Nullable, Default: c.Default, PrimaryKey: c.PrimaryKey,
			})
		}
		for _, i := range t.Indexes {
			table.Indexes = append(table.Indexes, quark.Index{Name: i.Name, Columns: i.Columns, Unique: i.Unique})
		}
		for _, fk := range t.ForeignKeys {
			table.ForeignKeys = append(table.ForeignKeys, quark.ForeignKey{
				Name: fk.Name, Columns: fk.Columns, RefTable: fk.RefTable, RefColumns: fk.RefColumns,
				OnDelete: fk.OnDelete, OnUpdate: fk.OnUpdate,
			})
		}
		for _, c := range t.Checks {
			table.Checks = append(table.Checks, quark.Check{Name: c.Name, Expression: c.Expression})
		}
		out.Tables = append(out.Tables, table)
	}
	return out, nil
}

// AcquireMigrationLock holds a dedicated connection, as the built-in locks
// do; a zero timeout cannot wait at all and reports ErrLockTimeout.
func (v115Dialect) AcquireMigrationLock(ctx context.Context, db quark.DBConnector, name string, timeout time.Duration) (quark.MigrationLock, error) {
	if timeout <= 0 {
		return nil, fmt.Errorf("%w: %s", quark.ErrLockTimeout, name)
	}
	var conn quark.DBConn
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, err
	}
	var res quark.Result
	if res, err = conn.ExecContext(ctx, "CREATE TEMP TABLE IF NOT EXISTS v115_lock (name TEXT)"); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if _, err := res.RowsAffected(); err != nil {
		_ = conn.Close()
		return nil, err
	}
	var row quark.Row = conn.QueryRowContext(ctx, "SELECT ?", name)
	var held string
	if err := row.Scan(&held); err != nil || held != name {
		_ = conn.Close()
		return nil, fmt.Errorf("lock %q: read back %q: %v", name, held, err)
	}
	return &v115Lock{conn: conn}, nil
}

type v115Lock struct{ conn quark.DBConn }

func (l *v115Lock) Release(context.Context) error {
	if l.conn == nil {
		return nil
	}
	err := l.conn.Close()
	l.conn = nil
	return err
}

var (
	_ quark.Dialect            = v115Dialect{}
	_ quark.SavepointDialect   = v115Dialect{}
	_ quark.ColumnTypeMapper   = v115Dialect{}
	_ quark.SchemaIntrospector = v115Dialect{}
	_ quark.MigrationLocker    = v115Dialect{}
	_ quark.MigrationLock      = (*v115Lock)(nil)

	_ = []quark.LockMode{quark.LockNone, quark.LockForUpdate, quark.LockForShare}
)

type v115Note struct {
	ID    int64  `db:"id" pk:"true"`
	Title string `db:"title"`
}

func (v115Note) TableName() string { return "v115_notes" }

// TestDialectContractV115 registers the dialect by the v1.15.2 names,
// resolves it by driver name, and drives every part of the contract it
// implements through a client.
func TestDialectContractV115(t *testing.T) {
	ctx := context.Background()
	quark.RegisterDialect("v115compat", v115Dialect{quark.SQLite()})
	for _, detect := range []func(string) (quark.Dialect, error){quark.DetectDialect, quark.DetectDialectByName} {
		d, err := detect("v115compat")
		if err != nil || d.Name() != "v115compat" {
			t.Fatalf("the registered dialect is not found by its name: %v, %v", d, err)
		}
	}

	db, err := sql.Open("sqlite", "file:v115compat?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	d, _ := quark.DetectDialect("v115compat")
	c, err := quark.NewWithDB("sqlite", db, quark.WithDialect(d))
	if err != nil {
		t.Fatalf("NewWithDB: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if _, err := c.Raw().ExecContext(ctx,
		`CREATE TABLE v115_notes (id INTEGER PRIMARY KEY AUTOINCREMENT, title TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}

	n := v115Note{Title: "first"}
	if err := quark.For[v115Note](ctx, c).Create(&n); err != nil || n.ID == 0 {
		t.Fatalf("create through the dialect: id=%d err=%v", n.ID, err)
	}

	if opts := (quark.LockOptions{Mode: quark.LockForUpdate, SkipLocked: true}); opts.IsZero() {
		t.Fatal("LockOptions{ForUpdate, SkipLocked}.IsZero() is true")
	}
	if _, err := quark.For[v115Note](ctx, c).ForShare().Limit(1).List(); !errors.Is(err, quark.ErrUnsupportedFeature) {
		t.Fatalf("the dialect's LockSuffix refusal did not reach the caller as ErrUnsupportedFeature: %v", err)
	}

	err = c.Tx(ctx, func(tx *quark.Tx) error {
		if err := tx.Savepoint("sp"); err != nil {
			return err
		}
		if err := quark.ForTx[v115Note](ctx, tx).Create(&v115Note{Title: "undone"}); err != nil {
			return err
		}
		return tx.RollbackTo("sp")
	})
	if err != nil {
		t.Fatalf("savepoint through the dialect's statements: %v", err)
	}
	if count, _ := quark.For[v115Note](ctx, c).Count(); count != 1 {
		t.Fatalf("the rollback to the savepoint kept %d rows, want 1", count)
	}

	schema, err := c.IntrospectSchema(ctx)
	if err != nil {
		t.Fatalf("IntrospectSchema through the dialect: %v", err)
	}
	var notes *quark.Table
	for i := range schema.Tables {
		if schema.Tables[i].Name == "v115_notes" {
			notes = &schema.Tables[i]
		}
	}
	if notes == nil || len(notes.Columns) != 2 || !notes.Columns[0].PrimaryKey {
		t.Fatalf("introspected schema: %+v", schema)
	}

	lock, err := c.AcquireMigrationLock(ctx, "v115", time.Second)
	if err != nil {
		t.Fatalf("AcquireMigrationLock through the dialect: %v", err)
	}
	if err := lock.Release(ctx); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := c.AcquireMigrationLock(ctx, "v115", 0); !errors.Is(err, quark.ErrLockTimeout) {
		t.Fatalf("a lock that cannot wait is not ErrLockTimeout: %v", err)
	}
}
