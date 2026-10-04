// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package extbench

// The "full participant" battery: what changes when the SAME engine, through
// the SAME dialect methods, is called something Quark does not know.
//
// extlite is SQLite under another name. Its database/sql driver is modernc's,
// registered a second time; its dialect forwards every Dialect method — and
// every optional dialect interface SQLite's dialect implements, the schema
// questions of quarkdriver included — to quark.SQLite(), and changes exactly
// one thing: Name() answers "extlite".
//
// So any difference between the two arms is behaviour that lives in Quark's
// own code, keyed on a dialect's NAME, instead of behind the Dialect
// interface. That is what an external driver cannot reach: whatever its
// dialect returns, the branch was taken on a string it does not own.
//
// Every step opens a database of its own on each arm, so one step's failure
// cannot cascade into the next. The query steps start from the same table on
// both arms — created by hand with SQLite's own DDL — so they measure the
// query path alone; the schema steps measure what Quark's migration code
// writes.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/quarkdriver"

	moderncsqlite "modernc.org/sqlite"
)

// extliteName is the engine name of the external driver the battery plays.
const extliteName = "extlite"

// extliteDialect is SQLite's dialect under another name.
type extliteDialect struct{ quark.Dialect }

func (extliteDialect) Name() string { return extliteName }

// The methods below forward the optional interfaces SQLite's dialect
// implements (asserted in registerExtlite), so the battery measures the name
// and nothing else. Embedding the Dialect interface promotes its methods and
// no others: a wrapper that renames a dialect has to forward these by hand,
// as a third party's would.

func (d extliteDialect) IntrospectSchema(ctx context.Context, exec quark.Executor) (quark.Schema, error) {
	return d.Dialect.(quark.SchemaIntrospector).IntrospectSchema(ctx, exec)
}

func (d extliteDialect) ColumnType(s quarkdriver.ColumnSpec) string {
	return d.Dialect.(quarkdriver.ColumnTyper).ColumnType(s)
}

func (d extliteDialect) BoolLiteral(v bool) string {
	return d.Dialect.(quarkdriver.ColumnTyper).BoolLiteral(v)
}

func (d extliteDialect) AutoIncrementColumn() (string, string) {
	return d.Dialect.(quarkdriver.AutoIncrementer).AutoIncrementColumn()
}

var extliteOnce sync.Once

// registerExtlite registers the extlite database/sql driver and dialect once
// per process: both registries are global.
func registerExtlite(t *testing.T) {
	t.Helper()
	extliteOnce.Do(func() {
		sql.Register(extliteName, &moderncsqlite.Driver{})
		quark.RegisterDialect(extliteName, extliteDialect{quark.SQLite()})
	})

	// The wrapper must be faithful: the same optional interfaces as the
	// dialect it wraps, or the battery would measure the wrapper.
	inner, wrapped := quark.SQLite(), quark.Dialect(extliteDialect{quark.SQLite()})
	for name, has := range map[string]func(quark.Dialect) bool{
		"SavepointDialect":   func(d quark.Dialect) bool { _, ok := d.(quark.SavepointDialect); return ok },
		"SchemaIntrospector": func(d quark.Dialect) bool { _, ok := d.(quark.SchemaIntrospector); return ok },
		"ColumnTypeMapper":   func(d quark.Dialect) bool { _, ok := d.(quark.ColumnTypeMapper); return ok },
		"MigrationLocker":    func(d quark.Dialect) bool { _, ok := d.(quark.MigrationLocker); return ok },
		"ColumnTyper":        func(d quark.Dialect) bool { _, ok := d.(quarkdriver.ColumnTyper); return ok },
		"AutoIncrementer":    func(d quark.Dialect) bool { _, ok := d.(quarkdriver.AutoIncrementer); return ok },
		"IdempotentDDL":      func(d quark.Dialect) bool { _, ok := d.(quarkdriver.IdempotentDDL); return ok },
	} {
		if has(inner) != has(wrapped) {
			t.Fatalf("the extlite wrapper does not mirror SQLite's dialect on %s (sqlite %v, extlite %v): the battery would measure the wrapper, not the name",
				name, has(inner), has(wrapped))
		}
	}
}

var extliteClassifierOnce sync.Once

// registerExtliteClassifier gives extlite the classifier its driver module
// would register: modernc's extended result codes for a unique or
// primary-key violation. The bench links no driver module, so this is the one
// classifier in the process that recognises a modernc error — on both arms,
// which keeps the comparison about the dialect.
func registerExtliteClassifier(t *testing.T) {
	t.Helper()
	var err error
	extliteClassifierOnce.Do(func() {
		err = quarkdriverRegister(extliteName)
	})
	if err != nil {
		t.Fatalf("register the extlite classifier: %v", err)
	}
}

// partRow is the battery's model: a unique column to collide on, an index,
// and the three types whose SQL type is chosen per dialect name.
type partRow struct {
	ID      int64     `db:"id" pk:"true"`
	Name    string    `db:"name" quark:"unique"`
	Tag     string    `db:"tag" quark:"index"`
	Active  bool      `db:"active"`
	Score   float64   `db:"score"`
	Created time.Time `db:"created"`
}

func (partRow) TableName() string { return "part_rows" }

// partRowV2 is partRow with one more column, for Sync.
type partRowV2 struct {
	ID      int64     `db:"id" pk:"true"`
	Name    string    `db:"name" quark:"unique"`
	Tag     string    `db:"tag" quark:"index"`
	Active  bool      `db:"active"`
	Score   float64   `db:"score"`
	Created time.Time `db:"created"`
	Extra   string    `db:"extra"`
}

func (partRowV2) TableName() string { return "part_rows" }

// partParent is the target of the foreign key the battery adds.
type partParent struct {
	ID int64 `db:"id" pk:"true"`
}

func (partParent) TableName() string { return "part_parents" }

// canonicalDDL is the battery's starting table, in SQLite's own DDL — what
// quark.SQLite() migrates partRow to. Both arms start from it, so the query
// steps compare the query path and nothing else.
var canonicalDDL = []string{
	`CREATE TABLE "part_parents" ("id" INTEGER PRIMARY KEY AUTOINCREMENT)`,
	`CREATE TABLE "part_rows" (
		"id" INTEGER PRIMARY KEY AUTOINCREMENT,
		"name" TEXT UNIQUE,
		"tag" TEXT,
		"active" BOOLEAN,
		"score" REAL,
		"created" DATETIME
	)`,
	`CREATE INDEX "idx_part_rows_tag" ON "part_rows" ("tag")`,
}

var stamp = time.Date(2026, 10, 4, 12, 30, 0, 0, time.UTC)

// canonical creates the starting table and three rows through the public
// API's Create, the same on both arms.
func canonical(ctx context.Context, c *quark.Client) error {
	for _, ddl := range canonicalDDL {
		if _, err := c.Raw().ExecContext(ctx, ddl); err != nil {
			return err
		}
	}
	for i, n := range []string{"alpha", "beta", "50% off"} {
		r := partRow{Name: n, Tag: "t", Active: i%2 == 0, Score: 1.5, Created: stamp}
		if err := quark.For[partRow](ctx, c).Create(&r); err != nil {
			return err
		}
	}
	return nil
}

// partStep is one operation of the battery. It returns what an application
// would observe — ids, counts, the class of an error — rendered as a string,
// so the two arms are compared without the battery deciding in advance which
// answer is right.
type partStep struct {
	name      string
	canonical bool // start from canonicalDDL
	run       func(ctx context.Context, c *quark.Client) string
}

// errClass renders an error as what a caller can act on: nil, a sentinel it
// can test, or "error" — never the message.
func errClass(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, quark.ErrUnsupportedFeature):
		return "ErrUnsupportedFeature"
	case quark.IsUniqueViolation(err):
		return "unique-violation"
	default:
		return "error"
	}
}

func idsOf(rows []partRow, err error) string {
	if err != nil {
		return errClass(err)
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, fmt.Sprint(r.ID))
	}
	return "[" + strings.Join(ids, ",") + "]"
}

func applyOp(ctx context.Context, c *quark.Client, op quark.Operation) string {
	return errClass(c.ApplyPlan(ctx, quark.Plan{Ops: []quark.Operation{op}}))
}

func partBattery() []partStep {
	return []partStep{
		// --- what Quark's migration code writes ---------------------------
		{name: "Migrate: an insert into the migrated table gets its primary key from the engine", run: func(ctx context.Context, c *quark.Client) string {
			if err := c.Migrate(ctx, &partRow{}); err != nil {
				return "migrate: " + errClass(err)
			}
			r := partRow{Name: "first", Created: stamp}
			if err := quark.For[partRow](ctx, c).Create(&r); err != nil {
				return "create: " + errClass(err)
			}
			return fmt.Sprintf("id=%d", r.ID)
		}},
		{name: "Migrate is idempotent (run twice)", run: func(ctx context.Context, c *quark.Client) string {
			if err := c.Migrate(ctx, &partRow{}); err != nil {
				return "first: " + errClass(err)
			}
			return errClass(c.Migrate(ctx, &partRow{}))
		}},
		{name: "PlanMigration against SQLite's own table proposes nothing", canonical: true, run: func(ctx context.Context, c *quark.Client) string {
			plan, err := c.PlanMigration(ctx, &partRow{}, &partParent{})
			if err != nil {
				return errClass(err)
			}
			return fmt.Sprintf("%d ops", len(plan.Ops))
		}},
		{name: "Sync adds a column", canonical: true, run: func(ctx context.Context, c *quark.Client) string {
			return errClass(c.Sync(ctx, quark.SyncOptions{}, &partRowV2{}))
		}},
		{name: "ApplyPlan adds a column", canonical: true, run: func(ctx context.Context, c *quark.Client) string {
			return applyOp(ctx, c, quark.OpAddColumn{Table: "part_rows", Column: quark.Column{Name: "note", Type: "TEXT", Nullable: true}})
		}},
		{name: "ApplyPlan drops a column", canonical: true, run: func(ctx context.Context, c *quark.Client) string {
			return applyOp(ctx, c, quark.OpDropColumn{Table: "part_rows", Column: "score"})
		}},
		{name: "ApplyPlan alters a column", canonical: true, run: func(ctx context.Context, c *quark.Client) string {
			return applyOp(ctx, c, quark.OpAlterColumn{
				Table: "part_rows",
				Old:   quark.Column{Name: "tag", Type: "TEXT", Nullable: true},
				New:   quark.Column{Name: "tag", Type: "TEXT", Nullable: false, Default: ptr("''")},
			})
		}},
		{name: "ApplyPlan adds a foreign key", canonical: true, run: func(ctx context.Context, c *quark.Client) string {
			if _, err := c.Raw().ExecContext(ctx, `ALTER TABLE "part_rows" ADD COLUMN "parent_id" INTEGER`); err != nil {
				return "setup: " + errClass(err)
			}
			return applyOp(ctx, c, quark.OpAddForeignKey{Table: "part_rows", ForeignKey: quark.ForeignKey{
				Name: "fk_part_parent", Columns: []string{"parent_id"},
				RefTable: "part_parents", RefColumns: []string{"id"},
			}})
		}},

		// An ApplyPlan whose second op fails must leave the table as it was
		// on an engine whose DDL is transactional — SQLite's — and the
		// dialect says whether it is (SupportsTransactionalDDL). The other
		// ApplyPlan steps cannot see that: an op that succeeds succeeds on
		// the resumable path too, which writes a checkpoint table instead
		// of rolling back.
		{name: "ApplyPlan undoes a plan that fails half-way", canonical: true, run: func(ctx context.Context, c *quark.Client) string {
			err := c.ApplyPlan(ctx, quark.Plan{Ops: []quark.Operation{
				quark.OpAddColumn{Table: "part_rows", Column: quark.Column{Name: "half", Type: "TEXT", Nullable: true}},
				quark.OpDropColumn{Table: "part_rows", Column: "no_such_column"},
			}})
			s, ierr := c.IntrospectSchema(ctx)
			if ierr != nil {
				return "introspect: " + errClass(ierr)
			}
			kept := false
			for _, t := range s.Tables {
				for _, col := range t.Columns {
					if t.Name == "part_rows" && col.Name == "half" {
						kept = true
					}
				}
			}
			return fmt.Sprintf("%s first-op-kept=%v", errClass(err), kept)
		}},

		// --- the query path, on the same table ------------------------------
		{name: "Create assigns the primary key", canonical: true, run: func(ctx context.Context, c *quark.Client) string {
			r := partRow{Name: "gamma", Created: stamp}
			if err := quark.For[partRow](ctx, c).Create(&r); err != nil {
				return errClass(err)
			}
			return fmt.Sprintf("id=%d", r.ID)
		}},
		{name: "a time and a bool read back as written", canonical: true, run: func(ctx context.Context, c *quark.Client) string {
			r, err := quark.For[partRow](ctx, c).Where("name", "=", "alpha").First()
			if err != nil {
				return errClass(err)
			}
			return fmt.Sprintf("created=%s active=%v", r.Created.UTC().Format(time.RFC3339), r.Active)
		}},
		{name: "OrderBy + Limit + Offset", canonical: true, run: func(ctx context.Context, c *quark.Client) string {
			return idsOf(quark.For[partRow](ctx, c).OrderBy("id", "DESC").Limit(2).Offset(1).List())
		}},
		{name: "a duplicate key is a unique violation", canonical: true, run: func(ctx context.Context, c *quark.Client) string {
			return errClass(quark.For[partRow](ctx, c).Create(&partRow{Name: "alpha", Created: stamp}))
		}},
		{name: "Upsert updates on conflict", canonical: true, run: func(ctx context.Context, c *quark.Client) string {
			if err := quark.For[partRow](ctx, c).Upsert(&partRow{Name: "beta", Tag: "upserted", Created: stamp}, []string{"name"}, []string{"tag"}); err != nil {
				return errClass(err)
			}
			got, err := quark.For[partRow](ctx, c).Where("name", "=", "beta").First()
			if err != nil {
				return errClass(err)
			}
			return got.Tag
		}},
		{name: "CreateBatch", canonical: true, run: func(ctx context.Context, c *quark.Client) string {
			if err := quark.For[partRow](ctx, c).CreateBatch([]*partRow{{Name: "b1", Created: stamp}, {Name: "b2", Created: stamp}}); err != nil {
				return errClass(err)
			}
			n, err := quark.For[partRow](ctx, c).Count()
			return fmt.Sprintf("%s rows=%d", errClass(err), n)
		}},
		{name: "Update and Delete", canonical: true, run: func(ctx context.Context, c *quark.Client) string {
			r, err := quark.For[partRow](ctx, c).Where("name", "=", "alpha").First()
			if err != nil {
				return errClass(err)
			}
			r.Tag = "updated"
			u, err := quark.For[partRow](ctx, c).Update(&r)
			if err != nil {
				return "update: " + errClass(err)
			}
			d, err := quark.For[partRow](ctx, c).Delete(&r)
			return fmt.Sprintf("updated=%d deleted=%d %s", u, d, errClass(err))
		}},
		{name: "WhereContains matches a literal %", canonical: true, run: func(ctx context.Context, c *quark.Client) string {
			return idsOf(quark.For[partRow](ctx, c).WhereContains("name", "%").List())
		}},
		{name: "a savepoint rolls back half a transaction", canonical: true, run: func(ctx context.Context, c *quark.Client) string {
			err := c.Tx(ctx, func(tx *quark.Tx) error {
				if err := quark.ForTx[partRow](ctx, tx).Create(&partRow{Name: "kept", Created: stamp}); err != nil {
					return err
				}
				if err := tx.Savepoint("half"); err != nil {
					return err
				}
				if err := quark.ForTx[partRow](ctx, tx).Create(&partRow{Name: "undone", Created: stamp}); err != nil {
					return err
				}
				return tx.RollbackTo("half")
			})
			if err != nil {
				return errClass(err)
			}
			n, err := quark.For[partRow](ctx, c).WhereIn("name", []any{"kept", "undone"}).Count()
			return fmt.Sprintf("%s rows=%d", errClass(err), n)
		}},
		{name: "FOR UPDATE is refused the way the dialect says", canonical: true, run: func(ctx context.Context, c *quark.Client) string {
			_, err := quark.For[partRow](ctx, c).ForUpdate().List()
			return errClass(err)
		}},
		{name: "PaginateAfter returns a page and a token", canonical: true, run: func(ctx context.Context, c *quark.Client) string {
			page, err := quark.For[partRow](ctx, c).OrderBy("id", "ASC").PaginateAfter(2, "")
			if err != nil {
				return errClass(err)
			}
			return fmt.Sprintf("rows=%d token=%v", len(page.Items), page.Next != "")
		}},
	}
}

func ptr[T any](v T) *T { return &v }

var partDB atomic.Int64

// participantDivergences runs every step on both arms and returns the steps
// whose outcome differs, sorted.
func participantDivergences(t *testing.T, e *env) []string {
	t.Helper()
	registerExtlite(t)
	registerExtliteClassifier(t)

	open := func(driverName string) *quark.Client {
		c, _ := e.open(t, driverName, fmt.Sprintf("part_%s_%d", driverName, partDB.Add(1)))
		return c
	}
	if got := open(extliteName).Dialect().Name(); got != extliteName {
		t.Fatalf("quark.New(%q) resolved dialect %q: the battery is not measuring the external name", extliteName, got)
	}

	var diverged []string
	for _, s := range partBattery() {
		outcome := map[string]string{}
		for _, arm := range []string{"sqlite", extliteName} {
			c := open(arm)
			if s.canonical {
				if err := canonical(e.ctx, c); err != nil {
					t.Fatalf("%s: set up the canonical table on %s: %v", s.name, arm, err)
				}
			}
			outcome[arm] = s.run(e.ctx, c)
		}
		t.Logf("%-80s sqlite=%-24q extlite=%q", s.name, outcome["sqlite"], outcome[extliteName])
		if outcome["sqlite"] != outcome[extliteName] {
			diverged = append(diverged, s.name)
		}
	}
	sort.Strings(diverged)
	return diverged
}
