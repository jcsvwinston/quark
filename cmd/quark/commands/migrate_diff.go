// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/fatih/color"
	"github.com/spf13/cobra"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/cmd/quark/internal/db"
	"github.com/jcsvwinston/quark/internal/migrate"
)

// A8 S10 (MIG-11). `quark migrate diff|plan|verify --from-models <dir>` diffs
// the schema the project's model structs describe against the live database
// without compiling those models into this binary: the structs are read with
// go/packages, the same static reader `migrate create --from-models` uses,
// and the SQL types come from the runtime's own mapping. PlanMigration — the
// library entry point — needs Go values and refuses when it gets none, which
// is the obstacle this command clears.

var migrateDiffFromModels string

func init() {
	for _, c := range []*cobra.Command{migrateDiffCmd, migratePlanCmd, migrateVerifyCmd} {
		c.Flags().StringVar(&migrateDiffFromModels, "from-models", "", "directory of the package whose model structs describe the desired schema (required)")
		_ = c.MarkFlagRequired("from-models")
		migrateCmd.AddCommand(c)
	}
}

var migrateDiffCmd = &cobra.Command{
	Use:           "diff",
	Short:         "Show the operations that would bring the database in line with the models",
	Example:       "  quark migrate diff --from-models ./models",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMigrateDiff(cmd, false)
	},
}

var migratePlanCmd = &cobra.Command{
	Use:           "plan",
	Short:         "Alias of diff: the pending operations, without applying them",
	Example:       "  quark migrate plan --from-models ./models",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMigrateDiff(cmd, false)
	},
}

var migrateVerifyCmd = &cobra.Command{
	Use:           "verify",
	Short:         "Exit non-zero when the database has drifted from the models (a CI gate)",
	Example:       "  quark migrate verify --from-models ./models   # exit 1 on drift",
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runMigrateDiff(cmd, true)
	},
}

func runMigrateDiff(cmd *cobra.Command, gate bool) error {
	models, err := loadModelsForDDL(migrateDiffFromModels)
	if err != nil {
		return err
	}
	client, err := db.GetQuarkClient()
	if err != nil {
		return err
	}
	defer client.Close()
	ctx := context.Background()

	plan, err := planFromStaticModels(ctx, client, models)
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if plan.IsEmpty() {
		fmt.Fprintln(out, color.GreenString("schema in sync: no pending operations"))
		return nil
	}
	fmt.Fprintf(out, "%d pending operation(s):\n%s\n", len(plan.Ops), plan.String())
	if gate {
		return fmt.Errorf("schema drift: %d operation(s) pending", len(plan.Ops))
	}
	return nil
}

// planFromStaticModels builds the desired schema from statically read models
// and diffs it against the live one the way PlanMigration does for compiled
// models: the catalog objects the models cannot declare are carried over so
// the plan never proposes dropping what the tags are silent about.
func planFromStaticModels(ctx context.Context, client *quark.Client, models []ddlModel) (quark.Plan, error) {
	desired, err := desiredSchemaFromModels(models, client.Dialect().Name())
	if err != nil {
		return quark.Plan{}, err
	}
	current, err := client.IntrospectSchema(ctx)
	if err != nil {
		return quark.Plan{}, fmt.Errorf("introspect the database: %w", err)
	}
	carryOverUndeclared(&desired, current)
	return quark.Plan{Ops: quark.Diff(desired, current)}, nil
}

// desiredSchemaFromModels maps the static models to a quark.Schema with the
// runtime's type mapping, so the plan matches what client.Migrate would have
// created for the same structs.
func desiredSchemaFromModels(models []ddlModel, dialect string) (quark.Schema, error) {
	var tables []quark.Table
	for _, m := range models {
		var t quark.Table
		t.Name = m.Table
		pkCount := 0
		for _, f := range m.Fields {
			if f.IsPK {
				pkCount++
			}
		}
		for _, f := range m.Fields {
			col, err := desiredColumn(f, dialect, pkCount == 1)
			if err != nil {
				return quark.Schema{}, fmt.Errorf("model %s, column %s: %w", m.Name, f.Column, err)
			}
			t.Columns = append(t.Columns, col)
			if f.IndexName != "" {
				t.Indexes = append(t.Indexes, quark.Index{Name: f.IndexName, Columns: []string{f.Column}})
			}
			if f.Check != "" {
				t.Checks = append(t.Checks, quark.Check{Name: "ck_" + m.Table + "_" + f.Column, Expression: f.Check})
			}
		}
		var fkCols []string
		for col := range m.FKs {
			fkCols = append(fkCols, col)
		}
		sort.Strings(fkCols)
		for _, col := range fkCols {
			t.ForeignKeys = append(t.ForeignKeys, quark.ForeignKey{
				Name: "fk_" + m.Table + "_" + col, Columns: []string{col}, RefTable: m.FKs[col], RefColumns: []string{"id"},
			})
		}
		tables = append(tables, t)
	}
	return quark.Schema{Tables: tables}, nil
}

func desiredColumn(f ddlField, dialect string, singlePK bool) (quark.Column, error) {
	goType := f.GoType
	nullable := false
	if strings.HasPrefix(goType, "*") {
		goType, nullable = goType[1:], true
	}
	if inner, ok := strings.CutPrefix(goType, "quark.Nullable["); ok {
		goType, nullable = strings.TrimSuffix(inner, "]"), true
	}
	if strings.HasPrefix(goType, "quark.JSON[") || strings.HasPrefix(goType, "quark.Array[") || goType == "json.RawMessage" {
		goType = "json.RawMessage"
	}
	rt, ok := staticGoType(goType)
	if !ok {
		return quark.Column{}, fmt.Errorf("unsupported Go type %q for a static plan — supported: bool, ints, floats, string, time.Time, json.RawMessage, quark.Nullable/Array/JSON, pointers", f.GoType)
	}
	sqlType := migrate.SQLTypeWithOpts(dialect, rt, migrate.TypeOptions{Size: f.Size, Precision: f.Precision, Scale: f.Scale})
	if f.IsPK && singlePK {
		if bare, ok := migrate.PKBareColumnType(dialect, rt); ok {
			sqlType = bare
		}
	}
	col := quark.Column{
		Name:       f.Column,
		Type:       sqlType,
		Nullable:   !f.NotNull && !f.IsPK && !f.IsVersion,
		PrimaryKey: f.IsPK,
	}
	if nullable && !f.IsPK {
		col.Nullable = true
	}
	switch {
	case f.IsVersion:
		zero := "0"
		col.Default = &zero
	case f.Default != "":
		def := f.Default
		if migrate.IsBoolColumn(rt) {
			def = migrate.NormalizeBoolDefault(dialect, def)
		}
		col.Default = &def
	}
	return col, nil
}

// carryOverUndeclared copies the live indexes, foreign keys and checks the
// models do not declare into the desired schema — PlanMigration's rule, so a
// static plan never proposes dropping a catalog object the tags cannot name.
func carryOverUndeclared(desired *quark.Schema, current quark.Schema) {
	live := map[string]quark.Table{}
	for _, t := range current.Tables {
		live[t.Name] = t
	}
	for i := range desired.Tables {
		dt := &desired.Tables[i]
		ct, ok := live[dt.Name]
		if !ok {
			continue
		}
		declaredIdx := map[string]bool{}
		for _, idx := range dt.Indexes {
			declaredIdx[idx.Name] = true
		}
		for _, idx := range ct.Indexes {
			if !declaredIdx[idx.Name] {
				dt.Indexes = append(dt.Indexes, idx)
			}
		}
		declaredFK := map[string]bool{}
		for _, fk := range dt.ForeignKeys {
			declaredFK[fkKey(fk)] = true
		}
		for _, fk := range ct.ForeignKeys {
			if !declaredFK[fkKey(fk)] {
				dt.ForeignKeys = append(dt.ForeignKeys, fk)
			}
		}
		if ct.Checks != nil {
			declaredChk := map[string]bool{}
			for _, c := range dt.Checks {
				declaredChk[c.Name] = true
			}
			if dt.Checks == nil {
				dt.Checks = []quark.Check{}
			}
			for _, c := range ct.Checks {
				if !declaredChk[c.Name] {
					dt.Checks = append(dt.Checks, c)
				}
			}
		}
	}
}

func fkKey(fk quark.ForeignKey) string {
	return strings.Join(fk.Columns, ",") + "->" + fk.RefTable + "(" + strings.Join(fk.RefColumns, ",") + ")"
}
