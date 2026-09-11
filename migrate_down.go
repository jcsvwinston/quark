// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import "fmt"

// Down returns the plan that undoes p: every operation inverted, in reverse
// order, so that applying p and then its Down leaves the schema where it
// started.
//
//	plan, _ := client.PlanMigration(ctx, &User{})
//	down, err := plan.Down()          // build it BEFORE applying plan
//	if err != nil {
//	    return err                     // not reversible; see below
//	}
//	if err := client.ApplyPlan(ctx, plan); err != nil {
//	    return err
//	}
//	// …later, to roll back:
//	err = client.ApplyPlan(ctx, down)
//
// Reverse order matters as much as the inversion: a plan that creates a table
// and then indexes it has to drop the index before the table, or the drop
// refers to something that no longer exists.
//
// # What is not reversible, and why it errors rather than guessing
//
// Two operations lose information, so their inverse cannot be derived from
// the plan alone:
//
//   - OpDropTable carries only the table's NAME. Recreating it needs the
//     columns, keys and indexes it had, which the plan does not record.
//   - OpDropColumn carries only the column's name, not its type, nullability
//     or default.
//
// Down returns an error naming the operation instead of emitting a partial
// rollback. A rollback that silently restores a table without its indexes is
// worse than one that refuses: it reports success and leaves the schema
// subtly different.
//
// Capture Down BEFORE applying the plan when you intend to roll back. Nothing
// stops you calling it after — the plan is a value — but a plan built from a
// schema you have already changed describes a different starting point.
func (p Plan) Down() (Plan, error) {
	out := make([]Operation, 0, len(p.Ops))
	for i := len(p.Ops) - 1; i >= 0; i-- {
		inv, err := invertOperation(p.Ops[i])
		if err != nil {
			return Plan{}, err
		}
		out = append(out, inv)
	}
	return Plan{Ops: out}, nil
}

// ErrIrreversibleOperation is returned by [Plan.Down] for an operation whose
// inverse is not derivable from the plan. Check with errors.Is.
var ErrIrreversibleOperation = fmt.Errorf("operation cannot be reversed from the plan alone")

func invertOperation(op Operation) (Operation, error) {
	switch o := op.(type) {
	case OpCreateTable:
		return OpDropTable{Table: o.Table.Name}, nil
	case OpAddColumn:
		return OpDropColumn{Table: o.Table, Column: o.Column.Name}, nil
	case OpCreateIndex:
		return OpDropIndex{Table: o.Table, Index: o.Index.Name}, nil
	case OpAddForeignKey:
		return OpDropForeignKey{Table: o.Table, ForeignKey: o.ForeignKey.Name}, nil
	case OpAddCheck:
		return OpDropCheck{Table: o.Table, Check: o.Check.Name}, nil
	case OpAlterColumn:
		// Symmetric: swapping the two sides is the inverse, and both are
		// fully described in the operation.
		return OpAlterColumn{Table: o.Table, Old: o.New, New: o.Old}, nil

	case OpDropTable:
		return nil, fmt.Errorf("%w: DROP TABLE %s — the plan records the name but not the table's shape",
			ErrIrreversibleOperation, o.Table)
	case OpDropColumn:
		return nil, fmt.Errorf("%w: DROP COLUMN %s.%s — the plan records the name but not the column's type, nullability or default",
			ErrIrreversibleOperation, o.Table, o.Column)
	case OpDropIndex:
		return nil, fmt.Errorf("%w: DROP INDEX %s ON %s — the plan records the name but not the indexed columns or uniqueness",
			ErrIrreversibleOperation, o.Index, o.Table)
	case OpDropForeignKey:
		return nil, fmt.Errorf("%w: DROP FOREIGN KEY %s ON %s — the plan records the name but not the referenced table or columns",
			ErrIrreversibleOperation, fkLabel(o.ForeignKey), o.Table)
	case OpDropCheck:
		return nil, fmt.Errorf("%w: DROP CHECK %s ON %s — the plan records the name but not the expression",
			ErrIrreversibleOperation, o.Check, o.Table)
	default:
		return nil, fmt.Errorf("%w: %T", ErrIrreversibleOperation, op)
	}
}
