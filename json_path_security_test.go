package quark_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/jcsvwinston/quark"
)

// jsonPathCapture captures emitted SELECT SQL and args for inspection.
type jsonPathCapture struct {
	mu   sync.Mutex
	stmt []string
	args [][]any
}

func (c *jsonPathCapture) ObserveQuery(e quark.QueryEvent) {
	if e.Operation != "SELECT" {
		return
	}
	c.mu.Lock()
	c.stmt = append(c.stmt, e.SQL)
	captured := make([]any, len(e.Args))
	copy(captured, e.Args)
	c.args = append(c.args, captured)
	c.mu.Unlock()
}

func (c *jsonPathCapture) snapshot() ([]string, [][]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	stmts := make([]string, len(c.stmt))
	copy(stmts, c.stmt)
	argsCopy := make([][]any, len(c.args))
	for i, a := range c.args {
		argsCopy[i] = append([]any(nil), a...)
	}
	return stmts, argsCopy
}

// testJSONPathSecurity is the regression test for P0-2: WhereJSON must validate
// the path and bind it as a parameter rather than interpolating it. Every
// dialect renders a different JSON function but none should ever inline the
// raw path string into the SQL surface.
func testJSONPathSecurity(ctx context.Context, t *testing.T, baseClient *quark.Client) {
	t.Helper()

	type JSONDoc struct {
		ID   int64  `db:"id" pk:"true"`
		Data string `db:"data"`
	}

	dropTable(baseClient, "json_docs")
	if err := baseClient.Migrate(ctx, &JSONDoc{}); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}
	defer dropTable(baseClient, "json_docs")

	cap := &jsonPathCapture{}
	client, err := baseClient.WithOptions(quark.WithQueryObserver(cap))
	if err != nil {
		t.Fatalf("WithOptions failed: %v", err)
	}

	// Seed two rows with valid JSON. The plan field is what we filter on.
	if err := quark.For[JSONDoc](ctx, client).Create(&JSONDoc{Data: `{"plan":"enterprise","user":{"name":"alice"}}`}); err != nil {
		t.Fatalf("seed insert failed: %v", err)
	}
	if err := quark.For[JSONDoc](ctx, client).Create(&JSONDoc{Data: `{"plan":"free","user":{"name":"bob"}}`}); err != nil {
		t.Fatalf("seed insert failed: %v", err)
	}

	t.Run("ValidPathBoundNotInterpolated", func(t *testing.T) {
		cap.mu.Lock()
		cap.stmt = nil
		cap.args = nil
		cap.mu.Unlock()

		got, err := quark.For[JSONDoc](ctx, client).
			WhereJSON("data", "plan", "=", "enterprise").
			Limit(10).
			List()
		if err != nil {
			t.Fatalf("List failed: %v", err)
		}
		if len(got) != 1 || !strings.Contains(got[0].Data, "enterprise") {
			t.Errorf("expected one enterprise row, got %d: %+v", len(got), got)
		}

		stmts, argsList := cap.snapshot()
		if len(stmts) == 0 {
			t.Fatal("no SELECT captured")
		}
		// Find the SELECT that hits json_docs.
		var sel string
		var selArgs []any
		for i, s := range stmts {
			if strings.Contains(s, "json_docs") {
				sel = s
				selArgs = argsList[i]
				break
			}
		}
		if sel == "" {
			t.Fatalf("did not observe SELECT against json_docs in: %v", stmts)
		}
		// The raw path "plan" must not appear quoted-as-literal in the SQL.
		// PG, SQLite, MySQL/MariaDB, MSSQL, Oracle all bind it now — so the
		// string "'plan'" should never appear in the emitted SQL.
		if strings.Contains(sel, "'plan'") {
			t.Errorf("path was interpolated into SQL (found '\\'plan\\''): %s", sel)
		}
		// Same check for the older pattern '$.plan'.
		if strings.Contains(sel, "'$.plan'") {
			t.Errorf("path was interpolated into SQL (found '$.plan'): %s", sel)
		}
		// The bind args must include the path component(s).
		// For PG variadic shape: each segment is a separate text arg.
		// For all others: a single "$.plan" arg.
		joined := ""
		for _, a := range selArgs {
			if s, ok := a.(string); ok {
				joined += s + " "
			}
		}
		if !strings.Contains(joined, "plan") {
			t.Errorf("path component %q not found in bound args %v", "plan", selArgs)
		}
	})

	t.Run("DottedPathBoundNotInterpolated", func(t *testing.T) {
		cap.mu.Lock()
		cap.stmt = nil
		cap.args = nil
		cap.mu.Unlock()

		got, err := quark.For[JSONDoc](ctx, client).
			WhereJSON("data", "user.name", "=", "alice").
			Limit(10).
			List()
		if err != nil {
			t.Fatalf("List failed: %v", err)
		}
		if len(got) != 1 || !strings.Contains(got[0].Data, "alice") {
			t.Errorf("expected one alice row, got %d: %+v", len(got), got)
		}

		stmts, _ := cap.snapshot()
		var sel string
		for _, s := range stmts {
			if strings.Contains(s, "json_docs") {
				sel = s
				break
			}
		}
		if sel == "" {
			t.Fatalf("did not observe SELECT against json_docs in: %v", stmts)
		}
		if strings.Contains(sel, "'user.name'") || strings.Contains(sel, "'$.user.name'") {
			t.Errorf("dotted path was interpolated into SQL: %s", sel)
		}
	})

	t.Run("InjectionAttemptRejected", func(t *testing.T) {
		injectionPaths := []string{
			"x'; DROP TABLE users--",
			"x; SELECT 1",
			"$.user.name", // leading $ rejected
			"",
			"user name",
			"user-name",
			"user/*x*/name",
			"user'name",
		}
		for _, bad := range injectionPaths {
			t.Run(bad, func(t *testing.T) {
				_, err := quark.For[JSONDoc](ctx, client).
					WhereJSON("data", bad, "=", "x").
					Limit(10).
					List()
				if err == nil {
					t.Errorf("path %q should have been rejected, got nil error", bad)
					return
				}
				if !errors.Is(err, quark.ErrInvalidJSONPath) {
					t.Errorf("path %q: expected ErrInvalidJSONPath, got %v", bad, err)
				}
			})
		}
	})
}
