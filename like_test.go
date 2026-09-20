// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

// QK-25. A wildcard the user typed reached the LIKE as a wildcard, and the
// builder had no way to say otherwise: no surface emitted `ESCAPE`, so on the
// engines with no default escape character (SQLite, SQL Server, Oracle) a
// search for "%" answered every row, and a hand-escaped `\%` searched for a
// backslash. These tests pin the escaped surfaces — builder, typed column,
// AST — by the ROWS they answer on the fixture with one literal percent sign,
// and by the CLAUSE they emit, because for a pattern without wildcards both
// statements return the same rows and only the SQL tells them apart.

type likeRow struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

func (likeRow) TableName() string { return "like_rows" }

func likeClient(t *testing.T, name string) (*Client, *txStatementRecorder) {
	t.Helper()
	rec := &txStatementRecorder{}
	c, err := New("sqlite", "file:"+name+"?mode=memory&cache=shared",
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		WithQueryObserver(rec))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	ctx := context.Background()
	if err := c.Migrate(ctx, &likeRow{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	for _, n := range []string{"alpha", "beta", "100% off", "under_score", "back\\slash", "[bracket]"} {
		row := likeRow{Name: n}
		if err := For[likeRow](ctx, c).Create(&row); err != nil {
			t.Fatalf("seed %q: %v", n, err)
		}
	}
	rec.reset()
	return c, rec
}

func TestEscapeLike(t *testing.T) {
	for in, want := range map[string]string{
		"plain":      "plain",
		"100%":       `100\%`,
		"a_b":        `a\_b`,
		`back\slash`: `back\\slash`,
		"[x]":        `\[x]`,
		"":           "",
	} {
		if got := EscapeLike(in); got != want {
			t.Errorf("EscapeLike(%q) = %q, want %q", in, got, want)
		}
	}
}

// The three user-text searches answer the ONE row that holds the character
// the user typed, on an engine whose LIKE has no default escape at all.
func TestWhereContainsMatchesTheUsersTextLiterally(t *testing.T) {
	c, rec := likeClient(t, "qk25_contains")
	ctx := context.Background()
	cases := []struct {
		name string
		q    *Query[likeRow]
		want string
	}{
		{"contains %", For[likeRow](ctx, c).WhereContains("name", "%"), "100% off"},
		{"contains _", For[likeRow](ctx, c).WhereContains("name", "_"), "under_score"},
		{"contains backslash", For[likeRow](ctx, c).WhereContains("name", `\`), `back\slash`},
		{"contains [", For[likeRow](ctx, c).WhereContains("name", "["), "[bracket]"},
		{"starts with 100%", For[likeRow](ctx, c).WhereStartsWith("name", "100%"), "100% off"},
		{"ends with % off", For[likeRow](ctx, c).WhereEndsWith("name", "% off"), "100% off"},
		{"hand-authored pattern", For[likeRow](ctx, c).WhereLike("name", `%\%%`), "100% off"},
		{"typed Contains", For[likeRow](ctx, c).WhereP(NewTypedStringColumn("name").Contains("%")), "100% off"},
		{"typed StartsWith", For[likeRow](ctx, c).WhereP(NewTypedStringColumn("name").StartsWith("under_")), "under_score"},
		{"typed LikeEscaped", For[likeRow](ctx, c).WhereP(NewTypedStringColumn("name").LikeEscaped(`%\_%`)), "under_score"},
		{"AST Contains", For[likeRow](ctx, c).WhereExpr(Contains(Col("name"), "%")), "100% off"},
		{"AST Like", For[likeRow](ctx, c).WhereExpr(Like(Col("name"), `%\%%`)), "100% off"},
		{"AST EndsWith", For[likeRow](ctx, c).WhereExpr(EndsWith(Col("name"), "]")), "[bracket]"},
	}
	for _, tc := range cases {
		rec.reset()
		rows, err := tc.q.List()
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if len(rows) != 1 || rows[0].Name != tc.want {
			t.Errorf("%s: got %d rows %v, want exactly %q\n  stmt: %s", tc.name, len(rows), rows, tc.want, rec.read())
		}
		if !strings.Contains(rec.read(), `ESCAPE '\'`) {
			t.Errorf("%s: the statement declares no escape character: %s", tc.name, rec.read())
		}
	}
}

// And the negations exclude exactly that row.
func TestWhereNotLikeExcludesTheLiteralMatch(t *testing.T) {
	c, _ := likeClient(t, "qk25_notlike")
	ctx := context.Background()
	for name, q := range map[string]*Query[likeRow]{
		"WhereNotLike":         For[likeRow](ctx, c).WhereNotLike("name", `%\%%`),
		"typed NotLikeEscaped": For[likeRow](ctx, c).WhereP(NewTypedStringColumn("name").NotLikeEscaped(`%\%%`)),
		"AST NotLike":          For[likeRow](ctx, c).WhereExpr(NotLike(Col("name"), `%\%%`)),
	} {
		rows, err := q.List()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(rows) != 5 {
			t.Errorf("%s: got %d rows, want 5 (everything but the literal-percent row)", name, len(rows))
		}
		for _, r := range rows {
			if r.Name == "100% off" {
				t.Errorf("%s: the negation kept the row it should exclude", name)
			}
		}
	}
}

// The plain form is unchanged: Where(col, "LIKE", …) hands the engine an
// opaque pattern with no tail, exactly as it always did. Its meaning is
// published, and making it escape-aware would change what a backslash means
// on three engines — that is A12 material, not this change.
func TestPlainLikeIsUnchanged(t *testing.T) {
	c, rec := likeClient(t, "qk25_plain")
	rows, err := For[likeRow](context.Background(), c).Where("name", "LIKE", "%%%").List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 6 {
		t.Fatalf("plain LIKE with a wildcard pattern answered %d rows, want all 6", len(rows))
	}
	if strings.Contains(strings.ToUpper(rec.read()), "ESCAPE") {
		t.Fatalf("the plain form grew an ESCAPE tail, which changes what a backslash means on SQLite, SQL Server and Oracle: %s", rec.read())
	}
}

// The tail is spelled per engine: MySQL and MariaDB read a backslash inside a
// string literal as an escape, so theirs is doubled; SQLite rejects the
// doubled one as more than one character. Everything else about the clause
// is identical, bind included — the value is escaped the same way everywhere.
func TestLikeEscapeTailIsSpelledPerDialect(t *testing.T) {
	want := map[string]string{
		"sqlite":   `ESCAPE '\'`,
		"postgres": `ESCAPE '\'`,
		"mssql":    `ESCAPE '\'`,
		"oracle":   `ESCAPE '\'`,
		"mysql":    `ESCAPE '\\'`,
		"mariadb":  `ESCAPE '\\'`,
	}
	for _, d := range []Dialect{SQLite(), PostgreSQL(), MSSQL(), Oracle(), MySQL(), MariaDB()} {
		if got := likeEscapeTail(d); got != want[d.Name()] {
			t.Errorf("%s: tail %q, want %q", d.Name(), got, want[d.Name()])
		}
	}
}

// A dangling escape is refused before the engine sees it, on every surface,
// and a pattern that merely CONTAINS escapes — including a doubled one at the
// end — is not dangling.
func TestDanglingEscapeIsRefusedBeforeTheEngine(t *testing.T) {
	c, rec := likeClient(t, "qk25_dangling")
	ctx := context.Background()
	for name, q := range map[string]*Query[likeRow]{
		"WhereLike":    For[likeRow](ctx, c).WhereLike("name", `abc\`),
		"WhereP":       For[likeRow](ctx, c).WhereP(NewTypedStringColumn("name").LikeEscaped(`abc\`)),
		"WhereExpr":    For[likeRow](ctx, c).WhereExpr(Like(Col("name"), `abc\`)),
		"WhereNotLike": For[likeRow](ctx, c).WhereNotLike("name", `\`),
	} {
		rec.reset()
		_, err := q.List()
		if !errors.Is(err, ErrInvalidQuery) {
			t.Errorf("%s: dangling escape returned %v, want ErrInvalidQuery", name, err)
		}
		if rec.count() != 0 {
			t.Errorf("%s: the statement reached the engine: %v", name, rec.all)
		}
	}
	for _, ok := range []string{`abc\\`, `\%\_\\`, `plain`, `%`, ``} {
		if _, err := For[likeRow](ctx, c).WhereLike("name", ok).List(); err != nil {
			t.Errorf("pattern %q was refused: %v", ok, err)
		}
	}
	// Contains never produces a dangling escape, whatever the user typed.
	if _, err := For[likeRow](ctx, c).WhereContains("name", `\`).List(); err != nil {
		t.Errorf("WhereContains with a lone backslash: %v", err)
	}
}

// The clause reaches every renderer, not only the SELECT: an escaped LIKE on
// a DELETE or an UPDATE carries the same tail, so the row it names is the row
// it touches.
func TestEscapedLikeReachesUpdateAndDelete(t *testing.T) {
	c, rec := likeClient(t, "qk25_write_paths")
	ctx := context.Background()

	rec.reset()
	n, err := For[likeRow](ctx, c).WhereLike("name", `%\%%`).UpdateMap(map[string]any{"name": "renamed"})
	if err != nil {
		t.Fatalf("UpdateMap: %v", err)
	}
	if n != 1 || !strings.Contains(rec.read(), `ESCAPE '\'`) {
		t.Fatalf("UpdateMap through an escaped LIKE touched %d rows with %s; want 1 and the tail", n, rec.read())
	}

	rec.reset()
	n, err = For[likeRow](ctx, c).WhereContains("name", "_").DeleteBy()
	if err != nil {
		t.Fatalf("DeleteBy: %v", err)
	}
	if n != 1 || !strings.Contains(rec.read(), `ESCAPE '\'`) {
		t.Fatalf("DeleteBy through an escaped LIKE deleted %d rows with %s; want 1 and the tail", n, rec.read())
	}
	left, _ := For[likeRow](ctx, c).Count()
	if left != 5 {
		t.Fatalf("%d rows left, want 5", left)
	}
}
