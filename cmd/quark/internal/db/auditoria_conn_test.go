// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package db

import (
	"log/slog"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// QC-5: an absent connection config must point at the fix, not just say
// "database configuration missing".
func TestMissingConfigErrorNamesTheRemedy(t *testing.T) {
	t.Cleanup(func() {
		viper.Set("database.default.driver", "")
		viper.Set("database.default.dsn", "")
	})
	viper.Set("database.default.driver", "")
	viper.Set("database.default.dsn", "")

	_, err := GetQuarkClient()
	if err == nil {
		t.Fatal("expected an error when the config is absent")
	}
	for _, want := range []string{"quark init", "QUARK_DATABASE_DEFAULT_DRIVER", "--config"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing-config error does not mention %q:\n%v", want, err)
		}
	}
}

// QC-5: the admin path carries the same actionable shape with its own keys.
func TestMissingAdminConfigErrorNamesAdminKeys(t *testing.T) {
	t.Cleanup(func() {
		viper.Set("database.admin.driver", "")
		viper.Set("database.admin.dsn", "")
	})
	viper.Set("database.admin.driver", "")
	viper.Set("database.admin.dsn", "")

	_, err := GetAdminQuarkClient()
	if err == nil {
		t.Fatal("expected an error when the admin config is absent")
	}
	if !strings.Contains(err.Error(), "database.admin.driver") {
		t.Errorf("admin error does not name the admin keys:\n%v", err)
	}
}

// AQ-16: the CLI logger is quiet by default (WARN) so the per-command
// "quark client initialized" INFO line no longer prints; --debug lowers it to
// INFO.
func TestCLILoggerQuietByDefault(t *testing.T) {
	t.Cleanup(func() { viper.Set("debug", false) })

	viper.Set("debug", false)
	if l := cliLogger(); l.Enabled(nil, slog.LevelInfo) {
		t.Error("default CLI logger emits INFO; the per-command line should be suppressed")
	}
	if l := cliLogger(); !l.Enabled(nil, slog.LevelWarn) {
		t.Error("default CLI logger drops WARN; warnings must still surface")
	}

	viper.Set("debug", true)
	if l := cliLogger(); !l.Enabled(nil, slog.LevelInfo) {
		t.Error("--debug should restore INFO logging")
	}
}
