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

// SyncOptions configures the behavior of the Sync operation.
type SyncOptions struct {
	DryRun        bool // If true, logs the SQL but doesn't execute it.
	NoTransaction bool // If true, doesn't wrap the sync in a transaction.
}

// Sync synchronizes the database schema with the provided models.
// It detects missing columns, renames, and can drop columns if safe mode is disabled.
//
// Sync reads the current columns through the dialect's [SchemaIntrospector]
// and writes column types through its quarkdriver.ColumnTyper, like
// [Client.Migrate]; a dialect without a SchemaIntrospector cannot be synced
// and Sync returns ErrUnsupportedFeature.
func (c *Client) Sync(ctx context.Context, opts SyncOptions, models ...any) error {
	// Execute within a transaction if supported and not disabled
	if !opts.NoTransaction && c.dialect.SupportsTransactionalDDL() {
		return c.Tx(ctx, func(tx *Tx) error {
			// syncModels runs the column changes on the transaction's
			// executor; the client itself is not rebound.
			return c.syncModels(ctx, opts, tx.tx, models)
		})
	}
	return c.syncModels(ctx, opts, c.db, models)
}

// syncModels creates the models' missing tables, reads the schema once, and
// brings each model's columns in line with it.
func (c *Client) syncModels(ctx context.Context, opts SyncOptions, executor Executor, models []any) error {
	metas := make([]*ModelMeta, 0, len(models))
	for _, model := range models {
		v := reflect.TypeOf(model)
		if v == nil {
			return fmt.Errorf("could not get metadata for model type: %v", v)
		}
		if v.Kind() == reflect.Ptr {
			v = v.Elem()
		}
		meta := GetModelMetaByType(v)
		if meta == nil {
			return fmt.Errorf("could not get metadata for model type: %v", v)
		}
		metas = append(metas, meta)
	}

	// 1. Ensure the tables exist.
	if !opts.DryRun {
		for _, model := range models {
			if err := c.Migrate(ctx, model); err != nil {
				return err
			}
		}
	}

	// 2. Read the current columns, once, through the dialect — never a
	// catalog query chosen by the dialect's name (A11 Q2).
	introspector, ok := c.dialect.(SchemaIntrospector)
	if !ok {
		return fmt.Errorf("%w: Sync reads the current columns through the dialect's SchemaIntrospector, and dialect %s does not implement it",
			ErrUnsupportedFeature, c.dialect.Name())
	}
	schema, err := introspector.IntrospectSchema(ctx, c.db)
	if err != nil {
		return fmt.Errorf("introspection failed: %w", err)
	}
	current := make(map[string]map[string]bool, len(schema.Tables))
	for _, t := range schema.Tables {
		cols := make(map[string]bool, len(t.Columns))
		for _, col := range t.Columns {
			cols[strings.ToLower(col.Name)] = true
		}
		current[strings.ToLower(t.Name)] = cols
	}

	// 3. Each model against what the table has. The map is updated as
	// columns are added, renamed and dropped, so two models of one table
	// see each other's changes.
	for _, meta := range metas {
		key := strings.ToLower(meta.Table)
		if current[key] == nil {
			current[key] = map[string]bool{}
		}
		if err := c.syncModel(ctx, meta, opts, executor, current[key]); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) syncModel(ctx context.Context, meta *ModelMeta, opts SyncOptions, executor Executor, currentCols map[string]bool) error {
	types := c.schemaTypes()

	// Sync columns (Add / Rename)
	for _, field := range meta.Fields {
		if field.Column == "" {
			continue
		}

		colNameLower := strings.ToLower(field.Column)
		if !currentCols[colNameLower] {
			// Column missing in DB. Check if it's a rename.
			if field.OldColumn != "" {
				oldColLower := strings.ToLower(field.OldColumn)
				if currentCols[oldColLower] {
					// Rename it!
					sqlStr := c.dialect.RenameColumn(meta.Table, field.OldColumn, field.Column)
					if opts.DryRun {
						c.logger.Info("sync dry-run: rename column", "table", meta.Table, "sql", sqlStr)
						continue
					}
					c.logger.Info("sync: renaming column", "table", meta.Table, "old", field.OldColumn, "new", field.Column)
					if _, err := executor.ExecContext(ctx, sqlStr); err != nil {
						return fmt.Errorf("failed to rename column %s to %s: %w", field.OldColumn, field.Column, err)
					}
					delete(currentCols, oldColLower)
					currentCols[colNameLower] = true
					continue
				}
			}

			// Not a rename, just add it.
			sqlType := migrate.ColumnSQL(types, field.Type, migrate.TypeOptions{
				Size:      field.Size,
				Precision: field.Precision,
				Scale:     field.Scale,
				IsPK:      field.IsPK,
			})
			sqlStr := c.dialect.AlterTableAddColumn(meta.Table, field.Column, sqlType)
			if opts.DryRun {
				c.logger.Info("sync dry-run: add column", "table", meta.Table, "sql", sqlStr)
				continue
			}
			c.logger.Info("sync: adding column", "table", meta.Table, "column", field.Column, "type", sqlType)
			if _, err := executor.ExecContext(ctx, sqlStr); err != nil {
				return fmt.Errorf("failed to add column %s: %w", field.Column, err)
			}
			currentCols[colNameLower] = true
		}
	}

	// Find columns to drop (only if NOT in safe mode)
	if !c.limits.SafeMigrations {
		for colName := range currentCols {
			found := false
			for _, field := range meta.Fields {
				if strings.ToLower(field.Column) == colName {
					found = true
					break
				}
			}
			if !found {
				sqlStr := c.dialect.AlterTableDropColumn(meta.Table, colName)
				if opts.DryRun {
					c.logger.Info("sync dry-run: drop column", "table", meta.Table, "sql", sqlStr)
					continue
				}
				c.logger.Warn("sync: dropping column (destructive)", "table", meta.Table, "column", colName)
				if _, err := executor.ExecContext(ctx, sqlStr); err != nil {
					return fmt.Errorf("failed to drop column %s: %w", colName, err)
				}
				delete(currentCols, colName)
			}
		}
	}

	return nil
}
