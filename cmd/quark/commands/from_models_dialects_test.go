// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package commands

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

// TestBuildDDLStatementsWritesEachEngineItsOwnIdempotence is QK-101: the
// DDL `--from-models` renders must apply on the engine it names. It used to
// write CREATE TABLE IF NOT EXISTS and CREATE INDEX IF NOT EXISTS for every
// dialect, which SQL Server rejects outright, Oracle before 23ai too, and
// MySQL on an index. Now each statement is the dialect's own, through the
// same quarkdriver.IdempotentDDL the runtime migrator uses.
func TestBuildDDLStatementsWritesEachEngineItsOwnIdempotence(t *testing.T) {
	models := []ddlModel{
		{Name: "User", Table: "users", Fields: []ddlField{
			{Column: "id", GoType: "uint", IsPK: true},
			{Column: "email", GoType: "string", Size: 190, Unique: true, IndexName: "idx_users_email"},
		}},
		{Name: "Article", Table: "articles", Fields: []ddlField{
			{Column: "id", GoType: "uint", IsPK: true},
			{Column: "author_id", GoType: "uint"},
		}, FKs: map[string]string{"author_id": "users"}},
	}
	cases := map[string]struct {
		up, down []string
		absent   []string
	}{
		"postgres": {
			up: []string{
				`CREATE TABLE IF NOT EXISTS "users" (` + "\n" + `  "id" BIGSERIAL PRIMARY KEY,`,
				`CREATE INDEX IF NOT EXISTS "idx_users_email" ON "users" ("email")`,
				`CREATE TABLE IF NOT EXISTS "articles" (`,
				`FOREIGN KEY ("author_id") REFERENCES "users"("id")`,
				`CREATE INDEX IF NOT EXISTS "idx_articles_author_id" ON "articles" ("author_id")`,
			},
			down: []string{`DROP TABLE IF EXISTS "articles"`, `DROP TABLE IF EXISTS "users"`},
		},
		"sqlite": {
			up:   []string{`CREATE TABLE IF NOT EXISTS "users" (`, `CREATE INDEX IF NOT EXISTS "idx_users_email" ON "users" ("email")`},
			down: []string{`DROP TABLE IF EXISTS "users"`},
		},
		"mysql": {
			// MySQL has IF NOT EXISTS on CREATE TABLE and not on CREATE INDEX.
			up:     []string{"CREATE TABLE IF NOT EXISTS `users` (", "CREATE INDEX `idx_users_email` ON `users` (`email`)"},
			down:   []string{"DROP TABLE IF EXISTS `users`"},
			absent: []string{"CREATE INDEX IF NOT EXISTS"},
		},
		"mssql": {
			// SQL Server has no IF NOT EXISTS: a guard on sys.tables / sys.indexes.
			up: []string{
				`IF NOT EXISTS (SELECT * FROM sys.tables WHERE name = 'users') CREATE TABLE [users] (`,
				`IF NOT EXISTS (SELECT name FROM sys.indexes WHERE name = 'idx_users_email') CREATE INDEX [idx_users_email] ON [users] ([email])`,
			},
			down:   []string{`DROP TABLE IF EXISTS [users]`},
			absent: []string{"CREATE TABLE IF NOT EXISTS", "CREATE INDEX IF NOT EXISTS"},
		},
		"oracle": {
			// Oracle has IF NOT EXISTS / IF EXISTS only since 23ai: plain
			// CREATE TABLE and DROP TABLE (the runtime counts ORA-00955 as
			// success; a migration applies once).
			// Oracle's Quote upper-cases, as the runtime's does.
			up:     []string{`CREATE TABLE "USERS" (`, `CREATE INDEX IF NOT EXISTS "IDX_USERS_EMAIL" ON "USERS" ("EMAIL")`},
			down:   []string{`DROP TABLE "ARTICLES"`, `DROP TABLE "USERS"`},
			absent: []string{"CREATE TABLE IF NOT EXISTS", "DROP TABLE IF EXISTS"},
		},
	}
	for dialect, want := range cases {
		t.Run(dialect, func(t *testing.T) {
			up, down, err := buildDDLStatements(models, dialect)
			if err != nil {
				t.Fatal(err)
			}
			upJoined, downJoined := strings.Join(up, "\n"), strings.Join(down, "\n")
			for _, w := range want.up {
				if !strings.Contains(upJoined, w) {
					t.Errorf("up lacks %q:\n%s", w, upJoined)
				}
			}
			for _, w := range want.down {
				if !strings.Contains(downJoined, w) {
					t.Errorf("down lacks %q:\n%s", w, downJoined)
				}
			}
			for _, a := range want.absent {
				if strings.Contains(upJoined+"\n"+downJoined, a) {
					t.Errorf("%s must not carry %q:\n%s\n%s", dialect, a, upJoined, downJoined)
				}
			}
			// Parent before child, child dropped first.
			lowerUp, lowerDown := strings.ToLower(upJoined), strings.ToLower(downJoined)
			if strings.Index(lowerUp, "users") > strings.Index(lowerUp, "articles") {
				t.Errorf("users must be created before articles:\n%s", upJoined)
			}
			if strings.Index(lowerDown, "articles") > strings.Index(lowerDown, "users") {
				t.Errorf("articles must be dropped before users:\n%s", downJoined)
			}
		})
	}
}

func TestBuildDDLStatementsRejectsAnUnknownDialect(t *testing.T) {
	if _, _, err := buildDDLStatements(nil, "db2"); err == nil || !strings.Contains(err.Error(), "db2") {
		t.Fatalf("expected the dialect to be named, got %v", err)
	}
}

// TestRenderExecBodyKeepsMySQLBackticksCompilable: MySQL quotes identifiers
// with backticks, which a Go raw string literal cannot hold; the rendered
// Exec goes in an interpreted literal and the migration file still parses.
func TestRenderExecBodyKeepsMySQLBackticksCompilable(t *testing.T) {
	up, down, err := buildDDLStatements([]ddlModel{{Name: "User", Table: "users", Fields: []ddlField{{Column: "id", GoType: "uint", IsPK: true}}}}, "mysql")
	if err != nil {
		t.Fatal(err)
	}
	body := renderExecBody(append(up, down...))
	if !strings.Contains(body, `client.Exec(ctx, "CREATE TABLE IF NOT EXISTS `+"`users`") {
		t.Errorf("a statement with backticks must be an interpreted literal:\n%s", body)
	}
	src := "package migrations\n\nfunc up(ctx int, client interface{ Exec(int, string) error }) error {\n" + body + "\n\treturn nil\n}\n"
	if _, err := parser.ParseFile(token.NewFileSet(), "m.go", src, parser.AllErrors); err != nil {
		t.Fatalf("the rendered body does not parse: %v\n%s", err, src)
	}
}
