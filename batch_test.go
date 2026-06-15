package quark_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/jcsvwinston/quark"
	_ "modernc.org/sqlite"
)

// ─── shared model ─────────────────────────────────────────────────────────────

type BatchUser struct {
	ID    int64  `db:"id"    pk:"true"`
	Name  string `db:"name"`
	Email string `db:"email" quark:"unique"`
	Score int    `db:"score"`
}

// newBatchClient spins up an isolated in-memory SQLite client for a single test.
func newBatchClient(t *testing.T) (*quark.Client, func()) {
	t.Helper()
	l := quark.DefaultLimits()
	l.SafeMigrations = false
	client, err := quark.New("sqlite", ":memory:", quark.WithLimits(l))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	ctx := context.Background()
	if err := client.Migrate(ctx, &BatchUser{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return client, func() { client.Close() }
}

// seedBatch inserts N BatchUser rows and returns them (with IDs populated).
func seedBatch(t *testing.T, client *quark.Client, n int) []*BatchUser {
	t.Helper()
	ctx := context.Background()
	var users []*BatchUser
	for i := 1; i <= n; i++ {
		u := &BatchUser{
			Name:  "User" + string(rune('A'+i-1)),
			Email: "user" + string(rune('a'+i-1)) + "@batch.com",
			Score: i * 10,
		}
		if err := quark.For[BatchUser](ctx, client).Create(u); err != nil {
			t.Fatalf("seed create %d: %v", i, err)
		}
		users = append(users, u)
	}
	return users
}

// ─── DeleteBatch ──────────────────────────────────────────────────────────────

func TestDeleteBatch_DeletesAllGiven(t *testing.T) {
	client, teardown := newBatchClient(t)
	defer teardown()
	ctx := context.Background()

	users := seedBatch(t, client, 5)
	ids := []any{users[0].ID, users[1].ID, users[2].ID}

	affected, err := quark.For[BatchUser](ctx, client).DeleteBatch(ids)
	if err != nil {
		t.Fatalf("DeleteBatch: %v", err)
	}
	if affected != 3 {
		t.Errorf("expected 3 rows affected, got %d", affected)
	}

	remaining, err := quark.For[BatchUser](ctx, client).Count()
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if remaining != 2 {
		t.Errorf("expected 2 remaining rows, got %d", remaining)
	}
}

func TestDeleteBatch_EmptySliceIsNoop(t *testing.T) {
	client, teardown := newBatchClient(t)
	defer teardown()
	ctx := context.Background()

	seedBatch(t, client, 3)
	affected, err := quark.For[BatchUser](ctx, client).DeleteBatch([]any{})
	if err != nil {
		t.Fatalf("DeleteBatch empty: %v", err)
	}
	if affected != 0 {
		t.Errorf("expected 0 affected for empty input, got %d", affected)
	}
	count, _ := quark.For[BatchUser](ctx, client).Count()
	if count != 3 {
		t.Errorf("rows should be untouched, got %d", count)
	}
}

func TestDeleteBatch_NonExistentIDsReturnZero(t *testing.T) {
	client, teardown := newBatchClient(t)
	defer teardown()
	ctx := context.Background()

	affected, err := quark.For[BatchUser](ctx, client).DeleteBatch([]any{99999, 88888})
	if err != nil {
		t.Fatalf("DeleteBatch nonexistent: %v", err)
	}
	if affected != 0 {
		t.Errorf("expected 0 affected for non-existent IDs, got %d", affected)
	}
}

func TestDeleteBatch_ChunkingLargeSlice(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large-batch chunking test in short mode")
	}
	client, teardown := newBatchClient(t)
	defer teardown()
	ctx := context.Background()

	// Insert 1200 records — crosses the 1000-element chunk boundary.
	const n = 1200
	var users []*BatchUser
	for i := 0; i < n; i++ {
		u := &BatchUser{Name: "Chunk", Email: fmt.Sprintf("chunk%d@x.com", i), Score: i}
		users = append(users, u)
	}
	if err := quark.For[BatchUser](ctx, client).CreateBatch(users); err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}

	all, err := quark.For[BatchUser](ctx, client).Limit(n + 100).List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	ids := make([]any, len(all))
	for i, u := range all {
		ids[i] = u.ID
	}

	affected, err := quark.For[BatchUser](ctx, client).DeleteBatch(ids)
	if err != nil {
		t.Fatalf("DeleteBatch large: %v", err)
	}
	if affected != int64(n) {
		t.Errorf("expected %d affected, got %d", n, affected)
	}
	count, _ := quark.For[BatchUser](ctx, client).Count()
	if count != 0 {
		t.Errorf("expected 0 remaining, got %d", count)
	}
}

// TestCreateBatch_ChunkingLargeSlice exercises CreateBatch across the bind-param
// chunk boundary directly (the cross-engine regression for BB-10 lives in the
// bug-bash f04_volume phase; this keeps a guard in the standard -short suite).
// BatchUser has 3 insertable columns, so rowsPerChunk = maxBatchBindParams/3 and
// 2000 rows spans several chunks — all rows must land, with PKs written back.
func TestCreateBatch_ChunkingLargeSlice(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping large-batch chunking test in short mode")
	}
	client, teardown := newBatchClient(t)
	defer teardown()
	ctx := context.Background()

	const n = 2000
	users := make([]*BatchUser, n)
	for i := 0; i < n; i++ {
		users[i] = &BatchUser{Name: "Bulk", Email: fmt.Sprintf("bulk%d@x.com", i), Score: i}
	}
	if err := quark.For[BatchUser](ctx, client).CreateBatch(users); err != nil {
		t.Fatalf("CreateBatch across chunk boundary: %v", err)
	}

	count, err := quark.For[BatchUser](ctx, client).Count()
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != int64(n) {
		t.Errorf("CreateBatch persisted %d rows, want %d", count, n)
	}
	// RETURNING dialects write PKs back to each pointer; the chunk slices alias
	// the caller slice, so the last entity must have a populated PK.
	if client.Dialect().SupportsReturning() && users[n-1].ID == 0 {
		t.Errorf("last entity PK not written back after chunked CreateBatch")
	}
}

// ─── UpsertBatch ──────────────────────────────────────────────────────────────

func TestUpsertBatch_InsertsNewRecords(t *testing.T) {
	client, teardown := newBatchClient(t)
	defer teardown()
	ctx := context.Background()

	users := []*BatchUser{
		{Name: "Alice", Email: "alice@ub.com", Score: 10},
		{Name: "Bob", Email: "bob@ub.com", Score: 20},
		{Name: "Carol", Email: "carol@ub.com", Score: 30},
	}
	if err := quark.For[BatchUser](ctx, client).UpsertBatch(users, []string{"email"}, []string{"name", "score"}); err != nil {
		t.Fatalf("UpsertBatch insert: %v", err)
	}

	count, _ := quark.For[BatchUser](ctx, client).Count()
	if count != 3 {
		t.Errorf("expected 3 rows after insert, got %d", count)
	}
}

func TestUpsertBatch_UpdatesExistingRecords(t *testing.T) {
	client, teardown := newBatchClient(t)
	defer teardown()
	ctx := context.Background()

	initial := []*BatchUser{
		{Name: "Alice", Email: "alice@ub.com", Score: 10},
		{Name: "Bob", Email: "bob@ub.com", Score: 20},
	}
	if err := quark.For[BatchUser](ctx, client).UpsertBatch(initial, []string{"email"}, []string{"name", "score"}); err != nil {
		t.Fatalf("UpsertBatch initial: %v", err)
	}

	// Upsert same emails with updated scores — should update, not insert.
	updated := []*BatchUser{
		{Name: "Alice-V2", Email: "alice@ub.com", Score: 99},
		{Name: "Bob-V2", Email: "bob@ub.com", Score: 88},
	}
	if err := quark.For[BatchUser](ctx, client).UpsertBatch(updated, []string{"email"}, []string{"name", "score"}); err != nil {
		t.Fatalf("UpsertBatch update: %v", err)
	}

	count, _ := quark.For[BatchUser](ctx, client).Count()
	if count != 2 {
		t.Errorf("expected 2 rows (no duplicates), got %d", count)
	}

	alice, err := quark.For[BatchUser](ctx, client).Where("email", "=", "alice@ub.com").First()
	if err != nil {
		t.Fatalf("find alice: %v", err)
	}
	if alice.Name != "Alice-V2" || alice.Score != 99 {
		t.Errorf("alice not updated: got Name=%q Score=%d", alice.Name, alice.Score)
	}
}

func TestUpsertBatch_EmptySliceIsNoop(t *testing.T) {
	client, teardown := newBatchClient(t)
	defer teardown()
	ctx := context.Background()

	err := quark.For[BatchUser](ctx, client).UpsertBatch([]*BatchUser{}, []string{"email"}, nil)
	if err != nil {
		t.Fatalf("UpsertBatch empty: %v", err)
	}
	count, _ := quark.For[BatchUser](ctx, client).Count()
	if count != 0 {
		t.Errorf("expected 0 rows, got %d", count)
	}
}

func TestUpsertBatch_MixedInsertAndUpdate(t *testing.T) {
	client, teardown := newBatchClient(t)
	defer teardown()
	ctx := context.Background()

	// Pre-insert one record
	existing := []*BatchUser{{Name: "Alice", Email: "alice@ub.com", Score: 10}}
	if err := quark.For[BatchUser](ctx, client).UpsertBatch(existing, []string{"email"}, []string{"name", "score"}); err != nil {
		t.Fatalf("pre-insert: %v", err)
	}

	// Upsert Alice (update) + Dave (new insert)
	mixed := []*BatchUser{
		{Name: "Alice-Updated", Email: "alice@ub.com", Score: 77},
		{Name: "Dave", Email: "dave@ub.com", Score: 55},
	}
	if err := quark.For[BatchUser](ctx, client).UpsertBatch(mixed, []string{"email"}, []string{"name", "score"}); err != nil {
		t.Fatalf("UpsertBatch mixed: %v", err)
	}

	count, _ := quark.For[BatchUser](ctx, client).Count()
	if count != 2 {
		t.Errorf("expected 2 rows, got %d", count)
	}

	alice, _ := quark.For[BatchUser](ctx, client).Where("email", "=", "alice@ub.com").First()
	if alice.Name != "Alice-Updated" {
		t.Errorf("expected Alice-Updated, got %q", alice.Name)
	}
	dave, _ := quark.For[BatchUser](ctx, client).Where("email", "=", "dave@ub.com").First()
	if dave.Name != "Dave" {
		t.Errorf("expected Dave, got %q", dave.Name)
	}
}

// ─── UpdateBatch ──────────────────────────────────────────────────────────────

func TestUpdateBatch_UpdatesAllRecords(t *testing.T) {
	client, teardown := newBatchClient(t)
	defer teardown()
	ctx := context.Background()

	users := seedBatch(t, client, 4)

	// Modify all in memory
	for _, u := range users {
		u.Score = u.Score + 1000
		u.Name = u.Name + "-Updated"
	}

	if err := quark.For[BatchUser](ctx, client).UpdateBatch(users); err != nil {
		t.Fatalf("UpdateBatch: %v", err)
	}

	for _, u := range users {
		got, err := quark.For[BatchUser](ctx, client).Find(u.ID)
		if err != nil {
			t.Fatalf("find %d: %v", u.ID, err)
		}
		if got.Score != u.Score {
			t.Errorf("id=%d: expected Score=%d, got %d", u.ID, u.Score, got.Score)
		}
		if got.Name != u.Name {
			t.Errorf("id=%d: expected Name=%q, got %q", u.ID, u.Name, got.Name)
		}
	}
}

func TestUpdateBatch_EmptySliceIsNoop(t *testing.T) {
	client, teardown := newBatchClient(t)
	defer teardown()
	ctx := context.Background()

	seedBatch(t, client, 2)
	err := quark.For[BatchUser](ctx, client).UpdateBatch([]*BatchUser{})
	if err != nil {
		t.Fatalf("UpdateBatch empty: %v", err)
	}
}

func TestUpdateBatch_IsAtomic_RollsBackOnError(t *testing.T) {
	client, teardown := newBatchClient(t)
	defer teardown()
	ctx := context.Background()

	users := seedBatch(t, client, 2)
	originalScore0 := users[0].Score
	originalScore1 := users[1].Score

	// Corrupt second entity so buildUpdate returns an error
	// (zero-value entity with no non-zero fields to update = ErrInvalidQuery).
	broken := []*BatchUser{
		{ID: users[0].ID, Score: 9999},
		{ID: users[1].ID}, // all other fields zero → buildUpdate will error
	}

	err := quark.For[BatchUser](ctx, client).UpdateBatch(broken)
	if err == nil {
		t.Fatal("expected error for entity with no updatable fields, got nil")
	}

	// Both rows must be unchanged due to transaction rollback.
	got0, _ := quark.For[BatchUser](ctx, client).Find(users[0].ID)
	got1, _ := quark.For[BatchUser](ctx, client).Find(users[1].ID)
	if got0.Score != originalScore0 {
		t.Errorf("row 0 should be unchanged: expected Score=%d, got %d", originalScore0, got0.Score)
	}
	if got1.Score != originalScore1 {
		t.Errorf("row 1 should be unchanged: expected Score=%d, got %d", originalScore1, got1.Score)
	}
}

// ─── cross-engine hook ─────────────────────────────────────────────────────────

// testBatchOps is wired into SharedSuite to run all batch operations
// cross-engine (SQLite, Postgres, MySQL, MariaDB, MSSQL, Oracle).
func testBatchOps(ctx context.Context, t *testing.T, client *quark.Client) {
	type BSOp struct {
		ID    int64  `db:"id"    pk:"true"`
		Name  string `db:"name"`
		Email string `db:"email" quark:"unique"`
		Score int    `db:"score"`
	}

	dropTable(client, "bs_ops")
	if err := client.Migrate(ctx, &BSOp{}); err != nil {
		t.Fatalf("migrate bs_ops: %v", err)
	}

	seed := []*BSOp{
		{Name: "Alpha", Email: "a@bs.com", Score: 1},
		{Name: "Beta", Email: "b@bs.com", Score: 2},
		{Name: "Gamma", Email: "c@bs.com", Score: 3},
		{Name: "Delta", Email: "d@bs.com", Score: 4},
		{Name: "Epsilon", Email: "e@bs.com", Score: 5},
	}

	// Seed via CreateBatch (already tested separately).
	if err := quark.For[BSOp](ctx, client).CreateBatch(seed); err != nil {
		t.Fatalf("CreateBatch seed: %v", err)
	}

	// Finding C regression: on RETURNING dialects (PG/SQLite/Oracle/MariaDB),
	// CreateBatch must back-fill the generated PK into EVERY entity — not just
	// the last one. Oracle's per-row INSERT path previously left them all 0
	// while the rows inserted fine, a silent divergence the chunk test (which
	// only checks the last row, SQLite-only) never caught.
	if client.Dialect().SupportsReturning() {
		pkSeen := make(map[int64]bool, len(seed))
		for i, e := range seed {
			switch {
			case e.ID == 0:
				t.Errorf("CreateBatch: seed[%d] (%s) PK not back-filled (ID==0)", i, e.Email)
			case pkSeen[e.ID]:
				t.Errorf("CreateBatch: seed[%d] PK %d duplicated — PKs not distinct", i, e.ID)
			}
			pkSeen[e.ID] = true
		}
	}

	// Re-fetch to get real PKs.
	allRows, err := quark.For[BSOp](ctx, client).OrderBy("score", "ASC").List()
	if err != nil || len(allRows) != 5 {
		t.Fatalf("list after seed: err=%v len=%d", err, len(allRows))
	}
	// Convert []BSOp → []*BSOp for pointer-based batch methods.
	all := make([]*BSOp, len(allRows))
	for i := range allRows {
		v := allRows[i]
		all[i] = &v
	}

	t.Run("UpsertBatch_Insert", func(t *testing.T) {
		newRows := []*BSOp{
			{Name: "Zeta", Email: "z@bs.com", Score: 99},
		}
		if err := quark.For[BSOp](ctx, client).UpsertBatch(newRows, []string{"email"}, []string{"name", "score"}); err != nil {
			t.Fatalf("UpsertBatch insert: %v", err)
		}
		count, _ := quark.For[BSOp](ctx, client).Count()
		if count != 6 {
			t.Errorf("expected 6 rows, got %d", count)
		}
	})

	t.Run("UpsertBatch_Update", func(t *testing.T) {
		updated := []*BSOp{
			{Name: "Alpha-X", Email: "a@bs.com", Score: 100},
			{Name: "Beta-X", Email: "b@bs.com", Score: 200},
		}
		if err := quark.For[BSOp](ctx, client).UpsertBatch(updated, []string{"email"}, []string{"name", "score"}); err != nil {
			t.Fatalf("UpsertBatch update: %v", err)
		}
		alpha, _ := quark.For[BSOp](ctx, client).Where("email", "=", "a@bs.com").First()
		if alpha.Score != 100 {
			t.Errorf("expected Score=100, got %d", alpha.Score)
		}
	})

	t.Run("UpdateBatch", func(t *testing.T) {
		for _, u := range all[:3] {
			u.Score = u.Score + 500
		}
		if err := quark.For[BSOp](ctx, client).UpdateBatch(all[:3]); err != nil {
			t.Fatalf("UpdateBatch: %v", err)
		}
		for _, u := range all[:3] {
			got, err := quark.For[BSOp](ctx, client).Find(u.ID)
			if err != nil {
				t.Fatalf("find %d: %v", u.ID, err)
			}
			if got.Score != u.Score {
				t.Errorf("id=%d: expected Score=%d, got %d", u.ID, u.Score, got.Score)
			}
		}
	})

	t.Run("DeleteBatch", func(t *testing.T) {
		before, _ := quark.For[BSOp](ctx, client).Count()
		ids := []any{all[0].ID, all[1].ID}
		affected, err := quark.For[BSOp](ctx, client).DeleteBatch(ids)
		if err != nil {
			t.Fatalf("DeleteBatch: %v", err)
		}
		if affected != 2 {
			t.Errorf("expected 2 affected, got %d", affected)
		}
		count, _ := quark.For[BSOp](ctx, client).Count()
		if count != before-2 {
			t.Errorf("expected %d rows remaining, got %d", before-2, count)
		}
	})
}
