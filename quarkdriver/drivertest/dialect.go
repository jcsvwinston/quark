// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package drivertest

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/quarkdriver"
)

// DialectCase describes a dialect and the live engine the kit checks it
// against.
//
// The engine is the driver's own: its test opens the database it can reach,
// with the DSN it knows, and hands the kit the pool. The kit never starts a
// server and never imports a driver, so running it adds nothing to a driver
// module's go.mod.
type DialectCase struct {
	// Dialect is the dialect under test: the value the driver module
	// registers with quarkdriver.RegisterDialect, or a built-in one such as
	// quark.PostgreSQL().
	Dialect quarkdriver.Dialect

	// DB is a pool on a live engine of the dialect's kind. The kit creates,
	// alters and drops tables whose names start with qk_kit_, takes and
	// releases a migration lock named qk_kit, and on an engine without
	// transactional DDL leaves the rows ApplyPlan records in Quark's own
	// checkpoint table (quark_migration_state). Give it a database it may
	// write to. The kit does not close DB.
	//
	// nil runs the checks that need no engine and skips the others, with
	// the reason in the test log.
	DB *sql.DB

	// DriverName is the database/sql driver name DB was opened with. When it
	// is set the kit also checks that Quark resolves this dialect from it —
	// quark.NewWithDB(DriverName, DB) with no quark.WithDialect — which is
	// what an application gets from quark.New(DriverName, dsn). Leave it
	// empty for a dialect an application has to pass with quark.WithDialect.
	DriverName string

	// Routines names a function and a procedure the test created on the
	// engine, so the kit can run Dialect.BuildRoutineQuery and
	// Dialect.BuildProcedureCall. Nil skips those two checks: the kit
	// cannot create a routine on an engine whose DDL it does not know.
	Routines *Routines
}

// Routines are the engine-side objects the kit needs to check the two
// routine methods of the Dialect contract. The driver's test creates them
// before it calls VerifyDialect and drops them after.
type Routines struct {
	// Function is the routine Dialect.BuildRoutineQuery calls: it takes one
	// integer argument n and returns n rows of one integer column named
	// value, holding 1 to n. A table-valued function where the dialect
	// selects from it; a procedure that returns a result set where the
	// dialect writes CALL (MySQL, MariaDB). The kit runs it with
	// quark.NewRoutine.
	Function string

	// Procedure is a procedure that takes one integer argument and returns
	// no result set. The kit runs it with quark.Call, which writes
	// Dialect.BuildProcedureCall, and checks that the engine accepts the
	// call with its argument bound.
	Procedure string
}

// VerifyDialect checks a dialect against the engine it writes SQL for.
//
// It runs in two groups of subtests, each named after the member of the
// contract it checks, so a failure names the method to fix:
//
//   - contract: what the dialect promises without an engine — placeholders
//     that agree with each other, a zero lock that writes nothing, a JSON
//     path that cannot carry SQL, an IsAlreadyExists that does not claim a
//     nil error.
//   - engine: every method of quarkdriver.Dialect and every optional
//     interface the dialect implements (SavepointDialect, ColumnTypeMapper,
//     MigrationLocker, SchemaIntrospector, ColumnTyper, AutoIncrementer,
//     IdempotentDDL, ColumnAlterer, ObjectDropper, ReferentialActioner,
//     TableRebuilder), driven through Quark — Create, Find, Where, Upsert,
//     the row locks, the savepoints of a transaction, Migrate,
//     PlanMigration, ApplyPlan, AddForeignKey, Sync and IntrospectSchema —
//     and judged by what the engine then holds or refuses, never by the
//     text of a statement. An optional interface the dialect does not
//     implement is reported in the log with the default Quark uses instead,
//     and that default is what the check exercises.
//
// A driver module calls it from its own test:
//
//	func TestDialectConformance(t *testing.T) {
//	    dsn := os.Getenv("EXTSQL_TEST_DSN")
//	    if dsn == "" {
//	        t.Skip("EXTSQL_TEST_DSN not set")
//	    }
//	    db, err := sql.Open("extsql", dsn)
//	    if err != nil {
//	        t.Fatal(err)
//	    }
//	    defer db.Close()
//	    drivertest.VerifyDialect(t, drivertest.DialectCase{
//	        Dialect:    extsql.Dialect(),
//	        DB:         db,
//	        DriverName: "extsql",
//	    })
//	}
func VerifyDialect(t *testing.T, c DialectCase) {
	t.Helper()
	if c.Dialect == nil {
		t.Fatal("drivertest: DialectCase.Dialect is required")
	}

	t.Run("contract", func(t *testing.T) {
		for _, chk := range contractChecks {
			t.Run(chk.name, func(t *testing.T) { chk.run(t, c.Dialect) })
		}
	})

	t.Run("engine", func(t *testing.T) {
		if c.DB == nil {
			t.Skip("DialectCase.DB is nil: the engine checks need a live database of the dialect's kind, opened by the driver's test")
		}
		k := newKit(t, c)
		for _, chk := range engineChecks {
			t.Run(chk.name, func(t *testing.T) { chk.run(t, k) })
		}
	})
}

type contractCheck struct {
	name string
	run  func(t *testing.T, d quarkdriver.Dialect)
}

type engineCheck struct {
	name string
	run  func(t *testing.T, k *kit)
}

// kit is what every engine check shares: the case, a client built on the
// case's pool with the dialect under test, and helpers that speak SQL in
// that dialect.
type kit struct {
	c      DialectCase
	d      quarkdriver.Dialect
	db     *sql.DB
	client *quark.Client
	ctx    context.Context
	// run makes the names of what a run creates outside the kit's own
	// tables — a lock, a savepoint — unique, so two runs against one
	// database do not see each other.
	run string
}

// quiet keeps Quark's own log out of the test output: the kit reports what
// it found, and a WARN about a missing classifier is not a dialect finding.
var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

func newKit(t *testing.T, c DialectCase) *kit {
	t.Helper()
	// The driver name only feeds auto-detection, which WithDialect
	// overrides: the client speaks the dialect under test whatever name
	// the pool was opened with.
	driverName := c.DriverName
	if driverName == "" {
		driverName = "drivertest"
	}
	client, err := quark.NewWithDB(driverName, c.DB, quark.WithDialect(c.Dialect), quark.WithLogger(quiet))
	if err != nil {
		t.Fatalf("quark.NewWithDB with the dialect under test: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() }) // a borrowed pool is not closed
	var b [3]byte
	_, _ = rand.Read(b[:])
	return &kit{c: c, d: c.Dialect, db: c.DB, client: client, ctx: context.Background(), run: hex.EncodeToString(b[:])}
}

// q quotes an identifier in the dialect under test.
func (k *kit) q(ident string) string { return k.d.Quote(ident) }

// exec runs a statement on the pool.
func (k *kit) exec(query string, args ...any) error {
	_, err := k.db.ExecContext(k.ctx, query, args...)
	return err
}

// drop drops tables, children first, ignoring "does not exist": the kit
// drops before it creates, so a run that died half-way does not poison the
// next one.
func (k *kit) drop(tables ...string) {
	for _, table := range tables {
		_ = k.exec("DROP TABLE " + k.q(table))
	}
}

// fresh drops the tables of models (children first, as given) and migrates
// them again, and drops them when the test ends. A failure here is reported
// against Migrate and the interfaces it asks.
func (k *kit) fresh(t *testing.T, models ...any) {
	t.Helper()
	names := make([]string, len(models))
	for i, m := range models {
		names[i] = tableOf(m)
	}
	k.drop(names...)
	t.Cleanup(func() { k.drop(names...) })
	// Parents first for Migrate: a child may name its parent.
	for i := len(models) - 1; i >= 0; i-- {
		if err := k.client.Migrate(k.ctx, models[i]); err != nil {
			t.Fatalf("setup: Migrate(%s) failed — Migrate writes the table through quarkdriver.ColumnTyper, AutoIncrementer and IdempotentDDL (or their portable defaults): %v", names[i], err)
		}
	}
}

// tableOf is the table a model names through TableName().
func tableOf(model any) string {
	if n, ok := model.(interface{ TableName() string }); ok {
		return n.TableName()
	}
	panic(fmt.Sprintf("drivertest: kit model %T has no TableName", model))
}

// count returns the rows of table, or the engine's error.
func (k *kit) count(table string) (int64, error) {
	var n int64
	err := k.db.QueryRowContext(k.ctx, "SELECT COUNT(*) FROM "+k.q(table)).Scan(&n)
	return n, err
}

// tableExists reports whether the engine can read table.
func (k *kit) tableExists(table string) bool {
	_, err := k.count(table)
	return err == nil
}

// columnExists reports whether the engine can read column of table. The
// column is qualified by its table: SQLite reads a double-quoted name that
// matches no column as a string literal, and a qualified name cannot be one.
func (k *kit) columnExists(table, column string) bool {
	rows, err := k.db.QueryContext(k.ctx, "SELECT "+k.q(table)+"."+k.q(column)+" FROM "+k.q(table))
	if err != nil {
		return false
	}
	_ = rows.Close()
	return true
}

// columnType spells a portable kind in the dialect under test: its
// ColumnTyper's answer, or the portable type Quark writes without one.
func (k *kit) columnType(spec quarkdriver.ColumnSpec) string {
	if ct, ok := k.d.(quarkdriver.ColumnTyper); ok {
		if s := ct.ColumnType(spec); s != "" {
			return s
		}
	}
	switch spec.Kind {
	case quarkdriver.KindString:
		if spec.Size > 0 {
			return fmt.Sprintf("VARCHAR(%d)", spec.Size)
		}
		return "VARCHAR(255)"
	case quarkdriver.KindInt32:
		return "INTEGER"
	case quarkdriver.KindInt64:
		return "BIGINT"
	}
	return "TEXT"
}

// clearCheckpoint removes what ApplyPlan's resumable path recorded for plan.
// On an engine without transactional DDL ApplyPlan skips every op it once
// recorded under the plan's hash, so a second run of the kit against the
// same database — after the kit dropped and recreated its tables — would
// find its plans "already applied" and change nothing. The table and its
// columns are Quark's (migrate_state.go); the error of a missing table is
// ignored.
func (k *kit) clearCheckpoint(plan quark.Plan) {
	if k.d.SupportsTransactionalDDL() || plan.IsEmpty() {
		return
	}
	_ = k.exec("DELETE FROM "+k.q("quark_migration_state")+" WHERE plan_hash = "+k.d.Placeholder(1), plan.Hash())
}

// apply clears the plan's checkpoint and applies it.
func (k *kit) apply(plan quark.Plan) error {
	k.clearCheckpoint(plan)
	return k.client.ApplyPlan(k.ctx, plan)
}

// planFor is PlanMigration for models, keeping only the operations on the
// kit's own tables: the database the driver hands the kit may hold others,
// and a plan from a subset of the models proposes dropping them.
func (k *kit) planFor(t *testing.T, models ...any) quark.Plan {
	t.Helper()
	plan, err := k.client.PlanMigration(k.ctx, models...)
	if err != nil {
		t.Fatalf("PlanMigration: %v", err)
	}
	keep := map[string]bool{}
	for _, m := range models {
		keep[strings.ToLower(tableOf(m))] = true
	}
	var ops []quark.Operation
	for _, op := range plan.Ops {
		if keep[strings.ToLower(opTable(op))] {
			ops = append(ops, op)
		}
	}
	return quark.Plan{Ops: ops}
}

// opTable is the table an operation is about.
func opTable(op quark.Operation) string {
	switch o := op.(type) {
	case quark.OpCreateTable:
		return o.Table.Name
	case quark.OpDropTable:
		return o.Table
	case quark.OpAddColumn:
		return o.Table
	case quark.OpDropColumn:
		return o.Table
	case quark.OpAlterColumn:
		return o.Table
	case quark.OpCreateIndex:
		return o.Table
	case quark.OpDropIndex:
		return o.Table
	case quark.OpAddForeignKey:
		return o.Table
	case quark.OpDropForeignKey:
		return o.Table
	case quark.OpAddCheck:
		return o.Table
	case quark.OpDropCheck:
		return o.Table
	}
	return ""
}

// introspected reads table from the dialect's SchemaIntrospector, by a
// case-insensitive name: an engine that folds unquoted names reports them
// folded.
func (k *kit) introspected(t *testing.T, table string) (quark.Table, bool) {
	t.Helper()
	schema, err := k.client.IntrospectSchema(k.ctx)
	if err != nil {
		t.Fatalf("SchemaIntrospector.IntrospectSchema: %v", err)
	}
	for _, tb := range schema.Tables {
		if strings.EqualFold(tb.Name, table) {
			return tb, true
		}
	}
	return quark.Table{}, false
}

// column finds a column of an introspected table by a case-insensitive name.
func column(tb quark.Table, name string) (quark.Column, bool) {
	for _, c := range tb.Columns {
		if strings.EqualFold(c.Name, name) {
			return c, true
		}
	}
	return quark.Column{}, false
}

// introspects reports whether the dialect reads its schema; the checks that
// diff against the live schema need it, and say so when they skip.
func (k *kit) introspects() bool {
	_, ok := k.d.(quarkdriver.SchemaIntrospector)
	return ok
}

func (k *kit) needIntrospector(t *testing.T) {
	t.Helper()
	if !k.introspects() {
		t.Skip("the dialect does not implement quarkdriver.SchemaIntrospector, so PlanMigration, Sync and IntrospectSchema return ErrUnsupportedFeature (checked under SchemaIntrospector) and this check has nothing to diff against")
	}
}

// within bounds an engine call the kit expects to return promptly.
func (k *kit) within(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(k.ctx, d)
}

// isUnsupported reports the contract's "this engine cannot" answer.
func isUnsupported(err error) bool { return errors.Is(err, quarkdriver.ErrUnsupportedFeature) }
