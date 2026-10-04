// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jcsvwinston/quark/quarkdriver"
)

// The schema path asks the dialect (A11 Q2). These tests pin both halves of
// that contract on statements a recording driver captures: a dialect that
// answers the quarkdriver schema questions gets ITS answers in the DDL, and a
// dialect that answers none gets the documented portable DDL — never a
// spelling chosen by its name.

// --- a recording driver -----------------------------------------------------

const schemaRecDriver = "quark-schema-rec"

var (
	schemaRecOnce sync.Once
	schemaRecMu   sync.Mutex
	schemaRecLog  []string
	// schemaRecFail, when set, is the error every Exec whose statement
	// contains schemaRecFailOn returns.
	schemaRecFail   error
	schemaRecFailOn string
)

type schemaRecDrv struct{}
type schemaRecConn struct{}
type schemaRecTx struct{}
type schemaRecRows struct{}
type schemaRecResult struct{}

func (schemaRecDrv) Open(string) (driver.Conn, error) { return schemaRecConn{}, nil }
func (schemaRecConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepare unsupported")
}
func (schemaRecConn) Close() error                   { return nil }
func (schemaRecConn) Begin() (driver.Tx, error)      { return schemaRecTx{}, nil }
func (schemaRecTx) Commit() error                    { return nil }
func (schemaRecTx) Rollback() error                  { return nil }
func (schemaRecResult) LastInsertId() (int64, error) { return 0, nil }
func (schemaRecResult) RowsAffected() (int64, error) { return 0, nil }
func (schemaRecRows) Columns() []string              { return []string{"c"} }
func (schemaRecRows) Close() error                   { return nil }
func (schemaRecRows) Next([]driver.Value) error      { return io.EOF }

func (schemaRecConn) ExecContext(_ context.Context, q string, _ []driver.NamedValue) (driver.Result, error) {
	schemaRecMu.Lock()
	defer schemaRecMu.Unlock()
	schemaRecLog = append(schemaRecLog, strings.Join(strings.Fields(q), " "))
	if schemaRecFail != nil && strings.Contains(q, schemaRecFailOn) {
		return nil, schemaRecFail
	}
	return schemaRecResult{}, nil
}

func (schemaRecConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return schemaRecRows{}, nil
}

// recordSchema runs f on a client of dialect d over the recording driver and
// returns the statements it executed.
func recordSchema(t *testing.T, d Dialect, f func(ctx context.Context, c *Client) error) []string {
	t.Helper()
	schemaRecOnce.Do(func() { sql.Register(schemaRecDriver, schemaRecDrv{}) })
	db, err := sql.Open(schemaRecDriver, "")
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewWithDB(schemaRecDriver, db, WithDialect(d))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(); _ = db.Close() })
	schemaRecMu.Lock()
	schemaRecLog = nil
	schemaRecMu.Unlock()
	if err := f(context.Background(), c); err != nil {
		t.Fatalf("%s: %v", d.Name(), err)
	}
	schemaRecMu.Lock()
	defer schemaRecMu.Unlock()
	return append([]string(nil), schemaRecLog...)
}

// --- two dialects under names Quark does not know --------------------------

// bareDialect is SQLite's Dialect methods under another name and NONE of the
// optional interfaces: embedding the interface promotes its methods only.
type bareDialect struct{ Dialect }

func (bareDialect) Name() string { return "bare" }

// answeringDialect answers every schema question with a marked string, so a
// test can see that the answer — not a table keyed on a name — reached the
// DDL.
type answeringDialect struct{ Dialect }

func (answeringDialect) Name() string { return "answering" }

func (answeringDialect) ColumnType(s quarkdriver.ColumnSpec) string {
	if s.Kind == quarkdriver.KindFloat32 {
		return "" // take the portable type
	}
	if s.Size > 0 {
		return "T_" + s.Kind.String() + "_" + strconv.Itoa(s.Size)
	}
	return "T_" + s.Kind.String()
}

func (answeringDialect) BoolLiteral(v bool) string {
	if v {
		return "YES"
	}
	return "NO"
}

func (answeringDialect) AutoIncrementColumn() (string, string) {
	return "KEYDEF PRIMARY KEY", "KEYTYPE"
}

func (d answeringDialect) CreateTableIfNotExists(table, body string) string {
	return "MAKE TABLE " + d.Quote(table) + " (" + body + ")"
}

func (d answeringDialect) CreateIndexIfNotExists(table, index string, columns []string, unique bool) string {
	return "MAKE INDEX " + d.Quote(index) + " ON " + d.Quote(table) + " " + strings.Join(columns, ",")
}

var errAnsweringExists = errors.New("answering: object exists")

func (answeringDialect) IsAlreadyExists(object quarkdriver.SchemaObject, err error) bool {
	return errors.Is(err, errAnsweringExists)
}

type schemaAskRow struct {
	ID      int64     `db:"id" pk:"true"`
	Name    string    `db:"name,size=40" quark:"unique"`
	Tag     string    `db:"tag" quark:"index"`
	Active  bool      `db:"active" default:"true"`
	Ratio   float32   `db:"ratio"`
	Price   float64   `db:"price,precision=10,scale=2"`
	Created time.Time `db:"created"`
}

func (schemaAskRow) TableName() string { return "ask_rows" }

func TestSchemaPathAsksTheDialect(t *testing.T) {
	got := recordSchema(t, answeringDialect{SQLite()}, func(ctx context.Context, c *Client) error {
		return c.Migrate(ctx, &schemaAskRow{})
	})
	want := []string{
		`MAKE TABLE "ask_rows" ("id" KEYDEF PRIMARY KEY, "name" T_string_40 UNIQUE, "tag" T_string, "active" T_bool DEFAULT YES, "ratio" REAL, "price" T_decimal, "created" T_time)`,
		`MAKE INDEX "idx_ask_rows_tag" ON "ask_rows" tag`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("Migrate did not write the dialect's answers.\n got:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}

	// PlanMigration's desired schema: the dialect's types, and the key's
	// catalog type from AutoIncrementColumn.
	c := clientOf(t, answeringDialect{SQLite()})
	s, err := c.modelsToSchema(&schemaAskRow{})
	if err != nil {
		t.Fatal(err)
	}
	types := map[string]string{}
	for _, col := range s.Tables[0].Columns {
		types[col.Name] = col.Type
		if col.Name == "active" && (col.Default == nil || *col.Default != "YES") {
			t.Errorf("the boolean default is not the dialect's literal: %v", col.Default)
		}
	}
	for col, typ := range map[string]string{"id": "KEYTYPE", "name": "T_string_40", "ratio": "REAL", "created": "T_time"} {
		if types[col] != typ {
			t.Errorf("modelsToSchema: column %s is %q, want %q", col, types[col], typ)
		}
	}
}

func TestSchemaPathAlreadyExistsIsTheDialectsCall(t *testing.T) {
	schemaRecFail, schemaRecFailOn = errAnsweringExists, "MAKE TABLE"
	t.Cleanup(func() { schemaRecFail, schemaRecFailOn = nil, "" })
	got := recordSchema(t, answeringDialect{SQLite()}, func(ctx context.Context, c *Client) error {
		// The CREATE fails with the error IsAlreadyExists recognises:
		// Migrate goes on to the index.
		return c.Migrate(ctx, &schemaAskRow{})
	})
	if len(got) != 2 || !strings.HasPrefix(got[1], "MAKE INDEX") {
		t.Errorf("an error the dialect calls \"already exists\" stopped Migrate: %q", got)
	}
}

func TestSchemaPathPortableDefaults(t *testing.T) {
	got := recordSchema(t, bareDialect{SQLite()}, func(ctx context.Context, c *Client) error {
		if err := c.Migrate(ctx, &schemaAskRow{}); err != nil {
			return err
		}
		if err := c.ensureMigrationStateTable(ctx, c.db); err != nil {
			return err
		}
		return c.ensureBackfillStateTable(ctx)
	})
	want := []string{
		`CREATE TABLE IF NOT EXISTS "ask_rows" ( "id" BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY, "name" VARCHAR(40) UNIQUE, "tag" VARCHAR(255), "active" BOOLEAN DEFAULT TRUE, "ratio" REAL, "price" DECIMAL(10,2), "created" TIMESTAMP )`,
		`CREATE INDEX IF NOT EXISTS "idx_ask_rows_tag" ON "ask_rows" ("tag")`,
		`CREATE TABLE IF NOT EXISTS "quark_migration_state" ( plan_hash CHAR(64) NOT NULL, op_index INTEGER NOT NULL, op_string TEXT NOT NULL, applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL, PRIMARY KEY (plan_hash, op_index) )`,
		`CREATE TABLE IF NOT EXISTS "quark_backfill_state" ( name VARCHAR(255) NOT NULL PRIMARY KEY, last_pk BIGINT NOT NULL, updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP NOT NULL )`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("a dialect that answers nothing did not get the portable DDL.\n got:\n  %s\nwant:\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}

	c := clientOf(t, bareDialect{SQLite()})
	s, err := c.modelsToSchema(&schemaAskRow{})
	if err != nil {
		t.Fatal(err)
	}
	if typ := s.Tables[0].Columns[0].Type; typ != "BIGINT" {
		t.Errorf("the portable key's catalog type is %q, want BIGINT", typ)
	}
}

// TestPortableKeyFailsLoudly is the reason the portable key is the SQL
// standard's identity column: on an engine that does not accept it — SQLite,
// here, under a name Quark does not know and with none of the schema
// interfaces — Migrate fails. Before A11 Q2 it wrote BIGINT PRIMARY KEY,
// which SQLite accepts, and the first insert left the key NULL.
func TestPortableKeyFailsLoudly(t *testing.T) {
	c, err := New("sqlite", "file:portable_key?mode=memory&cache=shared", WithDialect(bareDialect{SQLite()}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	err = c.Migrate(context.Background(), &schemaAskRow{})
	if err == nil || !strings.Contains(err.Error(), "syntax error") {
		t.Fatalf("Migrate should fail on the identity clause SQLite does not accept, got %v", err)
	}
}

func clientOf(t *testing.T, d Dialect) *Client {
	t.Helper()
	schemaRecOnce.Do(func() { sql.Register(schemaRecDriver, schemaRecDrv{}) })
	db, err := sql.Open(schemaRecDriver, "")
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewWithDB(schemaRecDriver, db, WithDialect(d))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(); _ = db.Close() })
	return c
}

// TestBuiltinsAnswerTheSchemaQuestions pins which optional interfaces each
// built-in implements: a wrapper that renames one has to forward exactly
// these (internal/extbench's extlite does, for SQLite).
func TestBuiltinsAnswerTheSchemaQuestions(t *testing.T) {
	for _, tc := range []struct {
		d          Dialect
		idempotent bool
	}{
		{PostgreSQL(), false}, {MySQL(), true}, {MariaDB(), true},
		{SQLite(), false}, {MSSQL(), true}, {Oracle(), true},
	} {
		if _, ok := tc.d.(quarkdriver.ColumnTyper); !ok {
			t.Errorf("%s does not implement ColumnTyper", tc.d.Name())
		}
		if _, ok := tc.d.(quarkdriver.AutoIncrementer); !ok {
			t.Errorf("%s does not implement AutoIncrementer", tc.d.Name())
		}
		if _, ok := tc.d.(quarkdriver.IdempotentDDL); ok != tc.idempotent {
			t.Errorf("%s implements IdempotentDDL: %v, want %v", tc.d.Name(), ok, tc.idempotent)
		}
	}
}
