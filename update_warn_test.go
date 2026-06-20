package quark_test

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
	_ "modernc.org/sqlite"
)

// TestUpdateWarnsOnlyScalarZeros is the regression for the noisy-WARN fix.
// Update must warn when it skips a *scalar* zero (false / 0 / "") that the
// caller might have meant to persist, but must stay silent when the only
// skipped fields are nil pointers/slices/maps — the idiomatic "absent" case
// (e.g. deleted_at on every soft-delete model). Before the fix, a routine
// Update of any soft-delete model logged a WARN naming deleted_at.
func TestUpdateWarnsOnlyScalarZeros(t *testing.T) {
	type WarnUser struct {
		ID        int64      `db:"id" pk:"true"`
		Name      string     `db:"name"`
		Score     int        `db:"score"`
		DeletedAt *time.Time `db:"deleted_at"`
	}

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	ctx := context.Background()
	client, err := quark.New("sqlite", "file:warnuser?mode=memory&cache=shared",
		quark.WithLogger(logger))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer client.Close()

	if err := client.Migrate(ctx, &WarnUser{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	u := &WarnUser{Name: "Alice", Score: 10}
	if err := quark.For[WarnUser](ctx, client).Create(u); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Case 1: a routine partial update. The only zero field is the nil
	// deleted_at pointer, so Update must NOT warn.
	buf.Reset()
	u.Name = "Alice Walker"
	if _, err := quark.For[WarnUser](ctx, client).Update(u); err != nil {
		t.Fatalf("update 1: %v", err)
	}
	if strings.Contains(buf.String(), "skipped zero-value") {
		t.Errorf("Update warned on a nil-pointer-only skip (deleted_at); want silence.\nlog: %s", buf.String())
	}

	// Case 2: a scalar zero (Score = 0) is skipped. Update MUST warn, naming
	// the scalar column but NOT the nil pointer.
	buf.Reset()
	u.Score = 0
	if _, err := quark.For[WarnUser](ctx, client).Update(u); err != nil {
		t.Fatalf("update 2: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "skipped zero-value") {
		t.Errorf("Update did not warn on a skipped scalar zero (score).\nlog: %q", out)
	}
	if !strings.Contains(out, "score") {
		t.Errorf("warn should name the skipped scalar column 'score'.\nlog: %q", out)
	}
	if strings.Contains(out, "deleted_at") {
		t.Errorf("warn should not name the nil-pointer column 'deleted_at'.\nlog: %q", out)
	}
}
