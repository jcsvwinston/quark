// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/jcsvwinston/quark/internal/migrate"
)

// Plan is the result of [Client.PlanMigration]: the ordered list of
// [Operation]s that, applied to the database, would bring it into
// alignment with the Go-side models.
//
// A Plan is inert — it doesn't execute itself. F3-3-execute (the
// follow-up PR) will add a Plan.Apply method that walks Ops and
// dispatches each to the per-dialect migrator helpers. The CLI plan
// command (F3-5) renders the Plan via [Plan.String] without ever
// touching SQL.
type Plan struct {
	// Ops is the diff between the desired schema (derived from
	// models) and the current schema (from [Client.IntrospectSchema]).
	// See the [Diff] godoc for ordering guarantees.
	Ops []Operation
}

// IsEmpty reports whether the Plan would be a no-op when applied.
// Equivalent to `len(p.Ops) == 0` but more readable in user code.
//
// Use this as the "did anything drift?" check in CI / health
// endpoints — a non-empty Plan means the Go models and the database
// schema have diverged.
func (p Plan) IsEmpty() bool { return len(p.Ops) == 0 }

// String renders the Plan as a multi-line human-readable report.
// Each Op contributes one line via its own [Operation.String].
// Empty plans render as "(no changes)".
//
// The format is intentionally minimal so the F3-5 CLI can wrap it
// without parsing — table or coloured output is the CLI's
// responsibility, not the Plan's.
func (p Plan) String() string {
	if p.IsEmpty() {
		return "(no changes)"
	}
	lines := make([]string, len(p.Ops))
	for i, op := range p.Ops {
		lines[i] = fmt.Sprintf("  %d. %s", i+1, op.String())
	}
	return strings.Join(lines, "\n")
}

// PlanMigration computes the [Plan] of operations needed to align
// the database schema with the Go-side models. It does NOT execute
// anything — the returned Plan is inert.
//
// The pipeline is:
//
//  1. Build a `desired` [Schema] by reflecting on the model structs
//     (the same metadata path [Client.Migrate] uses to render
//     CREATE TABLE DDL).
//  2. Read the `current` [Schema] via [Client.IntrospectSchema].
//  3. Call [Diff] to produce the ordered op list.
//  4. Wrap the ops in a [Plan].
//
// Surface caveats — known asymmetries between desired and current:
//
//   - **Type strings**: the desired Schema uses the migrator's
//     `SQLTypeWithOpts` output (`BIGINT`, `VARCHAR(255)`) while the
//     introspector returns whatever the catalog stores (lowercase,
//     parameter-bearing, or canonical form per dialect). The diff's
//     [normalizeType] helper collapses these to a comparable form
//     (case-fold, PG `character varying` ↔ `varchar` alias, MySQL
//     display-width strip), so a clean round-trip works on all 5
//     supported dialects since F3-3-types. Edge cases not yet
//     normalised: PG `int8`/`int4`/`int2` aliases (don't arise from
//     introspection; only relevant if the desired Schema is
//     hand-constructed with those names).
//   - **Indexes / FKs / CHECK** declared on the model: struct tags
//     don't yet carry index or FK metadata (CreateIndex /
//     AddForeignKey are explicit calls). PlanMigration's `desired`
//     Schema carries columns plus the synthesised m2m join tables
//     (see below) — indexes and FKs present in the
//     database but not in the model would show up as OpDropIndex /
//     OpDropForeignKey if Diff were left to its own devices. To
//     avoid that, PlanMigration **copies** the indexes / FKs / checks
//     from the current schema into the desired schema before
//     diffing, on the assumption that schema-level objects not
//     declared in models are managed manually. A future
//     F3-3-plan-indexes follow-up will let struct tags drive these.
//   - **m2m join tables ARE part of the desired schema**: for every
//     `rel:"many_to_many"` + `m2m:"join:fk:ref_fk"` tag, the desired
//     Schema includes the join table with the same shape
//     [Client.Migrate]'s createJoinTables emits (two int FK columns,
//     composite PK). Without this, the diff would propose DROPping a
//     table Quark itself created — destroying the join rows if the
//     plan were applied (superapp finding, task_b03f2155). An
//     explicit model mapping the join-table name takes precedence
//     over the synthetic shape.
//
// SQLite quirk: same Checks=nil handling as the rest of F3-3-core —
// no spurious drops when the database doesn't introspect checks.
func (c *Client) PlanMigration(ctx context.Context, models ...any) (Plan, error) {
	desired, err := c.modelsToSchema(models...)
	if err != nil {
		return Plan{}, fmt.Errorf("PlanMigration: build desired schema: %w", err)
	}
	current, err := c.IntrospectSchema(ctx)
	if err != nil {
		return Plan{}, fmt.Errorf("PlanMigration: introspect current schema: %w", err)
	}

	// Carry over the non-column surface from current → desired. The
	// rationale lives in the godoc above: struct tags don't yet
	// declare indexes / FKs / checks, so a strict Diff would emit
	// DROP ops for every catalog object the model is silent about.
	// Until F3-3-plan-indexes adds tag-driven indexes, the safe
	// default is "leave catalog-level objects alone".
	mergeNonColumnSurface(&desired, current)

	return Plan{Ops: Diff(desired, current)}, nil
}

// modelsToSchema builds a dialect-neutral [Schema] from Go model
// structs by reflecting on the cached [ModelMeta] for each. The
// resulting Schema carries columns only — see the [PlanMigration]
// godoc for why indexes / FKs / checks aren't surfaced from models.
//
// The SQL type strings come from the same `migrate.SQLTypeWithOpts`
// helper [Client.Migrate] uses, so the desired and (eventually)
// migrator-emitted DDL stay in lockstep. The caveat is that those
// strings don't always match what the catalog returns from
// [Client.IntrospectSchema] — see the type-strings note in the
// PlanMigration godoc.
func (c *Client) modelsToSchema(models ...any) (Schema, error) {
	tables := make([]Table, 0, len(models))
	// m2m join tables declared by the models' rel tags. Collected during
	// the model walk and synthesised AFTER it, so an explicit join-table
	// MODEL (a user struct mapping the same table name) always wins over
	// the synthetic two-column shape.
	type joinSpec struct{ table, fk, refFK string }
	var joins []joinSpec
	for _, model := range models {
		t := reflect.TypeOf(model)
		// `reflect.TypeOf(nil)` returns `nil`, which would panic on
		// the `t.Kind()` call below. Guard explicitly so the caller
		// who passes a stray `nil` (easy mistake with variadic args)
		// gets a clean error rather than a stack trace.
		if t == nil {
			return Schema{}, fmt.Errorf("model must not be nil")
		}
		if t.Kind() == reflect.Ptr {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct {
			return Schema{}, fmt.Errorf("model must be a struct, got %s", t.Kind())
		}
		meta := GetModelMetaByType(t)
		if meta == nil {
			return Schema{}, fmt.Errorf("no metadata for model %s", t.Name())
		}
		columns := make([]Column, 0, len(meta.Fields))
		for _, f := range meta.Fields {
			if f.Column == "" {
				// Field not mapped to a DB column (no db tag) — skip.
				continue
			}
			// Critical: pass `IsPK: false` here regardless of whether
			// the field is the PK. The migrator passes `IsPK: true`
			// when emitting CREATE TABLE so SQLTypeWithOpts returns a
			// full constraint-bearing fragment (`INTEGER PRIMARY KEY
			// AUTOINCREMENT` on SQLite, `SERIAL PRIMARY KEY` on PG,
			// etc.). That's correct for DDL but wrong here — Column.Type
			// is meant to carry the bare data type (`INTEGER`,
			// `bigint`) so it can compare 1:1 against what the
			// introspector reads from the catalog. PK status travels in
			// Column.PrimaryKey instead (F3-2-pk): the diff compares it
			// and ApplyPlan's CREATE TABLE renders the constraint from
			// it, so a plan-created table matches Migrate's output.
			sqlType := migrate.SQLTypeWithOpts(c.dialect.Name(), f.Type, migrate.TypeOptions{
				Size:      f.Size,
				Precision: f.Precision,
				Scale:     f.Scale,
				IsPK:      false,
			})
			col := Column{
				Name: f.Column,
				Type: sqlType,
				// A column is nullable when neither the not_null tag
				// nor the PK marker forbid it. The catalog readers
				// surface PK columns as Nullable=false too, so this
				// matches the introspector.
				Nullable:   !f.NotNull && !f.IsPK,
				PrimaryKey: f.IsPK,
			}
			if f.Default != "" {
				s := f.Default
				// Normalize boolean defaults to the dialect literal here too:
				// the plan/ApplyPlan DDL path (applyCreateTable) emits
				// col.Default verbatim, so an un-normalized "1" on a BOOLEAN
				// column would fail on PostgreSQL just like the direct Migrate
				// path. (Round-trip note: the desired default is now the
				// dialect literal, e.g. "TRUE" on PG; a PlanMigration diff
				// against an introspected catalog may still report a cosmetic
				// default difference until defaultsEqual learns bool-literal
				// equivalence — that's a diff refinement, not a migration
				// failure, and is tracked separately.)
				if migrate.IsBoolColumn(f.Type) {
					s = migrate.NormalizeBoolDefault(c.dialect.Name(), s)
				}
				col.Default = &s
			}
			columns = append(columns, col)
		}
		if len(columns) == 0 {
			return Schema{}, fmt.Errorf("no database columns for model %s", t.Name())
		}
		tables = append(tables, Table{Name: meta.Table, Columns: columns})
		for _, rel := range meta.Relations {
			if rel.Type != "many_to_many" || rel.JoinTable == "" {
				continue
			}
			joins = append(joins, joinSpec{table: rel.JoinTable, fk: rel.JoinFK, refFK: rel.JoinRefFK})
		}
	}

	// Synthesise the m2m join tables (superapp finding, task_b03f2155):
	// Migrate creates them via createJoinTables, so a desired schema that
	// omits them makes Diff propose DROPping a table Quark itself needs —
	// and applying that plan destroys the join rows. The synthetic shape
	// mirrors createJoinTables exactly: two int64 FK columns forming a
	// composite primary key (PK implies NOT NULL). Declared-from-both-
	// sides m2m (Project.Tags and Tag.Projects naming the same table)
	// dedupes by name; a table already produced by an explicit model is
	// skipped (the model's richer column set wins).
	//
	// TODO: two specs naming the same join table with DIFFERENT FK
	// columns (a typo in one side's m2m tag) dedupe silently — first one
	// wins, same as createJoinTables' CREATE IF NOT EXISTS. A warning
	// would help surface the typo; deferred until the logger is plumbed
	// through this path.
	declared := make(map[string]bool, len(tables))
	for _, t := range tables {
		declared[t.Name] = true
	}
	fkType := migrate.SQLTypeWithOpts(c.dialect.Name(), reflect.TypeOf(int64(0)), migrate.TypeOptions{})
	for _, j := range joins {
		if declared[j.table] {
			continue
		}
		declared[j.table] = true
		tables = append(tables, Table{
			Name: j.table,
			Columns: []Column{
				{Name: j.fk, Type: fkType, Nullable: false, PrimaryKey: true},
				{Name: j.refFK, Type: fkType, Nullable: false, PrimaryKey: true},
			},
		})
	}
	return Schema{Tables: tables}, nil
}

// mergeNonColumnSurface copies indexes, foreign keys, and checks
// from `current` into the matching tables of `desired`, so the diff
// doesn't emit spurious drops for catalog objects that the model
// can't yet declare (indexes, FKs, checks via struct tags is a
// future feature). Tables present in current but not in desired
// are NOT added — that case is a real "this table exists in the DB
// but not in the models" delta, which Diff legitimately surfaces
// as OpDropTable.
//
// Defensive copy: each slice is duplicated rather than aliased so
// F3-3-execute's future `Plan.Apply` can mutate the Plan's tables
// without corrupting the caller's `current` schema. The cost is
// negligible (schemas have small fan-out) and the alternative is a
// silent aliasing footgun.
func mergeNonColumnSurface(desired *Schema, current Schema) {
	curByName := make(map[string]Table, len(current.Tables))
	for _, t := range current.Tables {
		curByName[t.Name] = t
	}
	for i := range desired.Tables {
		dt := &desired.Tables[i]
		ct, ok := curByName[dt.Name]
		if !ok {
			continue
		}
		dt.Indexes = append([]Index(nil), ct.Indexes...)
		dt.ForeignKeys = append([]ForeignKey(nil), ct.ForeignKeys...)
		// Preserve the nil/empty distinction for Checks — Diff
		// treats nil as "not introspected" (SQLite contract) and
		// skips the comparison. Copying a nil slice as `append(nil,
		// nil...)` would yield an empty slice, breaking the skip.
		if ct.Checks != nil {
			dt.Checks = append([]Check(nil), ct.Checks...)
		}
	}
}
