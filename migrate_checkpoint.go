// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// checkpointStillHolds asks the live schema whether the ops a plan's
// checkpoint records as applied are still there, before the resumable path
// of ApplyPlan skips them (QK-51). It answers with the reason when it finds
// one that is not: the checkpoint then describes a schema that no longer
// exists — the tables were dropped and recreated, a test reset its database,
// a restore went back to before the plan — and skipping would leave the plan
// unapplied while ApplyPlan reported success.
//
// A dialect without a SchemaIntrospector cannot be asked, and the checkpoint
// is trusted as it was before: holds is true and the error nil.
func (c *Client) checkpointStillHolds(ctx context.Context, applied []Operation) (holds bool, reason string, err error) {
	live, err := c.IntrospectSchema(ctx)
	if errors.Is(err, ErrUnsupportedFeature) {
		return true, "", nil
	}
	if err != nil {
		return false, "", fmt.Errorf("read the schema to check the plan's checkpoint: %w", err)
	}
	if reason, missing := checkpointMismatch(live, applied); missing {
		return false, reason, nil
	}
	return true, "", nil
}

// checkpointMismatch walks the ops a checkpoint records as applied, last
// first, and returns the first one the live schema shows not applied, with
// the reason. Only positive evidence counts — a table the op created that is
// missing, a column it dropped that is there, a column it altered that still
// reads as the op's Old. An op whose effect the catalog does not settle (an
// altered column that reads as neither its Old nor its New, a foreign key
// dropped without a name) is taken as applied, as the checkpoint said, so a
// resume after a fix made by hand still resumes.
//
// An op that a later op of the same prefix touches again is not asked: its
// effect is the later op's to show. A DROP INDEX followed by a CREATE INDEX
// of the same name leaves the index there, which says nothing about the drop.
// A CREATE or DROP TABLE answers for every earlier op on that table.
//
// Names compare without case: MySQL may fold table names, and Oracle's
// catalog reads them back in its own case.
func checkpointMismatch(live Schema, applied []Operation) (string, bool) {
	cat := newLiveCatalog(live)
	touched := map[string]bool{}    // objects a later op touches again
	tableLevel := map[string]bool{} // tables a later op creates or drops
	for i := len(applied) - 1; i >= 0; i-- {
		op := applied[i]
		table, object := checkpointObject(op)
		if tableLevel[table] || touched[object] {
			continue
		}
		touched[object] = true
		switch op.(type) {
		case OpCreateTable, OpDropTable:
			tableLevel[table] = true
		}
		if why, missing := opMissingFrom(cat, op); missing {
			return fmt.Sprintf("op %d (%s): %s", i, op.String(), why), true
		}
	}
	return "", false
}

// checkpointObject names the table an op works on and the object it leaves
// changed, both in lower case: the key under which a later op of the same
// plan supersedes it.
func checkpointObject(op Operation) (table, object string) {
	low := strings.ToLower
	switch o := op.(type) {
	case OpCreateTable:
		return low(o.Table.Name), "table:" + low(o.Table.Name)
	case OpDropTable:
		return low(o.Table), "table:" + low(o.Table)
	case OpAddColumn:
		return low(o.Table), "column:" + low(o.Table) + "." + low(o.Column.Name)
	case OpDropColumn:
		return low(o.Table), "column:" + low(o.Table) + "." + low(o.Column)
	case OpAlterColumn:
		return low(o.Table), "column:" + low(o.Table) + "." + low(o.New.Name)
	case OpCreateIndex:
		return low(o.Table), "index:" + low(o.Table) + "." + low(o.Index.Name)
	case OpDropIndex:
		return low(o.Table), "index:" + low(o.Table) + "." + low(o.Index)
	case OpAddForeignKey:
		if o.ForeignKey.Name != "" {
			return low(o.Table), "fk:" + low(o.Table) + "." + low(o.ForeignKey.Name)
		}
		return low(o.Table), "fk:" + low(o.Table) + "." + fkShapeKey(o.ForeignKey)
	case OpDropForeignKey:
		return low(o.Table), "fk:" + low(o.Table) + "." + low(o.ForeignKey)
	case OpAddCheck:
		return low(o.Table), "check:" + low(o.Table) + "." + low(o.Check.Name)
	case OpDropCheck:
		return low(o.Table), "check:" + low(o.Table) + "." + low(o.Check)
	}
	return "", fmt.Sprintf("op:%T", op)
}

// opMissingFrom reports, with the reason, whether the live catalog shows that
// op's effect is not there.
func opMissingFrom(cat liveCatalog, op Operation) (string, bool) {
	switch o := op.(type) {
	case OpCreateTable:
		if _, ok := cat.table(o.Table.Name); !ok {
			return "table " + o.Table.Name + " does not exist", true
		}
	case OpDropTable:
		if _, ok := cat.table(o.Table); ok {
			return "table " + o.Table + " still exists", true
		}
	case OpAddColumn:
		t, ok := cat.table(o.Table)
		if !ok {
			return "table " + o.Table + " does not exist", true
		}
		if _, ok := findLiveColumn(t, o.Column.Name); !ok {
			return "column " + o.Table + "." + o.Column.Name + " does not exist", true
		}
	case OpDropColumn:
		if t, ok := cat.table(o.Table); ok {
			if _, ok := findLiveColumn(t, o.Column); ok {
				return "column " + o.Table + "." + o.Column + " still exists", true
			}
		}
	case OpAlterColumn:
		// Only a column that still reads as the op's Old is evidence; one
		// that reads as neither Old nor New — a type the catalog spells its
		// own way in a plan written by hand — settles nothing.
		if columnsEqual(o.Old, o.New) {
			break
		}
		if t, ok := cat.table(o.Table); ok {
			if cur, ok := findLiveColumn(t, o.New.Name); ok {
				cur.Name = o.Old.Name
				if columnsEqual(cur, o.Old) {
					return "column " + o.Table + "." + o.New.Name + " still has the definition the op changed", true
				}
			}
		}
	case OpCreateIndex:
		t, ok := cat.table(o.Table)
		if !ok {
			return "table " + o.Table + " does not exist", true
		}
		// MySQL and MariaDB keep out of the catalog the index that backs a
		// foreign key of the same name (withoutFKBackingIndexes).
		if !liveIndexNamed(t, o.Index.Name) && !liveFKNamed(t, o.Index.Name) {
			return "index " + o.Index.Name + " on " + o.Table + " does not exist", true
		}
	case OpDropIndex:
		if t, ok := cat.table(o.Table); ok && liveIndexNamed(t, o.Index) {
			return "index " + o.Index + " on " + o.Table + " still exists", true
		}
	case OpAddForeignKey:
		t, ok := cat.table(o.Table)
		if !ok {
			return "table " + o.Table + " does not exist", true
		}
		named := o.ForeignKey.Name != "" && liveFKNamed(t, o.ForeignKey.Name)
		if !named && !liveFKShaped(t, o.ForeignKey) {
			return "foreign key " + fkLabel(o.ForeignKey.Name) + " on " + o.Table + " does not exist", true
		}
	case OpDropForeignKey:
		if o.ForeignKey == "" {
			break
		}
		if t, ok := cat.table(o.Table); ok && liveFKNamed(t, o.ForeignKey) {
			return "foreign key " + o.ForeignKey + " on " + o.Table + " still exists", true
		}
	case OpAddCheck:
		t, ok := cat.table(o.Table)
		if !ok {
			return "table " + o.Table + " does not exist", true
		}
		if !liveCheckNamed(t, o.Check.Name) {
			return "check " + o.Check.Name + " on " + o.Table + " does not exist", true
		}
	case OpDropCheck:
		if t, ok := cat.table(o.Table); ok && liveCheckNamed(t, o.Check) {
			return "check " + o.Check + " on " + o.Table + " still exists", true
		}
	}
	return "", false
}

// liveCatalog is the introspected schema keyed by lower-case table name.
type liveCatalog map[string]Table

func newLiveCatalog(s Schema) liveCatalog {
	m := make(liveCatalog, len(s.Tables))
	for _, t := range s.Tables {
		m[strings.ToLower(t.Name)] = t
	}
	return m
}

func (l liveCatalog) table(name string) (Table, bool) {
	t, ok := l[strings.ToLower(name)]
	return t, ok
}

func findLiveColumn(t Table, name string) (Column, bool) {
	for _, col := range t.Columns {
		if strings.EqualFold(col.Name, name) {
			return col, true
		}
	}
	return Column{}, false
}

func liveIndexNamed(t Table, name string) bool {
	for _, ix := range t.Indexes {
		if strings.EqualFold(ix.Name, name) {
			return true
		}
	}
	return false
}

func liveFKNamed(t Table, name string) bool {
	for _, fk := range t.ForeignKeys {
		if strings.EqualFold(fk.Name, name) {
			return true
		}
	}
	return false
}

// liveFKShaped finds a foreign key on the same columns to the same target,
// whatever its name: the identity Diff matches foreign keys by.
func liveFKShaped(t Table, want ForeignKey) bool {
	key := fkShapeKey(want)
	for _, fk := range t.ForeignKeys {
		if fkShapeKey(fk) == key {
			return true
		}
	}
	return false
}

func fkShapeKey(fk ForeignKey) string {
	return strings.ToLower(fmt.Sprintf("[%s]→%s[%s]",
		strings.Join(fk.Columns, ","), fk.RefTable, strings.Join(fk.RefColumns, ",")))
}

// liveCheckNamed: the three engines that take the checkpointed path list
// their checks (MySQL 8.0.16+, MariaDB, Oracle), so a check the catalog does
// not name is not there.
func liveCheckNamed(t Table, name string) bool {
	for _, chk := range t.Checks {
		if strings.EqualFold(chk.Name, name) {
			return true
		}
	}
	return false
}
