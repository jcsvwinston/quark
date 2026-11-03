// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"strings"
	"testing"
)

// A4/S6, NU-50: Quark and Nucleus's pkg/model both read a tag called `db` and
// disagree about its grammar. Nucleus separates directives with semicolons
// and puts the column name in one of them (`db:"column:email;unique"`);
// Quark reads the first comma-separated element as the column NAME.
//
// Before this, a model written the Nucleus way produced a Quark column
// literally called `column:email;unique;not null` — no error, no warning, and
// a table nobody could query. These pin both halves of the fix: refuse what
// cannot be a column name, warn about what can.

type tgNucleusStyle struct {
	ID    uint   `db:"pk"`
	Email string `db:"column:email;unique;not null"`
}

func TestForeignTagGrammarIsRejected(t *testing.T) {
	ctx := context.Background()
	c, err := New("sqlite", "file:taggrammar1?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	err = c.RegisterModel(&tgNucleusStyle{})
	if !errors.Is(err, ErrInvalidTag) {
		t.Fatalf("RegisterModel err = %v, want ErrInvalidTag", err)
	}
	// The message has to teach the fix, not just refuse: someone hitting
	// this has a model written for the other layer and needs to know how it
	// is spelled here.
	for _, want := range []string{"column name", "Nucleus", `db:"email"`, `pk:"true"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message is missing %q:\n%s", want, err)
		}
	}

	// Migrate has to refuse it too, or the DDL goes out anyway.
	if err := c.Migrate(ctx, &tgNucleusStyle{}); !errors.Is(err, ErrInvalidTag) {
		t.Errorf("Migrate err = %v, want ErrInvalidTag", err)
	}
}

type tgDirectiveAsColumn struct {
	ID   int64  `db:"id" pk:"true"`
	Key  string `db:"pk"`
	Flag string `db:"readonly"`
}

// QK-76. A db tag whose column name is exactly a Nucleus pkg/model directive
// — `db:"pk"`, `db:"readonly"` — is a legal column name, so v1 warns and
// migrates a column with that name. Quark 2.0 refuses it with ErrInvalidTag
// unless the field carries quark:"literal_column" (DEP-2026-011); this test
// pins the v1 behaviour so the change at the major is a diff to it.
func TestDirectiveShapedColumnNameWarnsAndMigratesInV1(t *testing.T) {
	ctx := context.Background()
	meta := GetModelMeta[tgDirectiveAsColumn]()
	if meta.TagError != nil {
		t.Fatalf("v1: a directive-shaped column name must not be an error yet: %v", meta.TagError)
	}
	if len(meta.TagWarnings) != 2 {
		t.Fatalf("got %d warnings, want one per directive-shaped field: %v", len(meta.TagWarnings), meta.TagWarnings)
	}
	for i, col := range []string{"pk", "readonly"} {
		w := meta.TagWarnings[i]
		// The warning names the column, the other grammar, the way to keep
		// the column and the notice that announces the error.
		for _, want := range []string{`"` + col + `"`, "Nucleus", `quark:"literal_column"`, "DEP-2026-011"} {
			if !strings.Contains(w, want) {
				t.Errorf("warning for %s is missing %q:\n%s", col, want, w)
			}
		}
	}

	var logs bytes.Buffer
	c, err := New("sqlite", "file:qk76_directive?mode=memory&cache=shared",
		WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	logs.Reset() // what New logs about the driver is not this test's business

	// RegisterModel and Migrate accept the model and log the warning.
	if err := c.RegisterModel(&tgDirectiveAsColumn{}); err != nil {
		t.Fatalf("v1: RegisterModel = %v, want nil (a warning, not ErrInvalidTag)", err)
	}
	if err := c.Migrate(ctx, &tgDirectiveAsColumn{}); err != nil {
		t.Fatalf("v1: Migrate = %v, want nil (a warning, not ErrInvalidTag)", err)
	}
	out := logs.String()
	if strings.Count(out, "level=WARN") != 4 || strings.Count(out, "DEP-2026-011") != 4 ||
		!strings.Contains(out, "struct tag looks like the Nucleus pkg/model grammar") {
		t.Fatalf("v1: RegisterModel and Migrate should each log one WARN per field naming DEP-2026-011; log:\n%s", out)
	}

	// And the table has columns literally called pk and readonly.
	tb := tableNamed(t, c, meta.Table)
	columnNamed(t, tb, "pk")
	columnNamed(t, tb, "readonly")
	if err := For[tgDirectiveAsColumn](ctx, c).Create(&tgDirectiveAsColumn{ID: 1, Key: "k1", Flag: "yes"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := For[tgDirectiveAsColumn](ctx, c).Where("pk", "=", "k1").First()
	if err != nil {
		t.Fatalf("read back by the pk column: %v", err)
	}
	if got.Flag != "yes" {
		t.Fatalf("readonly column = %q, want yes", got.Flag)
	}
}

type tgLiteralColumn struct {
	ID   int64  `db:"id" pk:"true"`
	Key  string `db:"pk" quark:"literal_column"`
	Flag string `db:"readonly,size=20" quark:"not_null,literal_column"`
}

// The way out of DEP-2026-011, in v1 and after: quark:"literal_column" says
// the db tag is the column's real name. Same columns as the bare tags above,
// and no warning — so nothing to turn into an error at the major.
func TestLiteralColumnKeepsADirectiveNamedColumnWithoutWarning(t *testing.T) {
	ctx := context.Background()
	meta := GetModelMeta[tgLiteralColumn]()
	if meta.TagError != nil {
		t.Fatalf("quark:\"literal_column\" must be a valid token: %v", meta.TagError)
	}
	if len(meta.TagWarnings) != 0 {
		t.Fatalf("quark:\"literal_column\" must silence the directive warning: %v", meta.TagWarnings)
	}
	if got := meta.Fields[1].Column; got != "pk" {
		t.Errorf("column = %q, want pk", got)
	}
	if got := meta.Fields[2].Column; got != "readonly" {
		t.Errorf("column = %q, want readonly", got)
	}

	var logs bytes.Buffer
	c, err := New("sqlite", "file:qk76_literal?mode=memory&cache=shared",
		WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	logs.Reset()
	if err := c.RegisterModel(&tgLiteralColumn{}); err != nil {
		t.Fatalf("RegisterModel: %v", err)
	}
	if err := c.Migrate(ctx, &tgLiteralColumn{}); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if strings.Contains(logs.String(), "DEP-2026-011") || strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("quark:\"literal_column\" must log nothing; log:\n%s", logs.String())
	}

	// The same columns as the bare tags, and the other tokens still apply.
	tb := tableNamed(t, c, meta.Table)
	columnNamed(t, tb, "pk")
	if col := columnNamed(t, tb, "readonly"); col.Nullable {
		t.Errorf("readonly is nullable; quark:\"not_null\" beside literal_column was lost")
	}
	if err := For[tgLiteralColumn](ctx, c).Create(&tgLiteralColumn{ID: 1, Key: "k1", Flag: "yes"}); err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := For[tgLiteralColumn](ctx, c).Where("pk", "=", "k1").First()
	if err != nil {
		t.Fatalf("read back by the pk column: %v", err)
	}
	if got.Flag != "yes" {
		t.Fatalf("readonly column = %q, want yes", got.Flag)
	}
}

type tgQuarkStyle struct {
	ID    int64  `db:"id" pk:"true"`
	Email string `db:"email,size=255" quark:"unique,not_null"`
	Notes string `db:"notes"`
}

func TestQuarkGrammarIsUnaffected(t *testing.T) {
	// The check must not fire on the grammar it is defending.
	meta := GetModelMeta[tgQuarkStyle]()
	if meta.TagError != nil {
		t.Errorf("valid Quark tags must not error: %v", meta.TagError)
	}
	if len(meta.TagWarnings) != 0 {
		t.Errorf("valid Quark tags must not warn: %v", meta.TagWarnings)
	}
	if got := meta.Fields[1].Column; got != "email" {
		t.Errorf("column = %q, want email", got)
	}
}

func TestColumnNamesWithStrayCharactersAreRejected(t *testing.T) {
	// The rule is about column names, not about Nucleus: anything that
	// cannot be an identifier is caught whatever grammar produced it.
	// Quoted identifiers make almost anything legal to the engine, which is
	// why this has to be caught before the DDL.
	type strays struct {
		ID  int64  `db:"id" pk:"true"`
		Bad string `db:"user name"`
	}
	meta := GetModelMetaByType(reflect.TypeOf(strays{}))
	if meta.TagError == nil {
		t.Fatal("a column name with a space must be rejected")
	}
	if !strings.Contains(meta.TagError.Error(), `" "`) {
		t.Errorf("the message should name the offending character:\n%s", meta.TagError)
	}
}

// Quark's own audit table has a column really called pk. Its model carries
// quark:"literal_column", so EnableAuditLog — which migrates it on every
// call — logs no tag warning in v1 and stays valid when v2.0.0 makes the
// bare directive ErrInvalidTag (DEP-2026-011). Without the token it warned
// about itself since v1.14.0.
func TestEnableAuditLogDoesNotWarnAboutItsOwnPKColumn(t *testing.T) {
	if w := GetModelMeta[quarkAuditRow]().TagWarnings; len(w) != 0 {
		t.Fatalf("quarkAuditRow carries tag warnings: %v", w)
	}
	var logs bytes.Buffer
	c, err := New("sqlite", "file:qk76_audit?mode=memory&cache=shared",
		WithLogger(slog.New(slog.NewTextHandler(&logs, nil))))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	logs.Reset()
	if err := c.EnableAuditLog(context.Background(), AuditConfig{}); err != nil {
		t.Fatalf("EnableAuditLog: %v", err)
	}
	if strings.Contains(logs.String(), "DEP-2026-011") {
		t.Fatalf("EnableAuditLog warned about its own pk column; log:\n%s", logs.String())
	}
	// The column is still called pk.
	if _, err := For[quarkAuditRow](context.Background(), c).Where("pk", "=", "1").Count(); err != nil {
		t.Fatalf("query quark_audit by its pk column: %v", err)
	}
}
