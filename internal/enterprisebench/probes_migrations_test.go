// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enterprisebench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/migrate"
	"github.com/jcsvwinston/quark/quarkmigrate"
)

// --- shared helpers ----------------------------------------------------------
//
// The migration executor writes its DDL straight to the *sql.DB (or the *sql.Tx)
// instead of going through Client.Exec, so WithQueryObserver never sees a single
// migration statement — the recorder this bench uses elsewhere is blind here.
// These probes therefore measure the CATALOG after the fact, and use Raw() both
// to read SQLite's own tables (IntrospectSchema filters the quark_* bookkeeping
// out) and to play "another process changed this database behind the plan".

func rawExec(t *testing.T, c *quark.Client, stmt string) {
	t.Helper()
	if _, err := c.Raw().ExecContext(context.Background(), stmt); err != nil {
		t.Fatalf("out-of-band statement %q: %v", stmt, err)
	}
}

// sqliteObjectExists asks SQLite's catalog directly, which is the only way to
// see a table IntrospectSchema hides (the quark_* bookkeeping tables).
func sqliteObjectExists(t *testing.T, c *quark.Client, name string) bool {
	t.Helper()
	var n int
	if err := c.Raw().QueryRowContext(context.Background(),
		"SELECT count(*) FROM sqlite_master WHERE name = ?", name).Scan(&n); err != nil {
		t.Fatalf("read sqlite_master for %q: %v", name, err)
	}
	return n > 0
}

// migrationLimits is how the migration guide tells an application to build the
// client it drives the versioned migrator with: the migrator records its ledger
// through Client.Exec, which the default limits refuse.
func migrationLimits() quark.Limits {
	l := quark.DefaultLimits()
	l.AllowRawQueries = true
	return l
}

func tableOf(s quark.Schema, name string) (quark.Table, bool) {
	for _, tb := range s.Tables {
		if tb.Name == name {
			return tb, true
		}
	}
	return quark.Table{}, false
}

func hasColumn(tb quark.Table, name string) bool {
	for _, col := range tb.Columns {
		if col.Name == name {
			return true
		}
	}
	return false
}

func hasIndex(tb quark.Table, name string) bool {
	for _, idx := range tb.Indexes {
		if idx.Name == name {
			return true
		}
	}
	return false
}

// fingerprint renders the catalog as comparable text. A round trip that claims
// to restore a schema is only proven by comparing what the catalog holds before
// and after, not by the absence of an error.
func fingerprint(s quark.Schema) string {
	var b strings.Builder
	tables := append([]quark.Table(nil), s.Tables...)
	sort.Slice(tables, func(i, j int) bool { return tables[i].Name < tables[j].Name })
	for _, tb := range tables {
		fmt.Fprintf(&b, "table %s\n", tb.Name)
		for _, col := range tb.Columns {
			fmt.Fprintf(&b, "  col %s %s nullable=%v pk=%v\n", col.Name, col.Type, col.Nullable, col.PrimaryKey)
		}
		for _, idx := range tb.Indexes {
			fmt.Fprintf(&b, "  idx %s (%s) unique=%v\n", idx.Name, strings.Join(idx.Columns, ","), idx.Unique)
		}
	}
	return b.String()
}

func opNames(ops []quark.Operation) string {
	parts := make([]string, len(ops))
	for i, op := range ops {
		parts[i] = op.String()
	}
	return strings.Join(parts, " | ")
}

// --- shared helpers: classifying what a probe measured ------------------------

// migOpIsIndexOrFK reports whether an op is index or foreign-key work.
//
// Several controls in this family promise HALF of the declarative loop: tables
// and columns round-trip, the catalog surface model tags cannot declare does
// not. A probe that only asks whether the residual plan is empty cannot tell
// that documented half from a regression that also started losing columns —
// both leave "some ops" behind, and both would keep the bench green. So the
// residual is CLASSIFIED before it decides a verdict.
func migOpIsIndexOrFK(op quark.Operation) bool {
	switch op.(type) {
	case quark.OpCreateIndex, quark.OpDropIndex, quark.OpAddForeignKey, quark.OpDropForeignKey:
		return true
	default:
		return false
	}
}

// migResidualIsIndexOrFKOnly reports whether every op left over after an apply
// is index or foreign-key work. A table or column op in there means the half
// the title promises is the one that broke.
func migResidualIsIndexOrFKOnly(ops []quark.Operation) bool {
	for _, op := range ops {
		if !migOpIsIndexOrFK(op) {
			return false
		}
	}
	return len(ops) > 0
}

// migLogLine is one record a probe's logger saw.
type migLogLine struct {
	level slog.Level
	msg   string
}

// migLogSink captures what a client or a migrator LOGGED.
//
// Two notes in this family make a claim about logging — "reports it at Debug
// only", "applies with no error and no warning". The bench's clients write to
// io.Discard, so a probe that installs no sink of its own is REPEATING those
// claims from the source instead of measuring them: the day the line moves to
// Warn, or disappears, nothing here would notice. This sink records every
// level, so a probe can check both that the line it expects is there and that
// nothing louder came with it.
type migLogSink struct {
	mu    sync.Mutex
	lines []migLogLine
}

func (s *migLogSink) Enabled(context.Context, slog.Level) bool { return true }

func (s *migLogSink) Handle(_ context.Context, r slog.Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, migLogLine{level: r.Level, msg: r.Message})
	return nil
}

func (s *migLogSink) WithAttrs([]slog.Attr) slog.Handler { return s }

func (s *migLogSink) WithGroup(string) slog.Handler { return s }

// logger hands the sink to WithLogger, at a level that lets Debug through:
// measuring "reported at Debug only" needs the Debug line to arrive.
func (s *migLogSink) logger() *slog.Logger { return slog.New(s) }

// matching returns the recorded lines whose message contains any of needles,
// case-insensitively.
func (s *migLogSink) matching(needles ...string) []migLogLine {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []migLogLine
	for _, l := range s.lines {
		for _, n := range needles {
			if strings.Contains(strings.ToLower(l.msg), strings.ToLower(n)) {
				out = append(out, l)
				break
			}
		}
	}
	return out
}

// atLeast returns the recorded lines at or above level.
func (s *migLogSink) atLeast(level slog.Level) []migLogLine {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []migLogLine
	for _, l := range s.lines {
		if l.level >= level {
			out = append(out, l)
		}
	}
	return out
}

func migRenderLines(lines []migLogLine) string {
	parts := make([]string, len(lines))
	for i, l := range lines {
		parts[i] = fmt.Sprintf("%s %q", l.level, l.msg)
	}
	return strings.Join(parts, " | ")
}

// migStaleNeedles is the vocabulary a message would use to say "the schema
// moved under this plan". One list, read both from errors and from log lines,
// so the two halves of MIG-05 cannot drift apart.
var migStaleNeedles = []string{"stale", "changed since", "no longer", "hash mismatch", "out of date", "drift", "replan", "re-plan"}

// migMentionsStaleness reports whether a message reads as staleness rather than
// as the database refusing the statement. The difference is the whole of
// MIG-05: a collision reported by SQLite is the database catching the problem,
// not the library detecting that the plan is out of date.
func migMentionsStaleness(text string) bool {
	low := strings.ToLower(text)
	for _, needle := range migStaleNeedles {
		if strings.Contains(low, needle) {
			return true
		}
	}
	return false
}

// --- MIG-01 ------------------------------------------------------------------

// probeMigDeclarativeDiff drives the declarative loop the way an application
// would: a desired schema built by hand (the only form that can carry indexes
// and foreign keys, since model tags cannot), diffed against the live catalog,
// applied, and then diffed AGAIN. The second diff is the measurement — a
// declarative loop that converges leaves nothing to do; one that drops half the
// desired surface keeps proposing the same work for ever.
//
// WHAT the residual is made of decides the verdict, not how long it is. The
// title promises tables and columns; "some ops left over" is true both when the
// known index/FK gap is the only thing missing and when a regression started
// dropping columns too. So the probe reads the tables and the columns back from
// the catalog and classifies every residual op: anything above index and
// foreign-key work is the promised half breaking, which is a different verdict.
func probeMigDeclarativeDiff(t *testing.T, e *env) verdict {
	c, _ := e.fresh(t, "mig01_declarative")

	desired := quark.Schema{Tables: []quark.Table{
		{
			Name:    "mig01_parent",
			Columns: []quark.Column{{Name: "id", Type: "INTEGER", PrimaryKey: true}},
		},
		{
			Name: "mig01_child",
			Columns: []quark.Column{
				{Name: "id", Type: "INTEGER", PrimaryKey: true},
				{Name: "parent_id", Type: "INTEGER", Nullable: true},
				{Name: "email", Type: "TEXT", Nullable: true},
			},
			Indexes: []quark.Index{{Name: "idx_mig01_child_email", Columns: []string{"email"}, Unique: true}},
			ForeignKeys: []quark.ForeignKey{{
				Name: "fk_mig01_child_parent", Columns: []string{"parent_id"},
				RefTable: "mig01_parent", RefColumns: []string{"id"},
			}},
		},
	}}

	current, err := c.IntrospectSchema(e.ctx)
	if err != nil {
		t.Fatalf("introspect the empty database: %v", err)
	}
	plan := quark.Plan{Ops: quark.Diff(desired, current)}
	if plan.IsEmpty() {
		t.Logf("Diff proposes nothing for two tables the database does not have")
		return absent
	}

	// Inert means inert: computing a plan must not touch the database.
	if mid, err := c.IntrospectSchema(e.ctx); err == nil && len(mid.Tables) != 0 {
		t.Fatalf("computing the plan created %d table(s)", len(mid.Tables))
	}

	if err := c.ApplyPlan(e.ctx, plan); err != nil {
		t.Logf("ApplyPlan refuses the hand-built plan: %v", err)
		return absent
	}

	live, err := c.IntrospectSchema(e.ctx)
	if err != nil {
		t.Fatalf("introspect after apply: %v", err)
	}
	if _, ok := tableOf(live, "mig01_parent"); !ok {
		t.Logf("ApplyPlan returned nil and mig01_parent is not there")
		return absent
	}
	child, ok := tableOf(live, "mig01_child")
	if !ok {
		t.Logf("ApplyPlan returned nil and mig01_child is not there")
		return absent
	}
	for _, col := range []string{"id", "parent_id", "email"} {
		if !hasColumn(child, col) {
			t.Logf("mig01_child lost the column %q the desired schema declares", col)
			return absent
		}
	}

	residual := quark.Diff(desired, live)
	if len(residual) == 0 {
		return present
	}
	if !migResidualIsIndexOrFKOnly(residual) {
		t.Logf("the loop leaves table or column work behind, which is the half this control promises: %s",
			opNames(residual))
		return absent
	}
	t.Logf("applied and still not converged: child has %d index(es) and %d fk(s); the same plan re-proposes: %s",
		len(child.Indexes), len(child.ForeignKeys), opNames(residual))
	return partial
}

// --- MIG-02 ------------------------------------------------------------------

// mig02Document is what a versionable schema file would hold: JSON text, in a
// repository, with no Go type of the user's compiled into the binary that reads
// it. Every field of quark.Schema is exported (Default is a *string precisely so
// "no default" and "empty default" survive), so a document deserialises straight
// into the value quark.Diff takes.
const mig02Document = `{
  "Tables": [
    {
      "Name": "mig02_parent",
      "Columns": [{"Name": "id", "Type": "INTEGER", "PrimaryKey": true}]
    },
    {
      "Name": "mig02_documented",
      "Columns": [
        {"Name": "id", "Type": "INTEGER", "PrimaryKey": true},
        {"Name": "title", "Type": "TEXT", "Nullable": true},
        {"Name": "parent_id", "Type": "INTEGER", "Nullable": true}
      ],
      "Indexes": [
        {"Name": "idx_mig02_documented_title", "Columns": ["title"], "Unique": true}
      ],
      "ForeignKeys": [
        {
          "Name": "fk_mig02_documented_parent",
          "Columns": ["parent_id"],
          "RefTable": "mig02_parent",
          "RefColumns": ["id"]
        }
      ]
    }
  ]
}`

// probeMigSchemaFromDocument measures the CAPABILITY, not one entry point: can a
// desired schema come from a document instead of compiled Go models, and get
// applied? So the probe takes the whole route a versioned schema file would —
// JSON bytes, json.Unmarshal into quark.Schema, quark.Diff against the live
// catalog, ApplyPlan — and reads the catalog back.
//
// Measuring PlanMigration instead (the models path) would answer a different
// question: that entry point reflects over Go values, so handing it a schema
// gets it reflected over as if it were one more model. That is a fact about
// PlanMigration, which MIG-11 measures; it is not this capability's verdict.
//
// The document deliberately declares an index and a foreign key, because that is
// where the route stops: what survives to the database is exactly what MIG-01
// finds from a hand-built schema, and the residual diff says so.
func probeMigSchemaFromDocument(t *testing.T, e *env) verdict {
	c, _ := e.fresh(t, "mig02_document")

	var desired quark.Schema
	if err := json.Unmarshal([]byte(mig02Document), &desired); err != nil {
		t.Fatalf("a quark.Schema does not deserialise from JSON: %v", err)
	}

	current, err := c.IntrospectSchema(e.ctx)
	if err != nil {
		t.Fatalf("introspect the empty database: %v", err)
	}
	plan := quark.Plan{Ops: quark.Diff(desired, current)}
	if plan.IsEmpty() {
		t.Logf("a document describing two tables the database does not have diffs to nothing")
		return absent
	}
	if err := c.ApplyPlan(e.ctx, plan); err != nil {
		t.Logf("the plan built from a document does not apply: %v", err)
		return absent
	}

	live, err := c.IntrospectSchema(e.ctx)
	if err != nil {
		t.Fatalf("introspect after apply: %v", err)
	}
	if _, ok := tableOf(live, "mig02_parent"); !ok {
		t.Logf("the document's mig02_parent is not in the catalog after a nil apply")
		return absent
	}
	documented, ok := tableOf(live, "mig02_documented")
	if !ok {
		t.Logf("the document's mig02_documented is not in the catalog after a nil apply")
		return absent
	}
	for _, col := range []string{"id", "title", "parent_id"} {
		if !hasColumn(documented, col) {
			t.Logf("the document declares the column %q and the catalog does not have it", col)
			return absent
		}
	}

	residual := quark.Diff(desired, live)
	if len(residual) == 0 {
		return present
	}
	if !migResidualIsIndexOrFKOnly(residual) {
		t.Logf("a document loses table or column work, which is the half this control promises: %s",
			opNames(residual))
		return absent
	}
	t.Logf("tables and columns round-trip from the document; what it declares beyond them does not: %s",
		opNames(residual))
	return partial
}

// --- MIG-03 ------------------------------------------------------------------

type mig03Doc struct {
	ID   int64  `db:"id" pk:"true"`
	Slug string `db:"slug" quark:"index"`
}

func (mig03Doc) TableName() string { return "mig03_docs" }

// probeMigModelDeclaredIndexes measures whether the index set is an INPUT to the
// plan PlanMigration builds. Asking for an index the model declares is
// impossible (no tag carries one), so the probe measures the other direction,
// which is the same wire: an index the live schema HAS and the model does not
// declare. A plan that carried indexes would have to say something about it.
// Then the index is removed behind the planner's back — if indexes were an
// input, the plan would change. It does not, in either direction.
//
// What the probe does NOT claim is that the diff is blind to indexes: quark.Diff
// compares cur.Indexes with des.Indexes and emits OpCreateIndex / OpDropIndex
// (MIG-01 makes it emit one). The plan is empty because PlanMigration COPIES the
// live index set into the desired schema before diffing — on purpose, so a
// model that cannot declare an index never proposes dropping one. The result is
// the same for the caller, and the mechanism matters for the fix.
func probeMigModelDeclaredIndexes(t *testing.T, e *env) verdict {
	c, _ := e.fresh(t, "mig03_model_indexes")

	// Migrate creates the table AND the index the model declares, and the
	// plan against that schema is empty: the declaration is one input read
	// by both, so a freshly migrated model has nothing to plan.
	if err := c.Migrate(e.ctx, &mig03Doc{}); err != nil {
		t.Fatalf("migrate the model: %v", err)
	}
	live, err := c.IntrospectSchema(e.ctx)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	docs, ok := tableOf(live, "mig03_docs")
	if !ok {
		t.Fatalf("Migrate created no table")
	}
	declaredCreated := hasIndex(docs, "idx_mig03_docs_slug")
	afterMigrate, err := c.PlanMigration(e.ctx, &mig03Doc{})
	if err != nil {
		t.Fatalf("plan against the migrated schema: %v", err)
	}

	// An index nobody declared is left alone: the plan must not propose
	// dropping a catalog object the model is silent about.
	if err := c.CreateIndex(e.ctx, "mig03_docs", "idx_mig03_docs_manual", []string{"id"}, false); err != nil {
		t.Fatalf("create the undeclared index: %v", err)
	}
	withManual, err := c.PlanMigration(e.ctx, &mig03Doc{})
	if err != nil {
		t.Fatalf("plan with the undeclared index: %v", err)
	}

	// The declared index goes missing: the plan has to propose it, and
	// only it.
	rawExec(t, c, "DROP INDEX idx_mig03_docs_slug")
	withoutDeclared, err := c.PlanMigration(e.ctx, &mig03Doc{})
	if err != nil {
		t.Fatalf("plan against the de-indexed schema: %v", err)
	}
	proposesDeclared := false
	onlyThat := len(withoutDeclared.Ops) == 1
	for _, op := range withoutDeclared.Ops {
		if ci, ok := op.(quark.OpCreateIndex); ok && ci.Index.Name == "idx_mig03_docs_slug" {
			proposesDeclared = true
		}
	}

	switch {
	case declaredCreated && afterMigrate.IsEmpty() && withManual.IsEmpty() && proposesDeclared && onlyThat:
		return present
	case !declaredCreated && afterMigrate.IsEmpty() && withoutDeclared.IsEmpty():
		// Neither Migrate nor the plan read the declaration: the S0 state.
		t.Logf("the same empty plan with and without the index: the index set is not an input to the plan built from models")
		return absent
	default:
		t.Logf("declared index created by Migrate=%v; plan after migrate: %s; plan with an undeclared index: %s; "+
			"plan without the declared index: %s", declaredCreated, opNames(afterMigrate.Ops),
			opNames(withManual.Ops), opNames(withoutDeclared.Ops))
		return partial
	}
}

// --- MIG-04 ------------------------------------------------------------------

// probeMigPlanHash measures the three properties a plan hash is used for: it is
// a digest of the ops (same ops built twice, same digest), it separates plans
// that differ anywhere, and it is a fixed-width hex string a checkpoint row can
// key on.
func probeMigPlanHash(t *testing.T, e *env) verdict {
	build := func(colType string) quark.Plan {
		return quark.Plan{Ops: []quark.Operation{
			quark.OpCreateTable{Table: quark.Table{
				Name:    "mig04_hashed",
				Columns: []quark.Column{{Name: "id", Type: "INTEGER", PrimaryKey: true}},
			}},
			quark.OpAddColumn{Table: "mig04_hashed", Column: quark.Column{Name: "note", Type: colType, Nullable: true}},
		}}
	}

	first, second := build("TEXT").Hash(), build("TEXT").Hash()
	if first != second {
		t.Logf("the same plan built twice hashes differently: %s vs %s", first, second)
		return absent
	}
	if len(first) != 64 || strings.TrimLeft(first, "0123456789abcdef") != "" {
		t.Logf("the digest is not 64 hex characters: %q", first)
		return partial
	}
	if other := build("INTEGER").Hash(); other == first {
		t.Logf("a plan with a different column type hashes the same: %s", other)
		return absent
	}
	empty := (quark.Plan{}).Hash()
	if len(empty) != 64 || empty == first {
		t.Logf("the empty plan's digest is %q", empty)
		return partial
	}
	return present
}

// --- MIG-05 ------------------------------------------------------------------

type mig05Account struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

func (mig05Account) TableName() string { return "mig05_accounts" }

type mig05Ledger struct {
	ID     int64 `db:"id" pk:"true"`
	Amount int64 `db:"amount"`
}

func (mig05Ledger) TableName() string { return "mig05_ledgers" }

// probeMigStaleSchemaDetected measures whether anything notices that the
// database moved between planning and applying. It takes two readings, because
// one of them alone cannot tell "there is no guard" from "the guard is fine
// with this particular change":
//
//   - BENIGN staleness: another process creates a table this plan does not
//     touch. A guard keyed on the schema the plan was computed against would
//     refuse (or at least say something); no guard at all applies in silence.
//     This is the reading that needs a LOGGER, not just an error — the note
//     claims "no error and no warning", and a client writing to io.Discard
//     cannot measure the second half.
//   - CONTRADICTING staleness: another process creates the very table the plan
//     is about to create. Here something always stops the apply — so the
//     measurement is WHAT stops it. An error the library raises about the plan
//     being out of date is a guard; "table already exists" from SQLite is the
//     database catching what the library did not look for.
//
// The plan's own identity is read first: a digest that moved with the schema
// would be a guard by itself.
func probeMigStaleSchemaDetected(t *testing.T, e *env) verdict {
	sinkBenign := &migLogSink{}
	c, _ := e.fresh(t, "mig05_stale", quark.WithLogger(sinkBenign.logger()))

	if err := c.Migrate(e.ctx, &mig05Account{}); err != nil {
		t.Fatalf("migrate the starting schema: %v", err)
	}
	plan, err := c.PlanMigration(e.ctx, &mig05Account{}, &mig05Ledger{})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if plan.IsEmpty() {
		t.Fatalf("nothing to apply: the probe needs a non-empty plan")
	}
	hashWhenPlanned := plan.Hash()

	// Another migrator moves the ground: a table appears that this plan was
	// never computed against.
	rawExec(t, c, "CREATE TABLE mig05_interloper (id INTEGER PRIMARY KEY)")

	replan, err := c.PlanMigration(e.ctx, &mig05Account{}, &mig05Ledger{})
	if err != nil {
		t.Fatalf("re-plan: %v", err)
	}
	if len(replan.Ops) == len(plan.Ops) {
		t.Fatalf("the planner itself does not see the change; this probe cannot measure the gap")
	}
	if plan.Hash() != hashWhenPlanned {
		t.Logf("the plan's digest moved with the schema: %s → %s", hashWhenPlanned, plan.Hash())
		return present
	}

	errBenign := c.ApplyPlan(e.ctx, plan)
	if errBenign != nil && !migMentionsStaleness(errBenign.Error()) {
		// The benign plan must be applicable; a failure for any other reason
		// means the probe is measuring something else entirely.
		t.Fatalf("applying the stale-but-compatible plan failed for a reason that is not staleness: %v", errBenign)
	}

	// The second reading needs a database of its own: the contradiction has to
	// be with the plan, not with what the first scenario already applied.
	sinkClash := &migLogSink{}
	c2, _ := e.fresh(t, "mig05_contradicted", quark.WithLogger(sinkClash.logger()))
	if err := c2.Migrate(e.ctx, &mig05Account{}); err != nil {
		t.Fatalf("migrate the starting schema for the contradicting case: %v", err)
	}
	clashing, err := c2.PlanMigration(e.ctx, &mig05Account{}, &mig05Ledger{})
	if err != nil {
		t.Fatalf("plan the contradicting case: %v", err)
	}
	if clashing.IsEmpty() {
		t.Fatalf("the contradicting case needs a non-empty plan")
	}
	rawExec(t, c2, "CREATE TABLE mig05_ledgers (id INTEGER PRIMARY KEY, amount INTEGER NOT NULL)")
	errClash := c2.ApplyPlan(e.ctx, clashing)

	warned := append(sinkBenign.matching(migStaleNeedles...), sinkClash.matching(migStaleNeedles...)...)

	switch {
	case errBenign != nil:
		t.Logf("ApplyPlan refuses a plan computed against a schema that moved: %v", errBenign)
		return present
	case len(warned) > 0:
		t.Logf("the stale plan applied, and the library did say so: %s", migRenderLines(warned))
		return partial
	case errClash != nil && migMentionsStaleness(errClash.Error()):
		t.Logf("staleness is caught only when the plan collides: %v", errClash)
		return partial
	}

	// The note names WHICH layer stops the collision, so the probe has to
	// measure that layer and not merely the absence of detection. These splits
	// used to fall through to the same absent: the engine refusing the CREATE —
	// what the note publishes — and nothing refusing it at all, which is a
	// different world in which the note's sentence is false. So the second one
	// fails out loud instead of reading green, and absent is left to the exact
	// conjunction the note describes.
	switch {
	case errClash == nil:
		t.Errorf("the contradicting plan applied with no error; this control's note says SQLite stops it "+
			"with \"table already exists\" — rewrite the note. The up-to-date plan would have been: %s",
			opNames(replan.Ops))
	case !strings.Contains(strings.ToLower(errClash.Error()), "already exists"):
		t.Errorf("the collision is stopped by something that is neither staleness nor the engine's "+
			"\"table already exists\": %v — this control's note names that layer, so rewrite it", errClash)
	default:
		t.Logf("the contradicting plan is stopped by the database, not by the library: %v", errClash)
	}
	t.Logf("a plan computed against a schema that no longer exists applied with no error, and none of the "+
		"%d line(s) the client logged (Debug included) says anything about it; the up-to-date plan would "+
		"have been: %s",
		len(sinkBenign.atLeast(slog.LevelDebug)), opNames(replan.Ops))
	return absent
}

// --- MIG-06 ------------------------------------------------------------------

// probeMigApplyPlan measures the two halves of "apply the plan" separately,
// because they answer differently. The first is failure semantics on a dialect
// with transactional DDL: a plan whose second op fails must leave nothing of the
// first behind. The second is the harder question — does a successful apply do
// what the op it applied describes? The op carries the whole table; the catalog
// afterwards says which parts of it were emitted.
//
// The title asserts the first half HOLDS, so that half cannot share a verdict
// with the second: if the failed plan leaves its first op behind, the
// transactional guarantee this control publishes is gone and the answer is
// absent, not the partial the note describes.
func probeMigApplyPlan(t *testing.T, e *env) verdict {
	c, _ := e.fresh(t, "mig06_apply")

	failing := quark.Plan{Ops: []quark.Operation{
		quark.OpCreateTable{Table: quark.Table{
			Name:    "mig06_kept",
			Columns: []quark.Column{{Name: "id", Type: "INTEGER", PrimaryKey: true}},
		}},
		quark.OpDropTable{Table: "mig06_never_existed"},
	}}
	if err := c.ApplyPlan(e.ctx, failing); err == nil {
		t.Fatalf("dropping a table that does not exist succeeded; the probe needs a failing op")
	}
	afterFailure, err := c.IntrospectSchema(e.ctx)
	if err != nil {
		t.Fatalf("introspect after the failed plan: %v", err)
	}
	if _, ok := tableOf(afterFailure, "mig06_kept"); ok {
		t.Logf("the failed plan left mig06_kept behind: the all-or-nothing half this control publishes is gone")
		return absent
	}

	full := quark.Table{
		Name: "mig06_flags",
		Columns: []quark.Column{
			{Name: "id", Type: "INTEGER", PrimaryKey: true},
			{Name: "label", Type: "TEXT", Nullable: true},
		},
		Indexes: []quark.Index{{Name: "idx_mig06_flags_label", Columns: []string{"label"}, Unique: true}},
	}
	if err := c.ApplyPlan(e.ctx, quark.Plan{Ops: []quark.Operation{quark.OpCreateTable{Table: full}}}); err != nil {
		// A refusal is a different control from "reports success for half the
		// op": loud beats silent, and the note would no longer describe it.
		t.Logf("ApplyPlan refuses a CREATE TABLE carrying an index instead of applying it in part: %v", err)
		return absent
	}
	live, err := c.IntrospectSchema(e.ctx)
	if err != nil {
		t.Fatalf("introspect after the create: %v", err)
	}
	flags, ok := tableOf(live, "mig06_flags")
	if !ok {
		t.Logf("ApplyPlan returned nil and mig06_flags is not there")
		return absent
	}
	// The op carries the WHOLE table, so the parts it DID emit have to be read
	// back before this verdict is handed out. Asking only whether the index is
	// missing answers the same partial two ways: the documented one, where the
	// create emits the columns and drops the index, and a regression where the
	// create emits little more than the primary key. "Applied in part" would
	// then cover the second in silence, which is worse than the gap the title
	// publishes. So the columns the op declares are read from the catalog, and
	// what is STILL left to do is classified the way MIG-01 and MIG-02 classify
	// their residual.
	for _, col := range []string{"id", "label"} {
		if !hasColumn(flags, col) {
			t.Logf("the create lost the column %q the op declares: the faithful-emission half this title publishes is gone", col)
			return absent
		}
	}
	if hasIndex(flags, "idx_mig06_flags_label") {
		return present
	}
	residual := quark.Diff(quark.Schema{Tables: []quark.Table{full}}, live)
	if !migResidualIsIndexOrFKOnly(residual) {
		t.Logf("the create leaves table or column work behind, not the index work this control's note describes: %s",
			opNames(residual))
		return absent
	}
	t.Logf("ApplyPlan reported success for an op carrying one index; the table has %d, its columns are all there, "+
		"and what is left to do is index work only: %s", len(flags.Indexes), opNames(residual))
	return partial
}

// --- MIG-07 ------------------------------------------------------------------

// probeMigAlterColumn walks the FOUR deltas an ALTER COLUMN can carry — type,
// nullable, default and primary key — and measures each one the same way,
// because the failure modes are not interchangeable:
//
//   - ErrUnsupportedFeature is a loud gap: the caller learns the delta did not
//     happen.
//   - Any other error is also a gap, and a worse-behaved one; counting only
//     ErrUnsupportedFeature let a driver syntax error read exactly like success.
//   - A nil error is NOT a landing. SQLite's AlterTableAlterColumn renders a SQL
//     comment, so the statement succeeds and the column does not move. Every
//     delta is therefore read back from the catalog, not trusted.
//
// The verdict comes from how many deltas LANDED: four is the whole control, none
// is the control missing, anything between is the partial the note describes.
// The old counter could only say "at least one gap", which reported the same
// partial whether three deltas worked or none did.
func probeMigAlterColumn(t *testing.T, e *env) verdict {
	c, _ := e.fresh(t, "mig07_alter")
	rawExec(t, c, "CREATE TABLE mig07_items (id INTEGER PRIMARY KEY, amount INTEGER NOT NULL)")

	readAmount := func() quark.Column {
		live, err := c.IntrospectSchema(e.ctx)
		if err != nil {
			t.Fatalf("introspect: %v", err)
		}
		items, ok := tableOf(live, "mig07_items")
		if !ok {
			t.Fatalf("mig07_items not introspected")
		}
		for _, col := range items.Columns {
			if col.Name == "amount" {
				return col
			}
		}
		t.Fatalf("mig07_items has no amount column")
		return quark.Column{}
	}

	// cur is the baseline every delta is built from, and it follows the column:
	// if a delta ever lands, the next one must be expressed against what the
	// catalog now holds, not against the column as it was created.
	cur := readAmount()

	// measure applies one delta and reports whether it REACHED the column.
	measure := func(label string, mutate func(*quark.Column), landed func(quark.Column) bool) bool {
		next := cur
		mutate(&next)
		err := c.ApplyPlan(e.ctx, quark.Plan{Ops: []quark.Operation{
			quark.OpAlterColumn{Table: "mig07_items", Old: cur, New: next},
		}})
		switch {
		case errors.Is(err, quark.ErrUnsupportedFeature):
			t.Logf("%s: refused with ErrUnsupportedFeature — a loud gap", label)
			return false
		case err != nil:
			t.Logf("%s: failed with something other than ErrUnsupportedFeature: %v", label, err)
			return false
		}
		got := readAmount()
		if !landed(got) {
			t.Logf("%s: ApplyPlan returned nil and the catalog did not move (type=%s nullable=%v pk=%v default=%v)",
				label, got.Type, got.Nullable, got.PrimaryKey, got.Default != nil)
			return false
		}
		cur = got
		return true
	}

	landedCount := 0

	wantPK := !cur.PrimaryKey
	if measure("primary-key delta",
		func(col *quark.Column) { col.PrimaryKey = wantPK },
		func(col quark.Column) bool { return col.PrimaryKey == wantPK }) {
		landedCount++
	}

	wantNullable := !cur.Nullable
	if measure("nullable-only delta",
		func(col *quark.Column) { col.Nullable = wantNullable },
		func(col quark.Column) bool { return col.Nullable == wantNullable }) {
		landedCount++
	}

	// The fourth delta the title names. A default-only change reaches the same
	// executor branch as the nullable one, which is exactly why it has to be
	// measured rather than assumed: the title claims four, the bench counted
	// three, and nothing was watching the one it did not count.
	wantDefault := "0"
	if measure("default-only delta",
		func(col *quark.Column) { col.Default = &wantDefault },
		func(col quark.Column) bool { return col.Default != nil && strings.Contains(*col.Default, wantDefault) }) {
		landedCount++
	}

	wantType := "TEXT"
	if measure("type delta",
		func(col *quark.Column) { col.Type = wantType },
		func(col quark.Column) bool { return strings.EqualFold(col.Type, wantType) }) {
		landedCount++
	}

	switch landedCount {
	case 4:
		return present
	case 0:
		t.Logf("none of the four deltas reaches the column on this dialect")
		return absent
	default:
		t.Logf("%d of the four deltas land", landedCount)
		return partial
	}
}

// --- MIG-08 ------------------------------------------------------------------

// probeMigPlanDown measures the down plan against the catalog, not against the
// error: apply, roll back, and compare the fingerprint with the one taken before
// the plan ran. The second half measures the refusal — an op whose inverse is
// not derivable has to fail loudly rather than emit half a rollback.
func probeMigPlanDown(t *testing.T, e *env) verdict {
	c, _ := e.fresh(t, "mig08_down")

	before, err := c.IntrospectSchema(e.ctx)
	if err != nil {
		t.Fatalf("introspect the starting point: %v", err)
	}

	up := quark.Plan{Ops: []quark.Operation{
		quark.OpCreateTable{Table: quark.Table{
			Name:    "mig08_notes",
			Columns: []quark.Column{{Name: "id", Type: "INTEGER", PrimaryKey: true}},
		}},
		quark.OpAddColumn{Table: "mig08_notes", Column: quark.Column{Name: "body", Type: "TEXT", Nullable: true}},
	}}
	down, err := up.Down()
	if err != nil {
		t.Logf("Down refuses a plan of create + add column: %v", err)
		return absent
	}
	if err := c.ApplyPlan(e.ctx, up); err != nil {
		t.Fatalf("apply the up plan: %v", err)
	}
	if err := c.ApplyPlan(e.ctx, down); err != nil {
		t.Logf("the generated down plan does not apply: %v", err)
		return partial
	}
	after, err := c.IntrospectSchema(e.ctx)
	if err != nil {
		t.Fatalf("introspect after the rollback: %v", err)
	}
	if fingerprint(after) != fingerprint(before) {
		t.Logf("the rollback left a different schema:\nbefore:\n%s\nafter:\n%s", fingerprint(before), fingerprint(after))
		return partial
	}

	irreversible := quark.Plan{Ops: []quark.Operation{quark.OpDropTable{Table: "mig08_notes"}}}
	if _, err := irreversible.Down(); !errors.Is(err, quark.ErrIrreversibleOperation) {
		t.Logf("inverting a DROP TABLE returned %v instead of refusing", err)
		return partial
	}
	return present
}

// --- MIG-09 ------------------------------------------------------------------

// probeMigReversibleBeyondTables takes each reversible op class through the
// database and back: add column, create index, add foreign key. Round trips are
// where a rollback stops being a claim, so each half is applied and the catalog
// read.
//
// Each result is kept in its OWN variable, because the title names which two
// pass and which one does not. A counter cannot say that: "two of three" is the
// same number when the index is the broken one, and the verdict would go on
// publishing a sentence the probe never checked.
func probeMigReversibleBeyondTables(t *testing.T, e *env) verdict {
	c, _ := e.fresh(t, "mig09_roundtrip")

	base := quark.Plan{Ops: []quark.Operation{
		quark.OpCreateTable{Table: quark.Table{
			Name:    "mig09_orgs",
			Columns: []quark.Column{{Name: "id", Type: "INTEGER", PrimaryKey: true}},
		}},
		quark.OpCreateTable{Table: quark.Table{
			Name: "mig09_people",
			Columns: []quark.Column{
				{Name: "id", Type: "INTEGER", PrimaryKey: true},
				{Name: "org_id", Type: "INTEGER", Nullable: true},
				{Name: "email", Type: "TEXT", Nullable: true},
			},
		}},
	}}
	if err := c.ApplyPlan(e.ctx, base); err != nil {
		t.Fatalf("create the starting tables: %v", err)
	}

	roundTrip := func(label string, up quark.Plan, present func(quark.Table) bool) bool {
		down, err := up.Down()
		if err != nil {
			t.Logf("%s: Down refuses: %v", label, err)
			return false
		}
		if err := c.ApplyPlan(e.ctx, up); err != nil {
			t.Logf("%s: the forward op does not apply: %v", label, err)
			return false
		}
		live, err := c.IntrospectSchema(e.ctx)
		if err != nil {
			t.Fatalf("%s: introspect after up: %v", label, err)
		}
		tbl, _ := tableOf(live, "mig09_people")
		if !present(tbl) {
			t.Logf("%s: the forward op reported success and the catalog does not show it", label)
			return false
		}
		if err := c.ApplyPlan(e.ctx, down); err != nil {
			t.Logf("%s: the generated down does not apply: %v", label, err)
			return false
		}
		live, err = c.IntrospectSchema(e.ctx)
		if err != nil {
			t.Fatalf("%s: introspect after down: %v", label, err)
		}
		tbl, _ = tableOf(live, "mig09_people")
		if present(tbl) {
			t.Logf("%s: the rollback applied and the object is still there", label)
			return false
		}
		return true
	}

	okColumn := roundTrip("add column",
		quark.Plan{Ops: []quark.Operation{quark.OpAddColumn{
			Table: "mig09_people", Column: quark.Column{Name: "nickname", Type: "TEXT", Nullable: true},
		}}},
		func(tb quark.Table) bool { return hasColumn(tb, "nickname") })

	okIndex := roundTrip("create index",
		quark.Plan{Ops: []quark.Operation{quark.OpCreateIndex{
			Table: "mig09_people", Index: quark.Index{Name: "idx_mig09_people_email", Columns: []string{"email"}, Unique: true},
		}}},
		func(tb quark.Table) bool { return hasIndex(tb, "idx_mig09_people_email") })

	fkUp := quark.Plan{Ops: []quark.Operation{quark.OpAddForeignKey{
		Table: "mig09_people",
		ForeignKey: quark.ForeignKey{
			Name: "fk_mig09_people_org", Columns: []string{"org_id"},
			RefTable: "mig09_orgs", RefColumns: []string{"id"},
		},
	}}}
	okFK := roundTrip("add foreign key", fkUp, func(tb quark.Table) bool { return len(tb.ForeignKeys) > 0 })

	// The FK half of this control also claims that the GENERATED ROLLBACK
	// refuses loudly. The round trip above never gets that far — the forward op
	// fails first and returns — so the rollback is measured on its own, or the
	// claim would be a line read off the source like any other.
	fkRollbackRefuses := false
	if !okFK {
		down, err := fkUp.Down()
		if err != nil {
			t.Logf("add foreign key: Down refuses to derive a rollback at all: %v", err)
		} else {
			switch err := c.ApplyPlan(e.ctx, down); {
			case errors.Is(err, quark.ErrUnsupportedFeature):
				fkRollbackRefuses = true
			case err == nil:
				t.Logf("add foreign key: the generated rollback applies on SQLite; only the forward half is missing")
			default:
				t.Logf("add foreign key: the generated rollback fails with something other than "+
					"ErrUnsupportedFeature: %v", err)
			}
		}
	}

	switch {
	case okColumn && okIndex && okFK:
		return present
	case okColumn && okIndex && !okFK && fkRollbackRefuses:
		// Exactly the sentence the title and the note publish: the two that
		// round-trip, the one that does not, and its loud refusal.
		return partial
	case !okColumn && !okIndex && !okFK:
		return absent
	default:
		t.Logf("a split this control does not describe: column=%v index=%v foreign key=%v (its rollback refuses loudly=%v)",
			okColumn, okIndex, okFK, fkRollbackRefuses)
		return absent
	}
}

// --- MIG-10 ------------------------------------------------------------------

type mig10Noop struct {
	ID int64 `db:"id" pk:"true"`
}

func (mig10Noop) TableName() string { return "mig10_noop" }

// probeMigLock measures the lock from both sides, and the title names three
// facts: the public API refuses with ErrUnsupportedFeature, the versioned
// migrator RUNS ANYWAY without the lock, and it says so at Debug only. The
// recorded partial is the conjunction of the three — every one of them is read
// here, and any other split returns a different verdict with a log naming which
// fact moved.
//
// The migrator gets a logger of this probe's own: the client is opened quiet,
// so "reports it at Debug only" is unmeasurable without a sink, and a claim
// nobody measures is exactly what this bench exists to stop publishing.
func probeMigLock(t *testing.T, e *env) verdict {
	c, _ := e.fresh(t, "mig10_lock", quark.WithLimits(migrationLimits()))

	lock, err := c.AcquireMigrationLock(e.ctx, "quark:schema:mig10", 2*time.Second)
	if err == nil {
		_ = lock.Release(e.ctx)
		return present
	}
	if !errors.Is(err, quark.ErrUnsupportedFeature) {
		t.Logf("AcquireMigrationLock failed with something other than ErrUnsupportedFeature, "+
			"so the refusal is not one a caller can classify: %v", err)
		return absent
	}

	// The migrator asks for the same lock by default. What it does with the
	// refusal is the half an application cannot see.
	migrate.Reset()
	t.Cleanup(migrate.Reset)
	migrate.Register(&migrate.Migration{
		ID:   "mig10_0001",
		Name: "noop",
		Up:   func(ctx context.Context, cl *quark.Client) error { return nil },
		Down: func(ctx context.Context, cl *quark.Client) error { return nil },
	})
	sink := &migLogSink{}
	m := migrate.NewMigrator(c, migrate.WithLogger(sink.logger()))
	if err := m.Up(e.ctx, 0); err != nil {
		t.Logf("the migrator refuses to run without a lock instead of degrading: %v", err)
		return absent
	}
	applied, err := m.GetApplied(e.ctx)
	if err != nil {
		t.Fatalf("read the ledger: %v", err)
	}
	if !applied["mig10_0001"] {
		t.Logf("the migrator returned nil without applying the migration: it neither takes the lock nor runs")
		return absent
	}

	degraded := sink.matching("no distributed lock", "proceeding without")
	if len(degraded) == 0 {
		t.Logf("the migrator ran unserialised and logged nothing about it; the lines it did emit were: %s",
			migRenderLines(sink.atLeast(slog.LevelDebug)))
		return absent
	}
	for _, line := range degraded {
		if line.level > slog.LevelDebug {
			t.Logf("the degradation is reported above Debug (%s), which this control does not describe: %s",
				line.level, migRenderLines(degraded))
			return absent
		}
	}
	if loud := sink.atLeast(slog.LevelWarn); len(loud) > 0 {
		t.Logf("running without the lock also emitted %s", migRenderLines(loud))
		return absent
	}
	t.Logf("the migrator applied a migration on a dialect whose lock it could not take, and said so only at Debug: %s",
		migRenderLines(degraded))
	return partial
}

// --- MIG-11 ------------------------------------------------------------------

type mig11Thing struct {
	ID int64 `db:"id" pk:"true"`
}

func (mig11Thing) TableName() string { return "mig11_things" }

// probeMigDiffWithoutCompiledModels measures the obstacle a `quark migrate diff`
// subcommand has to clear: PlanMigration's desired schema is whatever Go values
// the caller hands it, so a precompiled binary — which has none of the user's —
// can only pass nothing, and nothing reads as "the database should be empty".
//
// The probe has a way to go GREEN, which the earlier version did not: all three
// of its paths returned absent, so the day this changed the bench would have
// kept publishing the gap in silence. An empty plan and a plan that proposes
// something other than dropping the live tables are different facts, and get
// different verdicts.
//
// Scope, so this control and MIG-02 do not answer the same question with
// opposite verdicts: this one is about the MODELS entry point. Feeding the
// desired schema from a document through quark.Diff works, and MIG-02 measures
// it — including what it loses on the way.
func probeMigDiffWithoutCompiledModels(t *testing.T, e *env) verdict {
	c, _ := e.fresh(t, "mig11_no_models")

	if err := c.Migrate(e.ctx, &mig11Thing{}); err != nil {
		t.Fatalf("create the live schema: %v", err)
	}
	plan, err := c.PlanMigration(e.ctx)
	if err != nil {
		t.Logf("planning with no models refuses outright: %v — a binary learns it cannot do this, "+
			"instead of being handed a plan that empties the database", err)
		return partial
	}
	for _, op := range plan.Ops {
		if drop, ok := op.(quark.OpDropTable); ok && drop.Table == "mig11_things" {
			t.Logf("with no models the plan reads as \"the database should be empty\": %s", opNames(plan.Ops))
			return absent
		}
	}
	if plan.IsEmpty() {
		t.Logf("with no models the plan is empty: not destructive any more, and not a diff of the live schema either")
		return partial
	}
	t.Logf("with no models the plan proposes work that is not dropping the live tables: %s", opNames(plan.Ops))
	return present
}

// --- MIG-12 ------------------------------------------------------------------

type mig12Item struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

func (mig12Item) TableName() string { return "mig12_items" }

// probeMigEmbeddableWrapper drives the wrapper as a CI gate would: verify on a
// drifted schema, apply, verify again. The exit codes are the contract a
// pipeline reads, so the probe asserts on them and on the schema that results.
func probeMigEmbeddableWrapper(t *testing.T, e *env) verdict {
	c, _ := e.fresh(t, "mig12_wrapper")

	var out, errOut strings.Builder
	if code := quarkmigrate.RunWithOutput(e.ctx, quarkmigrate.ActionVerify, c, &out, &errOut, &mig12Item{}); code != quarkmigrate.ExitDriftDetected {
		t.Logf("verify on a drifted schema returned %d", code)
		return partial
	}
	if !strings.Contains(out.String(), "mig12_items") {
		t.Logf("the rendered plan does not name the table: %q", out.String())
		return partial
	}
	if code := quarkmigrate.RunWithOutput(e.ctx, quarkmigrate.ActionApply, c, &out, &errOut, &mig12Item{}); code != quarkmigrate.ExitSuccess {
		t.Logf("apply returned %d (stderr: %s)", code, errOut.String())
		return partial
	}
	if code := quarkmigrate.RunWithOutput(e.ctx, quarkmigrate.ActionVerify, c, &out, &errOut, &mig12Item{}); code != quarkmigrate.ExitSuccess {
		t.Logf("verify after apply still reports drift: %d", code)
		return partial
	}
	live, err := c.IntrospectSchema(e.ctx)
	if err != nil {
		t.Fatalf("introspect: %v", err)
	}
	if _, ok := tableOf(live, "mig12_items"); !ok {
		t.Logf("apply returned success and the table is not there")
		return absent
	}
	return present
}

// --- MIG-13 ------------------------------------------------------------------

// probeMigRenderPlanAsDDL measures what a library caller can write into a
// migration file. The plan value carries the whole table; the only rendering the
// API offers is Plan.String, so the probe reads what survives it. What the
// rendering drops is what a scaffold written from the library would lose.
func probeMigRenderPlanAsDDL(t *testing.T, e *env) verdict {
	plan := quark.Plan{Ops: []quark.Operation{quark.OpCreateTable{Table: quark.Table{
		Name: "mig13_invoices",
		Columns: []quark.Column{
			{Name: "id", Type: "INTEGER", PrimaryKey: true},
			{Name: "total", Type: "NUMERIC(12,2)", Nullable: false},
		},
		Indexes: []quark.Index{{Name: "idx_mig13_invoices_total", Columns: []string{"total"}}},
	}}}}

	rendered := plan.String()
	if !strings.Contains(rendered, "mig13_invoices") {
		t.Fatalf("the rendering does not even name the table: %q", rendered)
	}
	// WHAT the rendering drops decides the verdict, not how many needles missed.
	// A rendering that carries the columns and their types is a DIFFERENT
	// capability from one that carries only the table name: the first writes a
	// migration file that is missing its indexes — the same half MIG-01 and
	// MIG-02 call partial — and the second writes nothing a migration file
	// could be built from. Counting absences answered absent for both, so a gap
	// closed halfway would have moved nothing here and this verdict would have
	// gone stale in silence.
	var droppedColumns []string
	for _, want := range []string{"total", "NUMERIC(12,2)"} {
		if !strings.Contains(rendered, want) {
			droppedColumns = append(droppedColumns, want)
		}
	}
	droppedIndexes := !strings.Contains(rendered, "idx_mig13_invoices_total")
	switch {
	case len(droppedColumns) > 0:
		t.Logf("the op carries the columns and the index; the rendering is %q and drops %v, so nothing callable "+
			"from Go can write a migration file", strings.TrimSpace(rendered), droppedColumns)
		return absent
	case droppedIndexes:
		t.Logf("the rendering carries the columns and their types and drops the index the op declares: %q",
			strings.TrimSpace(rendered))
		return partial
	}
	return present
}

// --- MIG-14 ------------------------------------------------------------------

// probeMigVersionedMigrations drives the whole hand-written cycle: init the
// ledger, dry-run (which must NOT touch the schema), up, read the ledger, down.
// Each step is checked against the catalog as well as the ledger, because a
// ledger that records what did not happen is the failure mode worth measuring.
//
// It also MEASURES the note this control carries. The note tells applications to
// enable AllowRawQueries — a security limit — so the claim behind it ("the
// default client is refused") cannot be a line read off the source: the probe
// runs the same cycle on a default-limits client first and reads the refusal. If
// the refusal ever stops happening the bench fails here, which is the only way
// the note stops sending applications to lower a limit they no longer need.
func probeMigVersionedMigrations(t *testing.T, e *env) verdict {
	migrate.Reset()
	t.Cleanup(migrate.Reset)
	migrate.Register(&migrate.Migration{
		ID:   "20260101000001",
		Name: "create_mig14_notes",
		Up: func(ctx context.Context, cl *quark.Client) error {
			_, err := cl.Raw().ExecContext(ctx, "CREATE TABLE mig14_notes (id INTEGER PRIMARY KEY)")
			return err
		},
		Down: func(ctx context.Context, cl *quark.Client) error {
			_, err := cl.Raw().ExecContext(ctx, "DROP TABLE mig14_notes")
			return err
		},
	})

	// The default-limits reading, on a database of its own because the
	// migration body runs before the ledger row is refused.
	defaults, _ := e.fresh(t, "mig14_default_limits")
	dm := migrate.NewMigrator(defaults, migrate.WithoutLock(), migrate.WithLogger(quiet))
	if err := dm.Init(e.ctx); err != nil {
		t.Errorf("Init is refused on a default-limits client (%v); this control's note says the refusal "+
			"comes from the LEDGER INSERT — rewrite it", err)
	} else if err := dm.Up(e.ctx, 0); err == nil {
		t.Errorf("the versioned cycle now runs on a default-limits client: drop the AllowRawQueries " +
			"requirement from this control's note and from migrationLimits()")
	} else if !errors.Is(err, quark.ErrInvalidQuery) || !strings.Contains(err.Error(), "raw queries") {
		t.Errorf("the default-limits client fails for a reason that is not the raw-query limit: %v", err)
	}

	c, _ := e.fresh(t, "mig14_versioned", quark.WithLimits(migrationLimits()))
	m := migrate.NewMigrator(c, migrate.WithoutLock(), migrate.WithLogger(quiet))
	if err := m.Init(e.ctx); err != nil {
		t.Logf("Init: %v", err)
		return absent
	}
	if err := m.UpDryRun(e.ctx, 0); err != nil {
		t.Logf("UpDryRun: %v", err)
		return partial
	}
	if sqliteObjectExists(t, c, "mig14_notes") {
		t.Logf("the dry run created the table")
		return partial
	}
	if err := m.Up(e.ctx, 0); err != nil {
		t.Logf("Up: %v", err)
		return partial
	}
	if !sqliteObjectExists(t, c, "mig14_notes") {
		t.Logf("Up reported success and the table is not there")
		return absent
	}
	applied, err := m.GetApplied(e.ctx)
	if err != nil {
		t.Fatalf("GetApplied: %v", err)
	}
	if !applied["20260101000001"] {
		t.Logf("the ledger does not record the migration that ran")
		return partial
	}
	if err := m.Down(e.ctx, 1); err != nil {
		t.Logf("Down: %v", err)
		return partial
	}
	if sqliteObjectExists(t, c, "mig14_notes") {
		t.Logf("Down reported success and the table is still there")
		return partial
	}
	applied, err = m.GetApplied(e.ctx)
	if err != nil {
		t.Fatalf("GetApplied after down: %v", err)
	}
	if applied["20260101000001"] {
		t.Logf("the ledger still records a migration that was rolled back")
		return partial
	}
	return present
}

// --- MIG-15 ------------------------------------------------------------------

// probeMigBackfill measures resume the only way it can be measured: interrupt a
// backfill halfway and run it again with the same name, recording which primary
// keys each run saw. Resume is the second run starting where the first stopped —
// not re-reading rows it already processed, and not skipping rows it never did.
func probeMigBackfill(t *testing.T, e *env) verdict {
	c, _ := e.fresh(t, "mig15_backfill")
	rawExec(t, c, "CREATE TABLE mig15_rows (id INTEGER PRIMARY KEY, done INTEGER NOT NULL DEFAULT 0)")
	for i := 1; i <= 10; i++ {
		rawExec(t, c, fmt.Sprintf("INSERT INTO mig15_rows (id, done) VALUES (%d, 0)", i))
	}

	var first, second []int64
	err := c.Backfill(e.ctx, quark.BackfillSpec{
		Name: "mig15_backfill", Table: "mig15_rows", PKColumn: "id", BatchSize: 3,
		Process: func(ctx context.Context, pks []int64) error {
			if len(pks) > 0 && pks[0] > 3 {
				return errors.New("interrupted on purpose")
			}
			first = append(first, pks...)
			return nil
		},
	})
	if err == nil {
		t.Fatalf("the interrupted backfill reported success")
	}
	if len(first) == 0 {
		t.Fatalf("the first run processed nothing; the probe needs committed progress")
	}

	err = c.Backfill(e.ctx, quark.BackfillSpec{
		Name: "mig15_backfill", Table: "mig15_rows", PKColumn: "id", BatchSize: 3,
		Process: func(ctx context.Context, pks []int64) error {
			second = append(second, pks...)
			return nil
		},
	})
	if err != nil {
		t.Logf("the second run failed: %v", err)
		return partial
	}

	seen := map[int64]int{}
	for _, pk := range first {
		seen[pk]++
	}
	repeats := 0
	for _, pk := range second {
		if seen[pk] > 0 {
			repeats++
		}
		seen[pk]++
	}
	if repeats > 0 {
		t.Logf("the second run re-processed %d row(s) the first had committed: %v then %v", repeats, first, second)
		return partial
	}
	for i := int64(1); i <= 10; i++ {
		if seen[i] == 0 {
			t.Logf("row %d was never processed: %v then %v", i, first, second)
			return partial
		}
	}
	return present
}

// --- MIG-16 ------------------------------------------------------------------

// probeMigCheckpointTable measures whether the resumable checkpoint exists on
// this dialect at all. SQLite takes the transactional path, so the probe applies
// a plan and then asks SQLite's own catalog — IntrospectSchema filters the
// quark_* tables out — whether the checkpoint table was ever created.
func probeMigCheckpointTable(t *testing.T, e *env) verdict {
	c, _ := e.fresh(t, "mig16_checkpoint")

	plan := quark.Plan{Ops: []quark.Operation{quark.OpCreateTable{Table: quark.Table{
		Name:    "mig16_rows",
		Columns: []quark.Column{{Name: "id", Type: "INTEGER", PrimaryKey: true}},
	}}}}
	if err := c.ApplyPlan(e.ctx, plan); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if sqliteObjectExists(t, c, "quark_migration_state") {
		return present
	}
	t.Logf("after a successful ApplyPlan the checkpoint table does not exist on this dialect")
	return absent
}
