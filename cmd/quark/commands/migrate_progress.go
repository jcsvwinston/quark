// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/migrate"
)

// newCLIMigrator binds the migrator to the client with the CLI's own
// progress printer: the library reports through a logger (QK-6), and the
// person running `quark migrate up` still reads the plain sentences on
// stdout the command always printed.
func newCLIMigrator(client *quark.Client) *migrate.Migrator {
	return migrate.NewMigrator(client, migrate.WithLogger(slog.New(&migrateProgress{w: os.Stdout})))
}

// migrateProgress renders the migrator's log records as the sentences the
// CLI printed before the library stopped writing to stdout. Records it
// does not know go out as "message key=value".
type migrateProgress struct {
	w     io.Writer
	attrs []slog.Attr
}

func (h *migrateProgress) Enabled(context.Context, slog.Level) bool { return true }

func (h *migrateProgress) Handle(_ context.Context, r slog.Record) error {
	kv := map[string]any{}
	for _, a := range h.attrs {
		kv[a.Key] = a.Value.Any()
	}
	r.Attrs(func(a slog.Attr) bool { kv[a.Key] = a.Value.Any(); return true })
	var line string
	switch r.Message {
	case "migrate: applying":
		line = fmt.Sprintf("Applying migration: %v (%v)...", kv["id"], kv["name"])
	case "migrate: applied":
		line = fmt.Sprintf("Applied %v migrations.", kv["count"])
	case "migrate: no pending migrations":
		line = "No pending migrations."
	case "migrate: reverting":
		line = fmt.Sprintf("Reverting migration: %v (%v)...", kv["id"], kv["name"])
	case "migrate: reverted":
		line = fmt.Sprintf("Reverted %v migrations.", kv["count"])
	case "migrate: no migrations to revert":
		line = "No migrations to revert."
	default:
		if r.Level < slog.LevelInfo {
			return nil // the library's debug notes are not CLI output
		}
		line = r.Message
		for k, v := range kv {
			line += fmt.Sprintf(" %s=%v", k, v)
		}
	}
	_, err := fmt.Fprintln(h.w, line)
	return err
}

func (h *migrateProgress) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &migrateProgress{w: h.w, attrs: append(append([]slog.Attr{}, h.attrs...), attrs...)}
}

func (h *migrateProgress) WithGroup(string) slog.Handler { return h }
