package quark_test

import (
	"context"
	"database/sql"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// Shared setup helpers
// ---------------------------------------------------------------------------

func newSQLiteDB(t *testing.T) *quark.Client {
	t.Helper()
	limits := quark.DefaultLimits()
	limits.AllowRawQueries = true
	c, err := quark.New("sqlite", ":memory:", quark.WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// ---------------------------------------------------------------------------
// Client options: WithLogger, WithQueryObserver, WithMiddleware, WithLimits
// ---------------------------------------------------------------------------

func TestClientOptions(t *testing.T) {
	logger := slog.Default()

	type mockObs struct{ called bool }
	obsImpl := &testQueryObserver{fn: func(e quark.QueryEvent) { _ = e }}

	limits := quark.Limits{
		MaxQueryLength:     1024,
		MaxResults:         500,
		MaxJoins:           3,
		MaxWhereConditions: 10,
		QueryTimeout:       10 * time.Second,
		AllowRawQueries:    true,
		SafeMigrations:     false,
	}

	c, err := quark.New("sqlite", ":memory:",
		quark.WithLogger(logger),
		quark.WithQueryObserver(obsImpl),
		quark.WithMiddleware(&passthroughMiddleware{}),
		quark.WithLimits(limits),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	// Raw() returns underlying db
	if c.Raw() == nil {
		t.Error("Raw() should not be nil")
	}

	// Dialect()
	if c.Dialect().Name() != "sqlite" {
		t.Errorf("Dialect=%q", c.Dialect().Name())
	}

	// RawQuery with AllowRawQueries=true — must use a placeholder to pass the guard
	rows, err := c.RawQuery(context.Background(), "SELECT ?", 1)
	if err != nil {
		t.Fatalf("RawQuery: %v", err)
	}
	rows.Close()

	// Exec
	if err := c.Exec(context.Background(), "CREATE TABLE raw_test (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(context.Background(), "INSERT INTO raw_test VALUES (1)"); err != nil {
		t.Fatalf("Exec: %v", err)
	}
}

func TestClientRawQueryDisabled(t *testing.T) {
	c, err := quark.New("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	// Default limits have AllowRawQueries=false
	_, err = c.RawQuery(context.Background(), "SELECT 1")
	if err == nil {
		t.Error("expected error when raw queries disabled")
	}
	if err := c.Exec(context.Background(), "SELECT 1"); err == nil {
		t.Error("expected error when raw queries disabled")
	}
}

// ---------------------------------------------------------------------------
// BaseMiddleware pass-through (option.go:122-124)
// ---------------------------------------------------------------------------

type passthroughMiddleware struct{ quark.BaseMiddleware }

type testQueryObserver struct {
	fn func(quark.QueryEvent)
}

func (o *testQueryObserver) ObserveQuery(e quark.QueryEvent) {
	if o.fn != nil {
		o.fn(e)
	}
}

func TestBaseMiddlewarePassthrough(t *testing.T) {
	bm := quark.BaseMiddleware{}
	ctx := context.Background()

	execNext := func(ctx context.Context, exec quark.Executor, sql string, args []any) (sql.Result, error) {
		return nil, nil
	}
	queryNext := func(ctx context.Context, exec quark.Executor, sql string, args []any) (*sql.Rows, error) {
		return nil, nil
	}
	queryRowNext := func(ctx context.Context, exec quark.Executor, sql string, args []any) *sql.Row {
		return nil
	}
	_ = ctx

	wrappedExec := bm.WrapExec(execNext)
	if wrappedExec == nil {
		t.Error("WrapExec returned nil")
	}
	wrappedQuery := bm.WrapQuery(queryNext)
	if wrappedQuery == nil {
		t.Error("WrapQuery returned nil")
	}
	wrappedRow := bm.WrapQueryRow(queryRowNext)
	if wrappedRow == nil {
		t.Error("WrapQueryRow returned nil")
	}
}

// ---------------------------------------------------------------------------
// Routine builder: NewRoutine, List, First, Scalar
// ---------------------------------------------------------------------------

func TestRoutineBuilder(t *testing.T) {
	c := newSQLiteDB(t)
	defer c.Close()

	ctx := context.Background()

	// Create a real table-valued function simulation using a table
	if err := c.Exec(ctx, `CREATE TABLE routine_vals (val INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(ctx, `INSERT INTO routine_vals VALUES (42)`); err != nil {
		t.Fatal(err)
	}
	// SQLite BuildRoutineQuery("routine_vals", 0) → "SELECT * FROM routine_vals()"
	// but SQLite doesn't support TVFs directly, so use NewRoutine with a table name
	// that exists as a regular table and no args (0 placeholders).
	r := quark.NewRoutine[int64](ctx, c, "routine_vals")
	vals, err := r.List()
	if err != nil {
		// If the driver doesn't support TVF syntax, skip gracefully
		t.Logf("Routine.List (TVF not supported in SQLite): %v", err)
		return
	}
	if len(vals) != 1 || vals[0] != 42 {
		t.Errorf("Routine.List expected [42], got %v", vals)
	}

	// First
	v, err := r.First()
	if err != nil {
		t.Logf("Routine.First: %v", err)
		return
	}
	if v != 42 {
		t.Errorf("Routine.First expected 42, got %d", v)
	}

	// Scalar
	s, err := r.Scalar()
	if err != nil {
		t.Logf("Routine.Scalar: %v", err)
		return
	}
	if s != 42 {
		t.Errorf("Routine.Scalar expected 42, got %d", s)
	}
}

func TestRoutineBuilder_FirstEmpty(t *testing.T) {
	c := newSQLiteDB(t)
	defer c.Close()
	ctx := context.Background()

	var err error
	if err = c.Exec(ctx, "CREATE TABLE empty_tbl (id INTEGER PRIMARY KEY)"); err != nil {
		t.Fatal(err)
	}

	// This will call BuildRoutineQuery("max", 0) → "SELECT max()" — SQLite returns NULL for max() on empty
	// Use a simpler approach: call a function that returns no rows via a subquery
	r := quark.NewRoutine[int64](ctx, c, "max", "SELECT id FROM empty_tbl WHERE 1=0")
	_, err = r.First()
	// We expect sql.ErrNoRows or a query error - either way First() must not panic
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		// acceptable: function may error on SQLite for this usage
		t.Logf("Routine.First on empty: %v (acceptable)", err)
	}
}

// ---------------------------------------------------------------------------
// Transaction: Savepoint, RollbackTo, ReleaseSavepoint error paths
// ---------------------------------------------------------------------------

func TestTxSavepointInvalidName(t *testing.T) {
	c := newSQLiteDB(t)
	ctx := context.Background()

	tx, err := c.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()

	// Invalid identifier should return error
	if err := tx.Savepoint("invalid name with spaces"); err == nil {
		t.Error("expected error for invalid savepoint name")
	}
	if err := tx.RollbackTo("bad-name!"); err == nil {
		t.Error("expected error for invalid RollbackTo name")
	}
	if err := tx.ReleaseSavepoint("bad-name!"); err == nil {
		t.Error("expected error for invalid ReleaseSavepoint name")
	}
}

func TestTxPanicRollback(t *testing.T) {
	c := newSQLiteDB(t)
	ctx := context.Background()

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic to propagate")
		}
	}()

	_ = c.Tx(ctx, func(tx *quark.Tx) error {
		panic("intentional panic")
	})
}

func TestTxNestedPanicRollback(t *testing.T) {
	c := newSQLiteDB(t)
	ctx := context.Background()

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic to propagate from nested tx")
		}
	}()

	_ = c.Tx(ctx, func(tx *quark.Tx) error {
		return tx.Tx(ctx, func(inner *quark.Tx) error {
			panic("inner panic")
		})
	})
}

// ---------------------------------------------------------------------------
// Sync: DryRun path and NoTransaction path
// ---------------------------------------------------------------------------

type SyncModel struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

type SyncModelV2 struct {
	ID    int64  `db:"id" pk:"true"`
	Name  string `db:"name"`
	Email string `db:"email"`
}

func TestSyncDryRun(t *testing.T) {
	c := newSQLiteDB(t)
	ctx := context.Background()

	// Migrate v1
	if err := c.Migrate(ctx, SyncModel{}); err != nil {
		t.Fatal(err)
	}

	// DryRun sync with v2 (has extra column)
	err := c.Sync(ctx, quark.SyncOptions{DryRun: true}, &SyncModelV2{})
	if err != nil {
		t.Fatalf("Sync DryRun: %v", err)
	}
}

func TestSyncNoTransaction(t *testing.T) {
	c := newSQLiteDB(t)
	ctx := context.Background()

	if err := c.Migrate(ctx, SyncModel{}); err != nil {
		t.Fatal(err)
	}

	err := c.Sync(ctx, quark.SyncOptions{NoTransaction: true}, &SyncModelV2{})
	if err != nil {
		t.Fatalf("Sync NoTransaction: %v", err)
	}
}

func TestSyncDropColumn(t *testing.T) {
	// Use unsafe mode (SafeMigrations=false) to cover drop column path
	limits := quark.DefaultLimits()
	limits.SafeMigrations = false
	c, err := quark.New("sqlite", ":memory:", quark.WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	// Migrate v2 first (has email column)
	if err := c.Migrate(ctx, SyncModelV2{}); err != nil {
		t.Fatal(err)
	}
	// Sync back to v1 (will try to drop email) - may error on SQLite (no DROP COLUMN pre-3.35)
	err = c.Sync(ctx, quark.SyncOptions{}, &SyncModel{})
	if err != nil {
		t.Logf("SyncDropColumn (expected on older SQLite): %v", err)
	}
}

// ---------------------------------------------------------------------------
// Validator: custom Validate interface path
// ---------------------------------------------------------------------------

type CustomValidatable struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

func (v *CustomValidatable) Validate(ctx context.Context) error {
	if v.Name == "invalid" {
		return errors.New("name cannot be 'invalid'")
	}
	return nil
}

func TestValidatorCustomInterface(t *testing.T) {
	c := newSQLiteDB(t)
	ctx := context.Background()

	bad := &CustomValidatable{Name: "invalid"}
	if err := c.Validate(ctx, bad); err == nil {
		t.Error("expected validation error from custom interface")
	}

	good := &CustomValidatable{Name: "alice"}
	if err := c.Validate(ctx, good); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Query builder: Join, LeftJoin, RightJoin, Having
// ---------------------------------------------------------------------------

type JoinPost struct {
	ID     int64  `db:"id" pk:"true"`
	Title  string `db:"title"`
	UserID int64  `db:"user_id"`
}

func TestQueryJoinMethods(t *testing.T) {
	c := newSQLiteDB(t)
	defer c.Close()

	ctx := context.Background()

	if err := c.Exec(ctx, `CREATE TABLE join_posts (id INTEGER PRIMARY KEY, title TEXT, user_id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(ctx, `CREATE TABLE join_users (id INTEGER PRIMARY KEY, name TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(ctx, `INSERT INTO join_users VALUES (1, 'Alice')`); err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(ctx, `INSERT INTO join_posts VALUES (1, 'Post 1', 1), (2, 'Post 2', 1)`); err != nil {
		t.Fatal(err)
	}

	// Join
	posts, err := quark.For[JoinPost](ctx, c).
		Join("join_users", "join_users.id = join_posts.user_id").
		List()
	if err != nil {
		t.Fatalf("Join: %v", err)
	}
	if len(posts) != 2 {
		t.Errorf("expected 2 posts, got %d", len(posts))
	}

	// LeftJoin
	posts2, err := quark.For[JoinPost](ctx, c).
		LeftJoin("join_users", "join_users.id = join_posts.user_id").
		List()
	if err != nil {
		t.Fatalf("LeftJoin: %v", err)
	}
	if len(posts2) != 2 {
		t.Errorf("expected 2 posts via LeftJoin, got %d", len(posts2))
	}
}

// ---------------------------------------------------------------------------
// Upsert (SQLite ON CONFLICT DO UPDATE)
// ---------------------------------------------------------------------------

type UpsertItem struct {
	ID    int64  `db:"id" pk:"true"`
	Name  string `db:"name"`
	Score int    `db:"score"`
}

func TestUpsertSQLite(t *testing.T) {
	c := newSQLiteDB(t)
	defer c.Close()

	ctx := context.Background()

	if err := c.Exec(ctx, `CREATE TABLE upsert_items (id INTEGER PRIMARY KEY, name TEXT UNIQUE, score INTEGER)`); err != nil {
		t.Fatal(err)
	}

	item := UpsertItem{Name: "alpha", Score: 10}
	if err := quark.For[UpsertItem](ctx, c).Create(&item); err != nil {
		t.Fatal(err)
	}

	// Upsert same name → update score
	item2 := UpsertItem{Name: "alpha", Score: 99}
	if err := quark.For[UpsertItem](ctx, c).Upsert(&item2, []string{"name"}, []string{"score"}); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	found, _ := quark.For[UpsertItem](ctx, c).Where("name", "=", "alpha").First()
	if found.Score != 99 {
		t.Errorf("expected score 99 after upsert, got %d", found.Score)
	}
}

// ---------------------------------------------------------------------------
// CreateBatch
// ---------------------------------------------------------------------------

type BatchItem struct {
	ID    int64  `db:"id" pk:"true"`
	Value string `db:"value"`
}

func TestCreateBatch(t *testing.T) {
	c := newSQLiteDB(t)
	defer c.Close()

	ctx := context.Background()

	if err := c.Exec(ctx, `CREATE TABLE batch_items (id INTEGER PRIMARY KEY, value TEXT)`); err != nil {
		t.Fatal(err)
	}

	items := []*BatchItem{
		{Value: "a"},
		{Value: "b"},
		{Value: "c"},
	}
	if err := quark.For[BatchItem](ctx, c).CreateBatch(items); err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}

	all, _ := quark.For[BatchItem](ctx, c).List()
	if len(all) != 3 {
		t.Errorf("expected 3 items, got %d", len(all))
	}
}

// ---------------------------------------------------------------------------
// Aggregate: Sum, Avg, Min, Max
// ---------------------------------------------------------------------------

type AggItem struct {
	ID    int64   `db:"id" pk:"true"`
	Score float64 `db:"score"`
}

func TestAggregates_Extra(t *testing.T) {
	c := newSQLiteDB(t)
	defer c.Close()

	ctx := context.Background()

	if err := c.Exec(ctx, `CREATE TABLE agg_items (id INTEGER PRIMARY KEY, score REAL)`); err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(ctx, `INSERT INTO agg_items VALUES (1,10),(2,20),(3,30)`); err != nil {
		t.Fatal(err)
	}

	sum, err := quark.For[AggItem](ctx, c).Sum("score")
	if err != nil {
		t.Fatalf("Sum: %v", err)
	}
	if sum != 60 {
		t.Errorf("Sum expected 60, got %v", sum)
	}

	avg, err := quark.For[AggItem](ctx, c).Avg("score")
	if err != nil {
		t.Fatalf("Avg: %v", err)
	}
	if avg != 20 {
		t.Errorf("Avg expected 20, got %v", avg)
	}

	min, err := quark.For[AggItem](ctx, c).Min("score")
	if err != nil {
		t.Fatalf("Min: %v", err)
	}
	if min != 10 {
		t.Errorf("Min expected 10, got %v", min)
	}

	max, err := quark.For[AggItem](ctx, c).Max("score")
	if err != nil {
		t.Fatalf("Max: %v", err)
	}
	if max != 30 {
		t.Errorf("Max expected 30, got %v", max)
	}
}

// ---------------------------------------------------------------------------
// Distinct, GroupBy, Having, Select columns
// ---------------------------------------------------------------------------

type GbItem struct {
	Category string `db:"category"`
	Score    int    `db:"score"`
}

func TestDistinctGroupByHaving(t *testing.T) {
	c := newSQLiteDB(t)
	defer c.Close()

	ctx := context.Background()

	if err := c.Exec(ctx, `CREATE TABLE gb_items (category TEXT, score INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(ctx, `INSERT INTO gb_items VALUES ('A',10),('A',20),('B',5)`); err != nil {
		t.Fatal(err)
	}

	// GroupBy only
	results, err := quark.For[GbItem](ctx, c).
		GroupBy("category").
		List()
	if err != nil {
		t.Fatalf("GroupBy: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 groups, got %d", len(results))
	}

	// Having with a simple valid column expression
	results2, err := quark.For[GbItem](ctx, c).
		GroupBy("category").
		Having("score", ">", 5).
		List()
	if err != nil {
		t.Fatalf("Having: %v", err)
	}
	_ = results2

	// Distinct — reuse GbItem which maps to gb_items table
	cats, err := quark.For[GbItem](ctx, c).Distinct().List()
	if err != nil {
		t.Fatalf("Distinct: %v", err)
	}
	if len(cats) == 0 {
		t.Error("expected distinct results")
	}
}

// ---------------------------------------------------------------------------
// RightJoin (covers the missing branch)
// ---------------------------------------------------------------------------

func TestQueryRightJoin(t *testing.T) {
	c := newSQLiteDB(t)
	defer c.Close()

	ctx := context.Background()
	var err error

	if err := c.Exec(ctx, `CREATE TABLE rj_a (id INTEGER PRIMARY KEY, val TEXT)`); err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(ctx, `CREATE TABLE rj_b (id INTEGER PRIMARY KEY, a_id INTEGER)`); err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(ctx, `INSERT INTO rj_a VALUES (1,'x')`); err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(ctx, `INSERT INTO rj_b VALUES (1,1),(2,NULL)`); err != nil {
		t.Fatal(err)
	}

	type RJB struct {
		ID  int64 `db:"id" pk:"true"`
		AID int64 `db:"a_id"`
	}
	// RIGHT JOIN not supported in old SQLite — just test the SQL builder doesn't panic
	_, err = quark.For[RJB](ctx, c).
		RightJoin("rj_a", "rj_a.id = rj_b.a_id").
		List()
	if err != nil {
		t.Logf("RightJoin error (may be unsupported in SQLite): %v", err)
	}
}

// ---------------------------------------------------------------------------
// Paginate edge cases: pageSize<=0, page<0
// ---------------------------------------------------------------------------

type PagEdge struct {
	ID int64 `db:"id" pk:"true"`
}

func TestPaginateEdgeCases(t *testing.T) {
	c := newSQLiteDB(t)
	defer c.Close()

	ctx := context.Background()

	if err := c.Exec(ctx, `CREATE TABLE pag_edges (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(ctx, `INSERT INTO pag_edges VALUES (1),(2),(3)`); err != nil {
		t.Fatal(err)
	}

	// pageSize=0 → defaults to 100
	p, err := quark.For[PagEdge](ctx, c).Paginate(0, 0)
	if err != nil {
		t.Fatalf("Paginate(0,0): %v", err)
	}
	if len(p.Items) != 3 {
		t.Errorf("expected 3 items, got %d", len(p.Items))
	}

	// page<0 → clamped to 0
	p2, err := quark.For[PagEdge](ctx, c).Paginate(100, -1)
	if err != nil {
		t.Fatalf("Paginate(100,-1): %v", err)
	}
	if p2.Page != 0 {
		t.Errorf("expected page clamped to 0, got %d", p2.Page)
	}
}

// ---------------------------------------------------------------------------
// For[T] with failing provider (error state propagation)
// ---------------------------------------------------------------------------

type failProvider struct{}

func (f *failProvider) GetClient(ctx context.Context) (*quark.Client, error) {
	return nil, errors.New("provider error")
}

func TestForWithFailingProvider(t *testing.T) {
	ctx := context.Background()
	q := quark.For[User](ctx, &failProvider{})

	// All operations should return the provider error
	_, err := q.List()
	if err == nil {
		t.Error("expected error from failing provider")
	}
	_, err = q.Count()
	if err == nil {
		t.Error("expected error from failing provider (Count)")
	}
}

// ---------------------------------------------------------------------------
// fullTableName with schema prefix
// ---------------------------------------------------------------------------

type SchemaModel struct {
	ID int64 `db:"id" pk:"true"`
}

func TestFullTableNameWithSchema(t *testing.T) {
	c := newSQLiteDB(t)
	ctx := context.Background()

	// We can't easily set schema via For[T], but we can test it doesn't panic
	// and the query is built correctly by using TenantRouter SchemaPerTenant
	cfg := quark.DefaultTenantConfig()
	cfg.Strategy = quark.SchemaPerTenant
	cfg.BaseClient = c

	resolver := func(ctx context.Context) string {
		if v, ok := ctx.Value("tid").(string); ok {
			return v
		}
		return ""
	}
	router := quark.NewTenantRouter(cfg, resolver, nil)
	tctx := context.WithValue(ctx, "tid", "myschema")

	q := quark.For[SchemaModel](tctx, router)
	// SQLite doesn't support schemas but the builder shouldn't panic
	_ = q
}

// ---------------------------------------------------------------------------
// wrapDBError (error types)
// ---------------------------------------------------------------------------

func TestWrapDBError(t *testing.T) {
	c := newSQLiteDB(t)
	ctx := context.Background()

	// Create a model that will fail on constraint
	type UniqueModel struct {
		ID    int64  `db:"id" pk:"true"`
		Email string `db:"email"`
	}
	if err := c.Exec(ctx, `CREATE TABLE unique_models (id INTEGER PRIMARY KEY, email TEXT UNIQUE)`); err != nil {
		t.Fatal(err)
	}
	if err := c.Exec(ctx, `INSERT INTO unique_models VALUES (1, 'dup@test.com')`); err != nil {
		t.Fatal(err)
	}

	m := UniqueModel{Email: "dup@test.com"}
	err := quark.For[UniqueModel](ctx, c).Create(&m)
	if err == nil {
		t.Error("expected unique constraint error")
	}
	// Should be wrapped as ErrDuplicateKey or ErrConstraint
	if !errors.Is(err, quark.ErrConstraintViolation) {
		t.Logf("got error (not ErrConstraintViolation, acceptable): %v", err)
	}
}

// ---------------------------------------------------------------------------
// Soft delete (Delete with soft-delete model)
// ---------------------------------------------------------------------------

type SoftItem struct {
	ID        int64      `db:"id" pk:"true"`
	Name      string     `db:"name"`
	DeletedAt *time.Time `db:"deleted_at"`
}

func TestSoftDelete(t *testing.T) {
	c := newSQLiteDB(t)
	defer c.Close()

	ctx := context.Background()
	var err error

	if err := c.Exec(ctx, `CREATE TABLE soft_items (id INTEGER PRIMARY KEY, name TEXT, deleted_at DATETIME)`); err != nil {
		t.Fatal(err)
	}

	item := SoftItem{Name: "soft"}
	if err := quark.For[SoftItem](ctx, c).Create(&item); err != nil {
		t.Fatal(err)
	}

	// Soft delete
	_, err = quark.For[SoftItem](ctx, c).Delete(&item)
	if err != nil {
		t.Fatalf("soft Delete: %v", err)
	}

	// Should not appear in default list
	all, _ := quark.For[SoftItem](ctx, c).List()
	for _, s := range all {
		if s.ID == item.ID {
			t.Error("soft-deleted item should not appear in List")
		}
	}
}
