// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/jcsvwinston/quark/internal/db"
	"github.com/jcsvwinston/quark/internal/migrate"
)

// SyncOptions configures the behavior of the Sync operation.
type SyncOptions struct {
	DryRun        bool // If true, logs the SQL but doesn't execute it.
	NoTransaction bool // If true, doesn't wrap the sync in a transaction.
}

// Sync synchronizes the database schema with the provided models.
// It detects missing columns, renames, and can drop columns if safe mode is disabled.
func (c *Client) Sync(ctx context.Context, opts SyncOptions, models ...any) error {
	// Execute within a transaction if supported and not disabled
	if !opts.NoTransaction && c.dialect.SupportsTransactionalDDL() {
		return c.Tx(ctx, func(tx *Tx) error {
			// Temporarily bind the client to the transaction's executor
			// Note: This is an internal sync, we don't need to swap the whole client,
			// just ensure syncModel uses the transaction's executor.
			for _, model := range models {
				if err := c.syncModel(ctx, model, opts, tx.tx); err != nil {
					return err
				}
			}
			return nil
		})
	}

	for _, model := range models {
		if err := c.syncModel(ctx, model, opts, c.db); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) syncModel(ctx context.Context, model any, opts SyncOptions, executor Executor) error {
	v := reflect.TypeOf(model)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}

	meta := GetModelMetaByType(v)
	if meta == nil {
		return fmt.Errorf("could not get metadata for model type: %v", v)
	}

	// 1. Ensure table exists
	if !opts.DryRun {
		if err := c.Migrate(ctx, model); err != nil {
			return err
		}
	}

	// 2. Get current DB info (Introspection)
	info, err := db.GetTableInfo(c.db, c.dialect.Name(), meta.Table)
	if err != nil {
		return fmt.Errorf("introspection failed for %s: %w", meta.Table, err)
	}

	currentCols := make(map[string]db.ColumnInfo)
	for _, col := range info.Columns {
		currentCols[strings.ToLower(col.Name)] = col
	}

	// 3. Sync columns (Add / Rename)
	for _, field := range meta.Fields {
		if field.Column == "" {
			continue
		}

		colNameLower := strings.ToLower(field.Column)
		if _, ok := currentCols[colNameLower]; !ok {
			// Column missing in DB. Check if it's a rename.
			if field.OldColumn != "" {
				oldColLower := strings.ToLower(field.OldColumn)
				if _, ok := currentCols[oldColLower]; ok {
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
					currentCols[colNameLower] = db.ColumnInfo{Name: field.Column}
					continue
				}
			}

			// Not a rename, just add it.
			sqlType := migrate.SQLTypeWithOpts(c.dialect.Name(), field.Type, migrate.TypeOptions{
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
		}
	}

	// 4. Find columns to drop (only if NOT in safe mode)
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
			}
		}
	}

	return nil
}
