// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Regression tests for the P0 items of the 2026-08-16 DX audit that live in
// the quark CLI:
//
//   - DX-6: every CLI command used to print the partial-literal Limits WARN
//     ("Sync may drop columns and tables") because the CLI's own connection
//     helpers built Limits from a partial literal — the first command after
//     `quark init` warned the user about a risk caused by the CLI itself.
//   - DX-7: no subcommand carried a cobra Example. The examples already
//     exist in the CLI guide; the terminal is where they are needed.
package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// DX-6: a plain status command on a fresh sqlite project must not emit the
// partial-literal warning on stderr.
func TestCLICommandsDoNotWarnAboutTheirOwnLimits(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "fresh.db")
	for _, kv := range [][2]string{
		{"database.default.driver", "sqlite"},
		{"database.default.dsn", dsn},
	} {
		key, old := kv[0], viper.GetString(kv[0])
		viper.Set(key, kv[1])
		t.Cleanup(func() { viper.Set(key, old) })
	}

	oldStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w

	statusErr := runMigrateStatus()

	w.Close()
	os.Stderr = oldStderr
	buf := make([]byte, 64*1024)
	n, _ := r.Read(buf)
	stderr := string(buf[:n])

	if statusErr != nil {
		t.Fatalf("migrate status on a fresh project: %v", statusErr)
	}
	if strings.Contains(stderr, "partial literal") || strings.Contains(stderr, "SafeMigrations") {
		t.Errorf("the CLI warns about its own Limits wiring:\n%s", stderr)
	}
}

// DX-7: every executable subcommand must ship a cobra Example.
func TestEveryExecutableSubcommandHasAnExample(t *testing.T) {
	var missing []string
	for _, cmd := range rootCmd.Commands() {
		for _, sub := range cmd.Commands() {
			if sub.Runnable() && strings.TrimSpace(sub.Example) == "" {
				missing = append(missing, cmd.Name()+" "+sub.Name())
			}
		}
		if cmd.Runnable() && len(cmd.Commands()) == 0 && strings.TrimSpace(cmd.Example) == "" {
			missing = append(missing, cmd.Name())
		}
	}
	if len(missing) > 0 {
		t.Errorf("executable subcommands without an Example: %v", missing)
	}
}
