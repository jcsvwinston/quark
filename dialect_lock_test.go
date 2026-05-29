package quark_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
)

// TestLockSuffix_PerDialect pins the SQL fragments each dialect emits for
// each LockOptions shape. Runs locally (no database required) so the
// matrix is exercised even when the dialect-specific test suites aren't
// runnable (testcontainers / DSN env vars not configured).
func TestLockSuffix_PerDialect(t *testing.T) {
	cases := []struct {
		name       string
		opts       quark.LockOptions
		dialect    quark.Dialect
		wantHint   string
		wantSuffix string
		wantErr    bool
		wantErrIs  error
	}{
		// Postgres
		{"pg/none", quark.LockOptions{}, quark.PostgreSQL(), "", "", false, nil},
		{"pg/for-update", quark.LockOptions{Mode: quark.LockForUpdate}, quark.PostgreSQL(), "", " FOR UPDATE", false, nil},
		{"pg/for-update+skip", quark.LockOptions{Mode: quark.LockForUpdate, SkipLocked: true}, quark.PostgreSQL(), "", " FOR UPDATE SKIP LOCKED", false, nil},
		{"pg/for-update+nowait", quark.LockOptions{Mode: quark.LockForUpdate, NoWait: true}, quark.PostgreSQL(), "", " FOR UPDATE NOWAIT", false, nil},
		{"pg/for-share", quark.LockOptions{Mode: quark.LockForShare}, quark.PostgreSQL(), "", " FOR SHARE", false, nil},

		// MySQL
		{"mysql/for-update", quark.LockOptions{Mode: quark.LockForUpdate}, quark.MySQL(), "", " FOR UPDATE", false, nil},
		{"mysql/for-update+skip", quark.LockOptions{Mode: quark.LockForUpdate, SkipLocked: true}, quark.MySQL(), "", " FOR UPDATE SKIP LOCKED", false, nil},
		// MySQL 8 keeps FOR SHARE (the BB-3 fix is MariaDB-only).
		{"mysql/for-share", quark.LockOptions{Mode: quark.LockForShare}, quark.MySQL(), "", " FOR SHARE", false, nil},

		// MariaDB
		{"mariadb/for-update", quark.LockOptions{Mode: quark.LockForUpdate}, quark.MariaDB(), "", " FOR UPDATE", false, nil},
		{"mariadb/for-update+nowait", quark.LockOptions{Mode: quark.LockForUpdate, NoWait: true}, quark.MariaDB(), "", " FOR UPDATE NOWAIT", false, nil},
		{"mariadb/for-update+skip", quark.LockOptions{Mode: quark.LockForUpdate, SkipLocked: true}, quark.MariaDB(), "", " FOR UPDATE SKIP LOCKED", false, nil},
		// MariaDB has no FOR SHARE (MySQL-8 syntax); it uses LOCK IN SHARE MODE,
		// which cannot carry SKIP LOCKED / NOWAIT. (BB-3)
		{"mariadb/for-share", quark.LockOptions{Mode: quark.LockForShare}, quark.MariaDB(), "", " LOCK IN SHARE MODE", false, nil},
		{"mariadb/for-share+skip-unsupported", quark.LockOptions{Mode: quark.LockForShare, SkipLocked: true}, quark.MariaDB(), "", "", true, quark.ErrUnsupportedFeature},
		{"mariadb/for-share+nowait-unsupported", quark.LockOptions{Mode: quark.LockForShare, NoWait: true}, quark.MariaDB(), "", "", true, quark.ErrUnsupportedFeature},

		// Oracle
		{"oracle/for-update", quark.LockOptions{Mode: quark.LockForUpdate}, quark.Oracle(), "", " FOR UPDATE", false, nil},
		{"oracle/for-update+skip", quark.LockOptions{Mode: quark.LockForUpdate, SkipLocked: true}, quark.Oracle(), "", " FOR UPDATE SKIP LOCKED", false, nil},
		{"oracle/for-share-unsupported", quark.LockOptions{Mode: quark.LockForShare}, quark.Oracle(), "", "", true, quark.ErrUnsupportedFeature},

		// MSSQL
		{"mssql/for-update", quark.LockOptions{Mode: quark.LockForUpdate}, quark.MSSQL(), " WITH (UPDLOCK, ROWLOCK)", "", false, nil},
		{"mssql/for-update+skip", quark.LockOptions{Mode: quark.LockForUpdate, SkipLocked: true}, quark.MSSQL(), " WITH (UPDLOCK, ROWLOCK, READPAST)", "", false, nil},
		{"mssql/for-share", quark.LockOptions{Mode: quark.LockForShare}, quark.MSSQL(), " WITH (HOLDLOCK, ROWLOCK)", "", false, nil},
		{"mssql/nowait-unsupported", quark.LockOptions{Mode: quark.LockForUpdate, NoWait: true}, quark.MSSQL(), "", "", true, quark.ErrUnsupportedFeature},

		// SQLite — anything non-zero is unsupported.
		{"sqlite/for-update-unsupported", quark.LockOptions{Mode: quark.LockForUpdate}, quark.SQLite(), "", "", true, quark.ErrUnsupportedFeature},
		{"sqlite/none-ok", quark.LockOptions{}, quark.SQLite(), "", "", false, nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hint, suffix, err := tc.dialect.LockSuffix(tc.opts)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got hint=%q suffix=%q", hint, suffix)
				}
				if tc.wantErrIs != nil && !errors.Is(err, tc.wantErrIs) {
					t.Errorf("expected errors.Is(%v); got %v", tc.wantErrIs, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if hint != tc.wantHint {
				t.Errorf("hint mismatch: got %q want %q", hint, tc.wantHint)
			}
			if suffix != tc.wantSuffix {
				t.Errorf("suffix mismatch: got %q want %q", suffix, tc.wantSuffix)
			}
		})
	}
}

// TestLockOptions_IsZero confirms the helper used by every dialect to
// short-circuit the no-lock path.
func TestLockOptions_IsZero(t *testing.T) {
	if !(quark.LockOptions{}).IsZero() {
		t.Error("zero-value LockOptions should be IsZero()")
	}
	if (quark.LockOptions{Mode: quark.LockForUpdate}).IsZero() {
		t.Error("LockForUpdate should not be IsZero()")
	}
	if (quark.LockOptions{SkipLocked: true}).IsZero() {
		t.Error("SkipLocked should not be IsZero()")
	}
	if (quark.LockOptions{NoWait: true}).IsZero() {
		t.Error("NoWait should not be IsZero()")
	}
}

// TestForUpdate_BuildsLockedSelect tracks the integration: chaining
// ForUpdate().SkipLocked() on a Query[T] flows through clone() and
// reaches buildSelect via the dialect, producing a SELECT with the
// expected suffix. We verify against PG syntax via dialect inspection
// because SQLite would error before the SELECT runs (covered by
// testPessimisticLocking in SharedSuite).
func TestForUpdate_BuildsLockedSelect(t *testing.T) {
	d := quark.PostgreSQL()
	hint, suffix, err := d.LockSuffix(quark.LockOptions{Mode: quark.LockForUpdate, SkipLocked: true})
	if err != nil {
		t.Fatalf("LockSuffix: %v", err)
	}
	if hint != "" {
		t.Errorf("expected empty table-hint on PG, got %q", hint)
	}
	if !strings.Contains(suffix, "FOR UPDATE SKIP LOCKED") {
		t.Errorf("PG suffix should contain FOR UPDATE SKIP LOCKED, got %q", suffix)
	}
}
