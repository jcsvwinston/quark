// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package registryrace is a fixture of internal/extbench, not a test suite of
// its own: it lives under testdata so `go test ./...` never runs it. The
// bench runs it explicitly, under the race detector, in a child process —
// because whether THIS process has the detector on depends on which CI lane
// is running it, and a verdict that depends on the lane is not a verdict.
//
// Each test does what a program does at start-up when two packages register
// an extension point from their init or from parallel test setup: writers
// and readers of one global registry, on goroutines with no ordering between
// them. The race detector reports an unsynchronised access whether or not the
// two accesses happened to overlap on this run.
package registryrace

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sync"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/quarkdriver"
)

const writers = 8

// hammer runs write(i) and read(i) for i in [0, writers) on 2*writers
// goroutines released together.
func hammer(write, read func(i int)) {
	var start, done sync.WaitGroup
	start.Add(1)
	for i := 0; i < writers; i++ {
		done.Add(2)
		go func(i int) { defer done.Done(); start.Wait(); write(i) }(i)
		go func(i int) { defer done.Done(); start.Wait(); read(i) }(i)
	}
	start.Done()
	done.Wait()
}

func TestDialectRegistry(t *testing.T) {
	hammer(
		func(i int) { quark.RegisterDialect(fmt.Sprintf("racedialect%d", i), quark.SQLite()) },
		func(i int) {
			_, _ = quark.DetectDialect(fmt.Sprintf("racedialect%d", i))
			_, _ = quark.DetectDialectByName(fmt.Sprintf("racedialect%d", i))
		},
	)
}

func TestClassifierRegistry(t *testing.T) {
	never := func(error) bool { return false }
	hammer(
		func(i int) {
			_ = quarkdriver.Register(fmt.Sprintf("raceengine%d", i), quarkdriver.Classifier{
				UniqueViolation: never, Deadlock: never, TransientConn: never,
			})
		},
		func(i int) {
			_ = quarkdriver.HasEngine(fmt.Sprintf("raceengine%d", i))
			_ = quark.IsUniqueViolation(errors.New("x"))
		},
	)
}

func TestListenerFactoryRegistry(t *testing.T) {
	factory := func(*sql.DB, quarkdriver.IdentifierValidator) (quarkdriver.Listener, error) { return nil, nil }
	hammer(
		func(i int) { _ = quarkdriver.RegisterListenerFactory(fmt.Sprintf("racelistener%d", i), factory) },
		func(i int) { _, _ = quarkdriver.LookupListenerFactory(fmt.Sprintf("racelistener%d", i)) },
	)
}

// raceRow has one field of a type registered concurrently, so Migrate reads
// the type-mapper registry while the writers fill it.
type raceCents int64

type raceRow struct {
	ID    int64     `db:"id" pk:"true"`
	Price raceCents `db:"price"`
}

func TestTypeMapperRegistry(t *testing.T) {
	sql.Register("racenull", nullDriver{})
	c, err := quark.New("racenull", "", quark.WithDialect(quark.SQLite()))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer c.Close()
	byte0 := reflect.TypeOf(byte(0))
	hammer(
		func(i int) {
			// Distinct types per writer: [1]byte, [2]byte, …
			quark.RegisterTypeMapper(reflect.ArrayOf(i+1, byte0), func(string, quark.TypeOptions) string { return "BLOB" })
			if i == 0 {
				quark.RegisterTypeMapper(reflect.TypeOf(raceCents(0)), func(string, quark.TypeOptions) string { return "INTEGER" })
			}
		},
		func(int) { _ = c.Migrate(context.Background(), &raceRow{}) },
	)
}

type raceModel struct {
	ID int64 `db:"id" pk:"true"`
}

func TestCodegenRegistry(t *testing.T) {
	model := reflect.TypeOf(raceModel{})
	hammer(
		func(int) {
			quark.RegisterTypedScanner(model, quark.StubScanner)
			quark.RegisterTypedBinder(model, quark.StubBinder)
			quark.RegisterGeneratedMeta(model, quark.GeneratedMeta{})
		},
		func(int) {
			_ = quark.GeneratedBinderRegistered(model)
			_, _ = quark.CheckGeneratedDrift(model)
		},
	)
}

// nullDriver accepts every statement and returns nothing: enough for Migrate
// to run its type mapping without linking a database engine, whose own
// instrumented build would multiply this fixture's compile time.
type nullDriver struct{}

func (nullDriver) Open(string) (driver.Conn, error) { return nullConn{}, nil }

type nullConn struct{}

func (nullConn) Prepare(string) (driver.Stmt, error) { return nullStmt{}, nil }
func (nullConn) Close() error                        { return nil }
func (nullConn) Begin() (driver.Tx, error)           { return nullTx{}, nil }

type nullStmt struct{}

func (nullStmt) Close() error                               { return nil }
func (nullStmt) NumInput() int                              { return -1 }
func (nullStmt) Exec([]driver.Value) (driver.Result, error) { return driver.RowsAffected(0), nil }
func (nullStmt) Query([]driver.Value) (driver.Rows, error)  { return nullRows{}, nil }

type nullRows struct{}

func (nullRows) Columns() []string         { return nil }
func (nullRows) Close() error              { return nil }
func (nullRows) Next([]driver.Value) error { return io.EOF }

type nullTx struct{}

func (nullTx) Commit() error   { return nil }
func (nullTx) Rollback() error { return nil }
