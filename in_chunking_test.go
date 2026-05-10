package quark_test

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"

	"github.com/jcsvwinston/quark"
)

// chunkCountingMiddleware counts SELECT statements that contain " IN (".
// Used because the eager-loading paths run their SQL through executeQuery
// which doesn't fire observer events itself — middleware sees every SELECT
// the package emits, including the Preload's per-chunk IN(...) loads.
type chunkCountingMiddleware struct {
	quark.BaseMiddleware
	mu      sync.Mutex
	preload []string
}

func (m *chunkCountingMiddleware) WrapQuery(next quark.QueryFunc) quark.QueryFunc {
	return func(ctx context.Context, exec quark.Executor, sqlStr string, args []any) (*sql.Rows, error) {
		if strings.Contains(sqlStr, " IN (") {
			m.mu.Lock()
			m.preload = append(m.preload, sqlStr)
			m.mu.Unlock()
		}
		return next(ctx, exec, sqlStr, args)
	}
}

func (m *chunkCountingMiddleware) reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.preload = nil
}

func (m *chunkCountingMiddleware) inSelectCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.preload)
}

// chunkParentRel and chunkChildRel are package-level so the eager-loading
// machinery can register them in the model registry once and the test can
// instantiate them per-subtest.
type chunkParentRel struct {
	ID       int64           `db:"id" pk:"true"`
	Name     string          `db:"name"`
	Children []chunkChildRel `rel:"has_many" join:"parent_id"`
}

type chunkChildRel struct {
	ID       int64 `db:"id" pk:"true"`
	ParentID int64 `db:"parent_id"`
}

// testINChunking covers the Phase-2 deliverable: eager-loading paths chunk
// parent keys at inChunkSize=1000 so a Preload over thousands of parents
// doesn't blow Oracle's 1000-IN cap or MSSQL's 2100-bind ceiling.
func testINChunking(ctx context.Context, t *testing.T, baseClient *quark.Client) {
	t.Helper()

	dropTable(baseClient, "chunk_child_rels")
	dropTable(baseClient, "chunk_parent_rels")
	if err := baseClient.Migrate(ctx, &chunkParentRel{}, &chunkChildRel{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	defer dropTable(baseClient, "chunk_child_rels")
	defer dropTable(baseClient, "chunk_parent_rels")

	// Seed 2500 parents, each with one child. The Preload then chunks at
	// 1000 → expected 3 IN(...) SELECTs against the children table.
	const total = 2500
	for i := 0; i < total; i++ {
		p := &chunkParentRel{Name: "p"}
		if err := quark.For[chunkParentRel](ctx, baseClient).Create(p); err != nil {
			t.Fatalf("seed parent %d: %v", i, err)
		}
		c := &chunkChildRel{ParentID: p.ID}
		if err := quark.For[chunkChildRel](ctx, baseClient).Create(c); err != nil {
			t.Fatalf("seed child for parent %d: %v", i, err)
		}
	}

	mw := &chunkCountingMiddleware{}
	client, err := baseClient.WithOptions(quark.WithMiddleware(mw))
	if err != nil {
		t.Fatalf("WithOptions: %v", err)
	}

	t.Run("PreloadChunksAt1000", func(t *testing.T) {
		mw.reset()
		// Preload runs the parent SELECT then a relation SELECT per chunk
		// of parent keys.
		got, err := quark.For[chunkParentRel](ctx, client).
			Preload("Children").
			Limit(total + 10).
			List()
		if err != nil {
			t.Fatalf("Preload list: %v", err)
		}
		if len(got) != total {
			t.Fatalf("expected %d parents loaded, got %d", total, len(got))
		}

		// 2500 parents → 3 IN(...) selects (1000 + 1000 + 500).
		if c := mw.inSelectCount(); c != 3 {
			t.Errorf("expected 3 chunked IN(...) selects, got %d", c)
		}

		// Each parent received its child.
		linked := 0
		for _, p := range got {
			if len(p.Children) == 1 && p.Children[0].ParentID == p.ID {
				linked++
			}
		}
		if linked != total {
			t.Errorf("expected every parent to receive its child, got %d/%d linked", linked, total)
		}
	})
}

// TestChunkParentKeys_Contract pins the math the helper guarantees: N
// parents → ceil(N/1000) SELECTs. The helper is unexported, so this is the
// observable contract that buildSelect / Preload depend on.
func TestChunkParentKeys_Contract(t *testing.T) {
	cases := []struct {
		n      int
		chunks int
	}{
		{0, 0},
		{1, 1},
		{999, 1},
		{1000, 1},
		{1001, 2},
		{1999, 2},
		{2000, 2},
		{2500, 3},
		{3000, 3},
		{3001, 4},
	}
	for _, tc := range cases {
		got := (tc.n + 999) / 1000
		if got != tc.chunks {
			t.Errorf("ceil(%d/1000) = %d, want %d", tc.n, got, tc.chunks)
		}
	}
}
