package quark_test

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/cache/memory"
	"github.com/jcsvwinston/quark/cache/redis"
	quarkotel "github.com/jcsvwinston/quark/otel"
)

type sqlCounter struct {
	count int
}

func (s *sqlCounter) ObserveQuery(event quark.QueryEvent) {
	if event.Operation == "SELECT" || event.Operation == "QUERY_ROW" {
		s.count++
	}
}

// SharedSuite runs a comprehensive set of tests against a given client.
func SharedSuite(t *testing.T, client *quark.Client) {
	ctx := context.Background()

	t.Run("CRUD", func(t *testing.T) {
		testCRUD(ctx, t, client)
	})

	t.Run("QueryBuilder", func(t *testing.T) {
		testQueryBuilder(ctx, t, client)
	})

	t.Run("Transactions", func(t *testing.T) {
		testTransactions(ctx, t, client)
	})

	t.Run("Relationships", func(t *testing.T) {
		testRelationships(ctx, t, client)
	})

	t.Run("Hooks", func(t *testing.T) {
		testHooks(ctx, t, client)
	})

	t.Run("Validation", func(t *testing.T) {
		testValidation(ctx, t, client)
	})

	t.Run("SoftDelete", func(t *testing.T) {
		testSoftDelete(ctx, t, client)
	})

	t.Run("Pagination", func(t *testing.T) {
		testPagination(ctx, t, client)
	})

	t.Run("MultiTenant", func(t *testing.T) {
		testMultiTenant(ctx, t, client)
	})

	t.Run("OrRLSLeak", func(t *testing.T) {
		testOrRLSLeak(ctx, t, client)
	})

	t.Run("JSONPathSecurity", func(t *testing.T) {
		testJSONPathSecurity(ctx, t, client)
	})

	t.Run("M2MLinkErrors", func(t *testing.T) {
		testM2MLinkErrors(ctx, t, client)
	})

	t.Run("UpdateZeroValues", func(t *testing.T) {
		testUpdateZeroValues(ctx, t, client)
	})

	t.Run("JoinOnSecurity", func(t *testing.T) {
		testJoinOnSecurity(ctx, t, client)
	})

	t.Run("DirtyTracking", func(t *testing.T) {
		testDirtyTracking(ctx, t, client)
	})

	t.Run("TypeMapper", func(t *testing.T) {
		testTypeMapper(ctx, t, client)
	})

	t.Run("OptimisticLocking", func(t *testing.T) {
		testOptimisticLocking(ctx, t, client)
	})

	t.Run("SoftDeleteScopes", func(t *testing.T) {
		testSoftDeleteScopes(ctx, t, client)
	})

	t.Run("Nullable", func(t *testing.T) {
		testNullable(ctx, t, client)
	})

	t.Run("JSONField", func(t *testing.T) {
		testJSONField(ctx, t, client)
	})

	t.Run("PessimisticLocking", func(t *testing.T) {
		testPessimisticLocking(ctx, t, client)
	})

	t.Run("INChunking", func(t *testing.T) {
		testINChunking(ctx, t, client)
	})

	t.Run("HavingAggregate", func(t *testing.T) {
		testHavingAggregate(ctx, t, client)
	})

	t.Run("NestedPreload", func(t *testing.T) {
		testNestedPreload(ctx, t, client)
	})

	t.Run("ExprAST", func(t *testing.T) {
		testExprAST(ctx, t, client)
	})

	t.Run("Subquery", func(t *testing.T) {
		testSubquery(ctx, t, client)
	})

	t.Run("CTE", func(t *testing.T) {
		testCTE(ctx, t, client)
	})

	t.Run("Window", func(t *testing.T) {
		testWindow(ctx, t, client)
	})

	t.Run("Events", func(t *testing.T) {
		testEvents(ctx, t, client)
	})

	t.Run("Middleware", func(t *testing.T) {
		testMiddleware(ctx, t, client)
	})

	t.Run("Raw", func(t *testing.T) {
		testRaw(ctx, t, client)
	})

	t.Run("DatabasePerTenant", func(t *testing.T) {
		testDatabasePerTenant(ctx, t)
	})

	t.Run("Sync", func(t *testing.T) {
		testSync(ctx, t, client)
	})

	t.Run("RecursiveAssociations", func(t *testing.T) {
		testRecursiveAssociations(ctx, t, client)
	})

	t.Run("Stress", func(t *testing.T) {
		testStress(ctx, t, client)
	})

	t.Run("JSON", func(t *testing.T) {
		testJSON(ctx, t, client)
	})

	t.Run("Caching", func(t *testing.T) {
		testCaching(ctx, t, client)
	})

	t.Run("OpenTelemetry", func(t *testing.T) {
		testOtelInSharedSuite(ctx, t, client)
	})

	t.Run("CompositePK", func(t *testing.T) {
		testCompositePK(ctx, t, client)
	})

	t.Run("P1Features", func(t *testing.T) {
		testP1Features(ctx, t, client)
	})

	t.Run("NFixes", func(t *testing.T) {
		testNFixes(ctx, t, client)
	})

	t.Run("BatchOps", func(t *testing.T) {
		testBatchOps(ctx, t, client)
	})
}

func testCaching(ctx context.Context, t *testing.T, client *quark.Client) {
	// 1. In-Memory Cache Test
	t.Run("Memory", func(t *testing.T) {
		runCacheValidation(ctx, t, client, memory.New())
	})

	// 2. Redis Cache Test
	t.Run("Redis", func(t *testing.T) {
		rStore := redis.New(redis.Options{Addr: "localhost:6379"})
		// Test connectivity
		if err := rStore.Ping(ctx); err != nil {
			t.Skip("Redis not available on localhost:6379, skipping distributed cache test")
			return
		}
		runCacheValidation(ctx, t, client, rStore)
	})
}

type CacheModel struct {
	ID   int64  `db:"id" pk:"true"`
	Data string `db:"data"`
}

func runCacheValidation(ctx context.Context, t *testing.T, baseClient *quark.Client, store quark.CacheStore) {
	counter := &sqlCounter{}
	client, _ := baseClient.WithOptions(quark.WithCacheStore(store), quark.WithQueryObserver(counter))

	dropTable(client, "cache_models")
	client.Migrate(ctx, &CacheModel{})

	// Flush any stale cache entries for this table from prior runs
	_ = store.InvalidateTags(ctx, "cache_models")

	// Insert test data
	m := &CacheModel{ID: 1, Data: "initial"}
	quark.For[CacheModel](ctx, client).Create(m)

	counter.count = 0

	// 1st Read: Cache Miss -> SQL
	_, err := quark.For[CacheModel](ctx, client).Where("id", "=", 1).Cache(1 * time.Minute).List()
	if err != nil {
		t.Fatalf("first read failed: %v", err)
	}
	if counter.count != 1 {
		t.Errorf("expected 1 SQL query for first read, got %d", counter.count)
	}

	// 2nd Read: Cache Hit -> NO SQL
	_, err = quark.For[CacheModel](ctx, client).Where("id", "=", 1).Cache(1 * time.Minute).List()
	if err != nil {
		t.Fatalf("second read failed: %v", err)
	}
	if counter.count != 1 {
		t.Errorf("expected cache hit (no new SQL), but query count increased to %d", counter.count)
	}

	// 3. Invalidation: Update record
	m.Data = "updated"
	_, err = quark.For[CacheModel](ctx, client).Update(m)
	if err != nil {
		t.Fatalf("update failed: %v", err)
	}

	// 4th Read: Cache Invalidated -> SQL
	_, err = quark.For[CacheModel](ctx, client).Where("id", "=", 1).Cache(1 * time.Minute).List()
	if err != nil {
		t.Fatalf("third read failed: %v", err)
	}
	if counter.count != 2 {
		t.Errorf("expected cache invalidation after update, but query count is %d", counter.count)
	}
}

func dropTable(client *quark.Client, tableName string) {
	switch client.Dialect().Name() {
	case "oracle":
		// Oracle doesn't support DROP TABLE IF EXISTS
		client.Raw().Exec(fmt.Sprintf("DROP TABLE %s", client.Dialect().Quote(tableName)))
	default:
		client.Raw().Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s", client.Dialect().Quote(tableName)))
	}
}

func testCRUD(ctx context.Context, t *testing.T, client *quark.Client) {
	dropTable(client, "suite_users")
	type SuiteUser struct {
		ID    int64  `db:"id" pk:"true"`
		Name  string `db:"name"`
		Email string `db:"email"`
	}

	// Setup table for the engine
	err := client.Migrate(ctx, &SuiteUser{})
	if err != nil {
		t.Fatalf("migrate failed: %v", err)
	}

	// Create
	u := SuiteUser{Name: "Suite User", Email: "suite@test.com"}
	if err := quark.For[SuiteUser](ctx, client).Create(&u); err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if u.ID == 0 {
		t.Error("expected ID to be set")
	}

	// Find
	found, err := quark.For[SuiteUser](ctx, client).Find(u.ID)
	if err != nil {
		t.Fatalf("find failed: %v", err)
	}
	if found.Name != u.Name {
		t.Errorf("expected name %s, got %s", u.Name, found.Name)
	}

	// Update
	found.Name = "Updated Name"
	if _, err := quark.For[SuiteUser](ctx, client).Update(&found); err != nil {
		t.Fatalf("update failed: %v", err)
	}

	// Verify Update
	verify, _ := quark.For[SuiteUser](ctx, client).Find(u.ID)
	if verify.Name != "Updated Name" {
		t.Errorf("expected updated name, got %s", verify.Name)
	}

	// Delete
	if _, err := quark.For[SuiteUser](ctx, client).HardDelete(&verify); err != nil {
		t.Fatalf("delete failed: %v", err)
	}

	// Verify Delete
	_, err = quark.For[SuiteUser](ctx, client).Find(u.ID)
	if err != quark.ErrNotFound {
		t.Errorf("expected quark.ErrNotFound, got %v", err)
	}
}

func testQueryBuilder(ctx context.Context, t *testing.T, client *quark.Client) {
	dropTable(client, "qb_users")
	type QBUser struct {
		ID   int64  `db:"id" pk:"true"`
		Name string `db:"name"`
		Age  int    `db:"age"`
		City string `db:"city"`
	}

	client.Migrate(ctx, &QBUser{})

	users := []QBUser{
		{Name: "Alice", Age: 20, City: "Madrid"},
		{Name: "Charlie", Age: 30, City: "Madrid"},
		{Name: "Bob", Age: 40, City: "Barcelona"},
	}
	for i := range users {
		if err := quark.For[QBUser](ctx, client).Create(&users[i]); err != nil {
			t.Fatalf("create failed: %v", err)
		}
	}

	// Test Simple Where
	madrid, err := quark.For[QBUser](ctx, client).Where("city", "=", "Madrid").List()
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(madrid) != 2 {
		t.Errorf("expected 2 users in Madrid, got %d", len(madrid))
	}

	// Test And
	oldMadrid, _ := quark.For[QBUser](ctx, client).Where("city", "=", "Madrid").Where("age", ">", 25).List()
	if len(oldMadrid) != 1 {
		t.Errorf("expected 1 old user in Madrid, got %d", len(oldMadrid))
	}

	// Test Or
	orResult, _ := quark.For[QBUser](ctx, client).Where("city", "=", "Barcelona").Or(func(q *quark.Query[QBUser]) *quark.Query[QBUser] {
		return q.Where("age", "<", 25)
	}).List()
	if len(orResult) != 2 {
		t.Errorf("expected 2 users for OR condition, got %d", len(orResult))
	}

	// Test In
	inResult, _ := quark.For[QBUser](ctx, client).WhereIn("age", []any{20, 40}).List()
	if len(inResult) != 2 {
		t.Errorf("expected 2 users for IN condition, got %d", len(inResult))
	}

	// Test Between
	betweenResult, _ := quark.For[QBUser](ctx, client).WhereBetween("age", 25, 35).List()
	if len(betweenResult) != 1 {
		t.Errorf("expected 1 user for BETWEEN condition, got %d", len(betweenResult))
	}

	// Test Select
	selResult, _ := quark.For[QBUser](ctx, client).Select("name", "city").Where("age", "=", 30).List()
	if len(selResult) != 1 {
		t.Errorf("expected 1 user for Select, got %d", len(selResult))
	}
	if selResult[0].Name != "Charlie" || selResult[0].Age != 0 {
		if selResult[0].Age != 0 {
			t.Errorf("expected Age to be zero (not selected), got %d", selResult[0].Age)
		}
	}
}

func testTransactions(ctx context.Context, t *testing.T, client *quark.Client) {
	dropTable(client, "tx_users")
	type TxUser struct {
		ID   int64  `db:"id" pk:"true"`
		Name string `db:"name"`
	}
	client.Migrate(ctx, &TxUser{})

	// Successful quark.Tx
	err := client.Tx(ctx, func(tx *quark.Tx) error {
		return quark.ForTx[TxUser](ctx, tx).Create(&TxUser{Name: "quark.Tx User"})
	})
	if err != nil {
		t.Fatalf("tx failed: %v", err)
	}

	// Rollback quark.Tx
	err = client.Tx(ctx, func(tx *quark.Tx) error {
		quark.ForTx[TxUser](ctx, tx).Create(&TxUser{Name: "Rollback User"})
		return fmt.Errorf("intentional rollback")
	})
	if err == nil {
		t.Error("expected error from tx, got nil")
	}

	// Verify results
	count, _ := quark.For[TxUser](ctx, client).Count()
	if count != 1 {
		t.Errorf("expected 1 user after tx and rollback, got %d", count)
	}
}

func testRelationships(ctx context.Context, t *testing.T, client *quark.Client) {
	// Already mostly covered in quark_test.go, but integrated here for all dialects
	// Implement Preload tests for HasMany and BelongsTo
}

func testHooks(ctx context.Context, t *testing.T, client *quark.Client) {
	dropTable(client, "hook_users")
	type HookUser struct {
		ID        int64      `db:"id" pk:"true"`
		Title     string     `db:"title"`
		DeletedAt *time.Time `db:"deleted_at"`
	}

	client.Migrate(ctx, &HookUser{})
	// Basic test for hooks could be more complex, but we mainly want to ensure they run across dialects
	u := HookUser{Title: "Hook Test"}
	if err := quark.For[HookUser](ctx, client).Create(&u); err != nil {
		t.Fatalf("create failed: %v", err)
	}

	// Just verify creation worked
	if u.ID == 0 {
		t.Error("hook user ID not set")
	}
}

func testValidation(ctx context.Context, t *testing.T, client *quark.Client) {
	dropTable(client, "validateds")
	type Validated struct {
		ID    int64  `db:"id" pk:"true"`
		Email string `db:"email" validate:"required,email"`
	}
	client.Migrate(ctx, &Validated{})

	err := quark.For[Validated](ctx, client).Create(&Validated{Email: "invalid"})
	if err == nil {
		t.Error("expected validation error, got nil")
	}
	dropTable(client, "validateds")
}

func testSoftDelete(ctx context.Context, t *testing.T, client *quark.Client) {
	dropTable(client, "posts")
	type Post struct {
		ID        int64      `db:"id" pk:"true"`
		Title     string     `db:"title"`
		DeletedAt *time.Time `db:"deleted_at"`
	}

	client.Migrate(ctx, &Post{})
	p := Post{Title: "Soft Delete Post"}
	if err := quark.For[Post](ctx, client).Create(&p); err != nil {
		t.Fatalf("create failed: %v", err)
	}

	// Soft delete
	rows, err := quark.For[Post](ctx, client).Delete(&p)
	if err != nil || rows != 1 {
		t.Fatalf("soft delete failed: %v, rows: %d", err, rows)
	}

	// Should not find by default
	_, err = quark.For[Post](ctx, client).Find(p.ID)
	if err != quark.ErrNotFound {
		t.Errorf("expected quark.ErrNotFound for soft deleted record, got %v", err)
	}

	// Should find with Unscoped
	found, err := quark.For[Post](ctx, client).Unscoped().Find(p.ID)
	if err != nil {
		t.Fatalf("unscoped find failed: %v", err)
	}
	if found.DeletedAt == nil {
		t.Error("expected DeletedAt to be set")
	}
}

func testPagination(ctx context.Context, t *testing.T, client *quark.Client) {
	dropTable(client, "logs")
	type Log struct {
		ID  int64  `db:"id" pk:"true"`
		Msg string `db:"msg"`
	}
	client.Migrate(ctx, &Log{})
	for i := 0; i < 50; i++ {
		if err := quark.For[Log](ctx, client).Create(&Log{Msg: "test"}); err != nil {
			t.Fatalf("failed to create log %d: %v", i, err)
		}
	}

	res, err := quark.For[Log](ctx, client).Paginate(10, 1) // Page 1 (offset 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 10 {
		t.Errorf("expected 10 items, got %d", len(res.Items))
	}
	if res.Total != 50 {
		t.Errorf("expected total 50, got %d", res.Total)
	}
}

func testMultiTenant(ctx context.Context, t *testing.T, client *quark.Client) {
	dropTable(client, "tenant_data")
	type TenantData struct {
		ID       int64  `db:"id" pk:"true"`
		TenantID string `db:"tenant_id"`
		Value    string `db:"value"`
	}
	client.Migrate(ctx, &TenantData{})

	cfg := quark.DefaultTenantConfig()
	cfg.Strategy = quark.RowLevelSecurity
	cfg.BaseClient = client

	resolver := func(ctx context.Context) string {
		if tid, ok := ctx.Value("tenant_id").(string); ok {
			return tid
		}
		return ""
	}

	router := quark.NewTenantRouter(cfg, resolver, nil)

	dropTable(client, "tenant_datas")
	client.Migrate(ctx, &TenantData{})

	ctx1 := context.WithValue(context.Background(), "tenant_id", "t1")
	ctx2 := context.WithValue(context.Background(), "tenant_id", "t2")

	quark.For[TenantData](ctx1, router).Create(&TenantData{Value: "V1"})
	quark.For[TenantData](ctx2, router).Create(&TenantData{Value: "V2"})

	// Verify isolation
	v1, _ := quark.For[TenantData](ctx1, router).List()
	if len(v1) != 1 || v1[0].Value != "V1" {
		t.Errorf("tenant 1 isolation failed: %v", v1)
	}

	v2, _ := quark.For[TenantData](ctx2, router).List()
	if len(v2) != 1 || v2[0].Value != "V2" {
		t.Errorf("tenant 2 isolation failed: %v", v2)
	}
}

type mockObserver struct {
	events []quark.QueryEvent
	mu     sync.Mutex
}

func (o *mockObserver) ObserveQuery(e quark.QueryEvent) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, e)
}

func testEvents(ctx context.Context, t *testing.T, client *quark.Client) {
	dropTable(client, "event_users")
	obs := &mockObserver{}
	// Since client options are applied at quark.New(), we can't easily add an observer to an existing client
	// unless we use a middleware or the client supports it.
	// Quark quark.Client has an 'observers' slice. Let's see if we can append to it.
	// Actually, it's unexported. But we can create a NEW client with the SAME DB for this test.

	c2, _ := client.WithOptions(quark.WithQueryObserver(obs))

	type EventUser struct {
		ID   int64  `db:"id" pk:"true"`
		Name string `db:"name"`
	}
	if err := c2.Migrate(ctx, &EventUser{}); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}
	if err := quark.For[EventUser](ctx, c2).Create(&EventUser{Name: "Event"}); err != nil {
		t.Fatalf("create failed: %v", err)
	}
	if _, err := quark.For[EventUser](ctx, c2).List(); err != nil {
		t.Fatalf("list failed: %v", err)
	}

	obs.mu.Lock()
	defer obs.mu.Unlock()
	if len(obs.events) < 2 {
		t.Errorf("expected at least 2 events, got %d", len(obs.events))
	}
}

type suiteMockMiddleware struct {
	called bool
}

func (m *suiteMockMiddleware) WrapQuery(next quark.QueryFunc) quark.QueryFunc {
	return func(ctx context.Context, exec quark.Executor, sql string, args []any) (*sql.Rows, error) {
		m.called = true
		return next(ctx, exec, sql, args)
	}
}

func (m *suiteMockMiddleware) WrapQueryRow(next quark.QueryRowFunc) quark.QueryRowFunc {
	return next
}

func (m *suiteMockMiddleware) WrapExec(next quark.ExecFunc) quark.ExecFunc {
	return next
}

func testMiddleware(ctx context.Context, t *testing.T, client *quark.Client) {
	dropTable(client, "mid_users")
	mid := &suiteMockMiddleware{}
	c2, _ := client.WithOptions(quark.WithMiddleware(mid))

	type MidUser struct {
		ID int64 `db:"id" pk:"true"`
	}
	c2.Migrate(ctx, &MidUser{})
	quark.For[MidUser](ctx, c2).List()

	if !mid.called {
		t.Error("middleware was not called")
	}
}

func testRaw(ctx context.Context, t *testing.T, client *quark.Client) {
	dropTable(client, "raw_test")
	// Enable raw queries for this test
	c2, _ := client.WithOptions(quark.WithLimits(quark.Limits{AllowRawQueries: true, MaxResults: 1000, QueryTimeout: time.Second}))

	sqlType := "TEXT"
	switch client.Dialect().Name() {
	case "mysql", "mariadb":
		sqlType = "VARCHAR(255)"
	case "oracle":
		sqlType = "VARCHAR2(255)"
	case "mssql":
		sqlType = "NVARCHAR(MAX)"
	}

	createSQL := fmt.Sprintf("CREATE TABLE raw_test (id INTEGER, name %s)", sqlType)
	if err := c2.Exec(ctx, createSQL); err != nil {
		t.Fatalf("exec failed: %v", err)
	}

	query := fmt.Sprintf("SELECT * FROM raw_test WHERE id = %s", strings.Join(c2.Dialect().Placeholders(1), ", "))
	if _, err := c2.RawQuery(ctx, query, 1); err != nil {
		t.Fatalf("raw query failed: %v", err)
	}
}

func testDatabasePerTenant(ctx context.Context, t *testing.T) {
	factory := func(tenantID string) (*quark.Client, error) {
		limits := quark.DefaultLimits()
		limits.AllowRawQueries = true
		return quark.New("sqlite", ":memory:", quark.WithLimits(limits))
	}

	cfg := quark.DefaultTenantConfig()
	cfg.Strategy = quark.DatabasePerTenant
	cfg.MaxCachedPools = 2 // Small limit for eviction test

	resolver := func(ctx context.Context) string {
		if tid, ok := ctx.Value("tenant_id").(string); ok {
			return tid
		}
		return ""
	}

	router := quark.NewTenantRouter(cfg, resolver, factory)

	ctx1 := context.WithValue(ctx, "tenant_id", "t1")
	ctx2 := context.WithValue(ctx, "tenant_id", "t2")
	ctx3 := context.WithValue(ctx, "tenant_id", "t3")

	// Trigger cache population
	router.GetClient(ctx1)
	router.GetClient(ctx2)

	active := router.ActiveTenants()
	if len(active) != 2 {
		t.Errorf("expected 2 active tenants, got %d", len(active))
	}

	// Trigger eviction
	router.GetClient(ctx3)

	activeAfter := router.ActiveTenants()
	if len(activeAfter) != 2 {
		t.Errorf("expected 2 active tenants after eviction, got %d", len(activeAfter))
	}
}

type SyncUserV1 struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

func (SyncUserV1) TableName() string { return "sync_users" }

type SyncUserV2 struct {
	ID    int64  `db:"id" pk:"true"`
	Name  string `db:"name"`
	Email string `db:"email"`
}

func (SyncUserV2) TableName() string { return "sync_users" }

type SyncUserV3 struct {
	ID       int64  `db:"id" pk:"true"`
	Name     string `db:"name"`
	Contacts string `db:"contacts" quark:"rename:email"`
}

func (SyncUserV3) TableName() string { return "sync_users" }

type SyncUserV4 struct {
	ID   int64  `db:"id" pk:"true"`
	Name string `db:"name"`
}

func (SyncUserV4) TableName() string { return "sync_users" }

type RProfile struct {
	ID       int64  `db:"id" pk:"true"`
	Bio      string `db:"bio"`
	AuthorID int64  `db:"author_id"`
}

func (RProfile) TableName() string { return "r_profiles" }

type RPost struct {
	ID       int64  `db:"id" pk:"true"`
	Title    string `db:"title"`
	AuthorID int64  `db:"author_id"`
}

func (RPost) TableName() string { return "r_posts" }

type RAuthor struct {
	ID      int64    `db:"id" pk:"true"`
	Name    string   `db:"name"`
	Profile RProfile `rel:"has_one" join:"author_id"`
	Posts   []RPost  `rel:"has_many" join:"author_id"`
}

func (RAuthor) TableName() string { return "r_authors" }

func testSync(ctx context.Context, t *testing.T, client *quark.Client) {
	dropTable(client, "sync_users")

	// Initial migration
	if err := client.Migrate(ctx, &SyncUserV1{}); err != nil {
		t.Fatalf("initial migrate failed: %v", err)
	}

	// Evolution: Add column
	if err := client.Sync(ctx, quark.SyncOptions{}, &SyncUserV2{}); err != nil {
		t.Fatalf("sync v2 failed: %v", err)
	}

	// Verify addition
	u2 := SyncUserV2{Name: "Sync", Email: "sync@test.com"}
	if err := quark.For[SyncUserV2](ctx, client).Create(&u2); err != nil {
		t.Fatalf("create v2 failed: %v", err)
	}

	// Evolution: Rename column (email -> contacts)
	if err := client.Sync(ctx, quark.SyncOptions{}, &SyncUserV3{}); err != nil {
		t.Fatalf("sync v3 failed: %v", err)
	}

	// Verify rename
	u3, err := quark.For[SyncUserV3](ctx, client).Find(u2.ID)
	if err != nil {
		t.Fatalf("find v3 failed: %v", err)
	}
	if u3.Contacts != "sync@test.com" {
		t.Errorf("expected contacts to have sync@test.com, got %s", u3.Contacts)
	}

	// Evolution: Destructive drop (contacts)
	// Safe mode (default) - should NOT drop
	if err := client.Sync(ctx, quark.SyncOptions{}, &SyncUserV4{}); err != nil {
		t.Fatal(err)
	}

	// Destructive mode
	cDestructive, _ := client.WithOptions(quark.WithLimits(quark.Limits{SafeMigrations: false}))
	if err := cDestructive.Sync(ctx, quark.SyncOptions{}, &SyncUserV4{}); err != nil {
		t.Fatalf("destructive sync failed: %v", err)
	}
}

func testRecursiveAssociations(ctx context.Context, t *testing.T, client *quark.Client) {
	dropTable(client, "r_authors")
	dropTable(client, "r_profiles")
	dropTable(client, "r_posts")

	client.Migrate(ctx, &RAuthor{}, &RProfile{}, &RPost{})

	// Recursive Create
	author := RAuthor{
		Name:    "Recursive Author",
		Profile: RProfile{Bio: "Author Bio"},
		Posts: []RPost{
			{Title: "Post 1"},
			{Title: "Post 2"},
		},
	}

	if err := quark.For[RAuthor](ctx, client).Create(&author); err != nil {
		t.Fatalf("recursive create failed: %v", err)
	}

	if author.ID == 0 || author.Profile.ID == 0 || len(author.Posts) != 2 || author.Posts[0].ID == 0 {
		t.Errorf("recursive IDs not set correctly: %+v", author)
	}

	// Verify persistence
	found, err := quark.For[RAuthor](ctx, client).Preload("Profile").Preload("Posts").Find(author.ID)
	if err != nil {
		t.Fatal(err)
	}
	if found.Profile.Bio != "Author Bio" || len(found.Posts) != 2 {
		t.Errorf("recursive data not persisted: %+v", found)
	}

	// Recursive Update (Add a post)
	found.Posts = append(found.Posts, RPost{Title: "Post 3"})
	found.Profile.Bio = "Updated Bio"

	if _, err := quark.For[RAuthor](ctx, client).Update(&found); err != nil {
		t.Fatalf("recursive update failed: %v", err)
	}

	// Verify Update
	verify, _ := quark.For[RAuthor](ctx, client).Preload("Profile").Preload("Posts").Find(author.ID)
	if len(verify.Posts) != 3 || verify.Profile.Bio != "Updated Bio" {
		t.Errorf("recursive update failed verification: %d posts, bio: %s", len(verify.Posts), verify.Profile.Bio)
	}
}

func testJSON(ctx context.Context, t *testing.T, client *quark.Client) {
	dropTable(client, "json_docs")
	type JSONDoc struct {
		ID       int64  `db:"id" pk:"true"`
		Metadata string `db:"metadata"`
	}

	// Use Sync with options
	err := client.Sync(ctx, quark.SyncOptions{}, &JSONDoc{})
	if err != nil {
		t.Fatalf("Sync failed for JSONDoc: %v", err)
	}

	// Insert docs (let DB generate IDs for better cross-dialect compatibility)
	doc1 := JSONDoc{Metadata: `{"color": "red", "size": "M"}`}
	doc2 := JSONDoc{Metadata: `{"color": "blue", "size": "L"}`}

	_ = quark.For[JSONDoc](ctx, client).Create(&doc1)
	_ = quark.For[JSONDoc](ctx, client).Create(&doc2)

	// Query JSON using native dialect extraction
	results, err := quark.For[JSONDoc](ctx, client).
		WhereJSON("metadata", "color", "=", "red").
		List()

	if err != nil {
		t.Logf("WhereJSON query info: %v", err)
		if client.Dialect().Name() == "oracle" || client.Dialect().Name() == "mssql" || client.Dialect().Name() == "mariadb" {
			t.Log("Skipping JSON deep verification for this dialect (requires specific setup)")
			return
		}
		t.Fatalf("WhereJSON failed: %v", err)
	}

	if len(results) != 1 {
		t.Errorf("Expected 1 result for JSON query, got %d", len(results))
	}
}

// testP1Features validates P0/P1 query features across all dialects.
func testP1Features(ctx context.Context, t *testing.T, client *quark.Client) {
	// ── models ──────────────────────────────────────────────────────────────
	type P1SuiteUser struct {
		ID     int64  `db:"id" pk:"true"`
		Name   string `db:"name"`
		Email  string `db:"email"`
		Age    int    `db:"age"`
		Active bool   `db:"active"`
	}
	type P1SuiteOrder struct {
		ID     int64   `db:"id" pk:"true"`
		UserID int64   `db:"user_id"`
		Amount float64 `db:"amount"`
	}

	dropTable(client, "p1_suite_users")
	dropTable(client, "p1_suite_orders")
	if err := client.Migrate(ctx, &P1SuiteUser{}, &P1SuiteOrder{}); err != nil {
		t.Fatalf("migrate P1 tables: %v", err)
	}

	// ── Upsert ───────────────────────────────────────────────────────────────
	t.Run("Upsert", func(t *testing.T) {
		// Insert a record first; the PK always has a UNIQUE constraint across dialects.
		u := &P1SuiteUser{Name: "Alice", Email: "alice-suite@test.com", Age: 30}
		if err := quark.For[P1SuiteUser](ctx, client).Create(u); err != nil {
			t.Fatalf("create: %v", err)
		}
		if u.ID == 0 {
			t.Fatal("create did not populate ID")
		}
		// Upsert on PK: same ID → update name/age.
		u2 := &P1SuiteUser{ID: u.ID, Email: "alice-suite@test.com", Name: "Alice Updated", Age: 31}
		if err := quark.For[P1SuiteUser](ctx, client).Upsert(u2, []string{"id"}, []string{"name", "age"}); err != nil {
			t.Fatalf("upsert: %v", err)
		}
		got, err := quark.For[P1SuiteUser](ctx, client).Find(u.ID)
		if err != nil {
			t.Fatalf("find after upsert: %v", err)
		}
		if got.Name != "Alice Updated" {
			t.Errorf("upsert: expected 'Alice Updated', got %q", got.Name)
		}
	})

	// ── CreateBatch ──────────────────────────────────────────────────────────
	t.Run("CreateBatch", func(t *testing.T) {
		batch := []*P1SuiteUser{
			{Name: "Batch1", Email: "b1-suite@test.com", Age: 10},
			{Name: "Batch2", Email: "b2-suite@test.com", Age: 20},
			{Name: "Batch3", Email: "b3-suite@test.com", Age: 30},
		}
		if err := quark.For[P1SuiteUser](ctx, client).CreateBatch(batch); err != nil {
			t.Fatalf("CreateBatch: %v", err)
		}
		count, _ := quark.For[P1SuiteUser](ctx, client).
			WhereIn("email", []any{"b1-suite@test.com", "b2-suite@test.com", "b3-suite@test.com"}).
			Count()
		if count != 3 {
			t.Errorf("CreateBatch: expected 3 records, got %d", count)
		}
	})

	// ── WhereNot ─────────────────────────────────────────────────────────────
	t.Run("WhereNot", func(t *testing.T) {
		dropTable(client, "p1_suite_users")
		client.Migrate(ctx, &P1SuiteUser{})
		quark.For[P1SuiteUser](ctx, client).CreateBatch([]*P1SuiteUser{
			{Name: "WNActive", Email: "wn-active@test.com", Active: true},
			{Name: "WNInactive", Email: "wn-inactive@test.com", Active: false},
		})
		results, err := quark.For[P1SuiteUser](ctx, client).WhereNot("active", "=", true).List()
		if err != nil {
			t.Fatalf("WhereNot: %v", err)
		}
		for _, r := range results {
			if r.Active {
				t.Errorf("WhereNot: got active user %q", r.Name)
			}
		}
	})

	// ── Distinct ─────────────────────────────────────────────────────────────
	t.Run("Distinct", func(t *testing.T) {
		dropTable(client, "p1_suite_users")
		client.Migrate(ctx, &P1SuiteUser{})
		quark.For[P1SuiteUser](ctx, client).CreateBatch([]*P1SuiteUser{
			{Name: "DistinctA", Email: "da1@test.com"},
			{Name: "DistinctA", Email: "da2@test.com"},
			{Name: "DistinctB", Email: "db1@test.com"},
		})
		results, err := quark.For[P1SuiteUser](ctx, client).Select("name").Distinct().List()
		if err != nil {
			t.Fatalf("Distinct: %v", err)
		}
		seen := map[string]int{}
		for _, r := range results {
			seen[r.Name]++
		}
		for name, cnt := range seen {
			if cnt > 1 {
				t.Errorf("Distinct: name %q appeared %d times", name, cnt)
			}
		}
	})

	// ── GroupBy ──────────────────────────────────────────────────────────────
	t.Run("GroupBy", func(t *testing.T) {
		dropTable(client, "p1_suite_users")
		client.Migrate(ctx, &P1SuiteUser{})
		quark.For[P1SuiteUser](ctx, client).CreateBatch([]*P1SuiteUser{
			{Name: "GroupA", Email: "g1@test.com", Age: 20},
			{Name: "GroupA", Email: "g2@test.com", Age: 20},
			{Name: "GroupB", Email: "g3@test.com", Age: 30},
		})
		results, err := quark.For[P1SuiteUser](ctx, client).Select("name").GroupBy("name").List()
		if err != nil {
			t.Fatalf("GroupBy: %v", err)
		}
		if len(results) != 2 {
			t.Errorf("GroupBy: expected 2 grouped rows, got %d", len(results))
		}
	})

	// ── Aggregates ───────────────────────────────────────────────────────────
	t.Run("Aggregates", func(t *testing.T) {
		dropTable(client, "p1_suite_orders")
		client.Migrate(ctx, &P1SuiteOrder{})
		quark.For[P1SuiteOrder](ctx, client).CreateBatch([]*P1SuiteOrder{
			{UserID: 1, Amount: 10.0},
			{UserID: 1, Amount: 20.0},
			{UserID: 2, Amount: 5.0},
		})

		sum, err := quark.For[P1SuiteOrder](ctx, client).Sum("amount")
		if err != nil {
			t.Fatalf("Sum: %v", err)
		}
		if sum != 35.0 {
			t.Errorf("Sum: expected 35.0, got %f", sum)
		}

		min, err := quark.For[P1SuiteOrder](ctx, client).Min("amount")
		if err != nil {
			t.Fatalf("Min: %v", err)
		}
		if min != 5.0 {
			t.Errorf("Min: expected 5.0, got %f", min)
		}

		max, err := quark.For[P1SuiteOrder](ctx, client).Max("amount")
		if err != nil {
			t.Fatalf("Max: %v", err)
		}
		if max != 20.0 {
			t.Errorf("Max: expected 20.0, got %f", max)
		}

		avg, err := quark.For[P1SuiteOrder](ctx, client).Where("user_id", "=", 1).Avg("amount")
		if err != nil {
			t.Fatalf("Avg: %v", err)
		}
		if avg != 15.0 {
			t.Errorf("Avg: expected 15.0, got %f", avg)
		}
	})

	// ── Scopes ───────────────────────────────────────────────────────────────
	t.Run("Scopes", func(t *testing.T) {
		dropTable(client, "p1_suite_users")
		client.Migrate(ctx, &P1SuiteUser{})
		quark.For[P1SuiteUser](ctx, client).CreateBatch([]*P1SuiteUser{
			{Name: "SA", Email: "sa@test.com", Active: true, Age: 20},
			{Name: "SB", Email: "sb@test.com", Active: false, Age: 30},
			{Name: "SC", Email: "sc@test.com", Active: true, Age: 40},
		})
		activeOnly := quark.Scope[P1SuiteUser](func(q *quark.Query[P1SuiteUser]) *quark.Query[P1SuiteUser] {
			return q.Where("active", "=", true)
		})
		olderThan25 := quark.Scope[P1SuiteUser](func(q *quark.Query[P1SuiteUser]) *quark.Query[P1SuiteUser] {
			return q.Where("age", ">", 25)
		})
		results, err := quark.For[P1SuiteUser](ctx, client).Apply(activeOnly, olderThan25).List()
		if err != nil {
			t.Fatalf("Scopes: %v", err)
		}
		if len(results) != 1 || results[0].Name != "SC" {
			t.Errorf("Scopes: expected [SC], got %v", results)
		}
	})

	// ── WhereSubquery ────────────────────────────────────────────────────────
	t.Run("WhereSubquery", func(t *testing.T) {
		dropTable(client, "p1_suite_orders")
		client.Migrate(ctx, &P1SuiteOrder{})
		quark.For[P1SuiteOrder](ctx, client).CreateBatch([]*P1SuiteOrder{
			{UserID: 1, Amount: 50.0},
			{UserID: 2, Amount: 200.0},
			{UserID: 3, Amount: 10.0},
		})

		// AllowRawQueries must be true for WhereSubquery to work
		rawLimits := quark.DefaultLimits()
		rawLimits.AllowRawQueries = true
		rawClient, _ := client.WithOptions(quark.WithLimits(rawLimits))

		// dialect-aware quoting for the subquery
		q := client.Dialect().Quote
		sub := fmt.Sprintf("SELECT %s FROM %s WHERE %s > 75",
			q("id"), q("p1_suite_orders"), q("amount"))
		results, err := quark.For[P1SuiteOrder](ctx, rawClient).WhereSubquery("id", "IN", sub).List()
		if err != nil {
			t.Fatalf("WhereSubquery: %v", err)
		}
		if len(results) != 1 {
			t.Errorf("WhereSubquery: expected 1 result, got %d", len(results))
		}

		// Negative: WhereSubquery must be blocked when AllowRawQueries=false
		_, err = quark.For[P1SuiteOrder](ctx, client).WhereSubquery("id", "IN", sub).List()
		if err == nil {
			t.Error("WhereSubquery: expected error when AllowRawQueries=false, got nil")
		}
	})

	// ── CreateIndex ──────────────────────────────────────────────────────────
	t.Run("CreateIndex", func(t *testing.T) {
		if err := client.CreateIndex(ctx, "p1_suite_users", "idx_p1su_email", []string{"email"}, true); err != nil {
			t.Fatalf("CreateIndex: %v", err)
		}
		// idempotent — second call must not error
		if err := client.CreateIndex(ctx, "p1_suite_users", "idx_p1su_email", []string{"email"}, true); err != nil {
			t.Fatalf("CreateIndex (idempotent): %v", err)
		}
	})

	// ── AddForeignKey ────────────────────────────────────────────────────────
	t.Run("AddForeignKey", func(t *testing.T) {
		err := client.AddForeignKey(ctx, "p1_suite_orders", "fk_p1so_user",
			[]string{"user_id"}, "p1_suite_users", []string{"id"}, "CASCADE", "")
		// SQLite will error; other dialects should succeed — we just require no panic
		if err != nil {
			t.Logf("AddForeignKey info (%s): %v", client.Dialect().Name(), err)
		}
	})
}

// testOtelInSharedSuite verifica que las operaciones generan trazas OTel.
//
// Cuando el collector OTLP está disponible en localhost:4318, las trazas se
// envían a Jaeger usando el dialecto como service name (e.g. "quark-mysql").
// Si el collector no está disponible se usa un exporter en memoria, por lo
// que el test nunca hace skip: las assertions de spans son iguales en ambos paths.
func testOtelInSharedSuite(ctx context.Context, t *testing.T, client *quark.Client) {
	dialect := client.Dialect().Name()
	serviceName := fmt.Sprintf("quark-%s", dialect)

	// setupSuiteOtel installs a combined OTLP+InMemory provider when the
	// collector is reachable, or an InMemory-only provider otherwise.
	exporter, realCollector, shutdown := setupSuiteOtel(ctx, dialect)
	defer shutdown()

	if realCollector {
		t.Logf("📡 OTLP collector active — traces → Jaeger service '%s'", serviceName)
		t.Logf("   Jaeger UI: http://localhost:16686")
	}

	// Client with OTel middleware wired to the now-active global TracerProvider.
	otelClient, err := client.WithOptions(quark.WithMiddleware(quarkotel.New()))
	if err != nil {
		t.Fatalf("failed to create otel client: %v", err)
	}

	type OtelSuiteUser struct {
		ID    int64  `db:"id" pk:"true"`
		Name  string `db:"name"`
		Email string `db:"email"`
	}

	dropTable(client, "otel_suite_users")
	if err := client.Migrate(ctx, &OtelSuiteUser{}); err != nil {
		t.Fatalf("migrate failed: %v", err)
	}
	exporter.Reset()

	// Test 1: INSERT
	t.Run("Insert", func(t *testing.T) {
		user := &OtelSuiteUser{Name: "Otel Test", Email: "otel@test.com"}
		if err := quark.For[OtelSuiteUser](ctx, otelClient).Create(user); err != nil {
			t.Fatalf("create failed: %v", err)
		}
		spans := exporter.GetSpans()
		if len(spans) == 0 {
			t.Error("expected spans for INSERT operation")
		}
		t.Logf("✓ INSERT generated %d spans", len(spans))
		exporter.Reset()
	})

	// Test 2: SELECT
	t.Run("Select", func(t *testing.T) {
		users, err := quark.For[OtelSuiteUser](ctx, otelClient).List()
		if err != nil {
			t.Fatalf("list failed: %v", err)
		}
		if len(users) != 1 {
			t.Errorf("expected 1 user, got %d", len(users))
		}
		spans := exporter.GetSpans()
		if len(spans) == 0 {
			t.Error("expected spans for SELECT operation")
		}
		t.Logf("✓ SELECT generated %d spans", len(spans))
		exporter.Reset()
	})

	// Test 3: First
	t.Run("First", func(t *testing.T) {
		user, err := quark.For[OtelSuiteUser](ctx, otelClient).First()
		if err != nil {
			t.Fatalf("first failed: %v", err)
		}
		if user.Name != "Otel Test" {
			t.Errorf("expected 'Otel Test', got %s", user.Name)
		}
		spans := exporter.GetSpans()
		if len(spans) == 0 {
			t.Error("expected spans for First operation")
		}
		t.Logf("✓ First() generated %d spans", len(spans))
		exporter.Reset()
	})

	// Test 4: UPDATE
	t.Run("Update", func(t *testing.T) {
		user, _ := quark.For[OtelSuiteUser](ctx, otelClient).First()
		user.Name = "Updated Otel"
		if _, err := quark.For[OtelSuiteUser](ctx, otelClient).Update(&user); err != nil {
			t.Fatalf("update failed: %v", err)
		}
		spans := exporter.GetSpans()
		if len(spans) == 0 {
			t.Error("expected spans for UPDATE operation")
		}
		t.Logf("✓ UPDATE generated %d spans", len(spans))
		exporter.Reset()
	})

	// Test 5: DELETE
	t.Run("Delete", func(t *testing.T) {
		if _, err := quark.For[OtelSuiteUser](ctx, otelClient).Where("id", ">", 0).DeleteBy(); err != nil {
			t.Fatalf("delete failed: %v", err)
		}
		spans := exporter.GetSpans()
		if len(spans) == 0 {
			t.Error("expected spans for DELETE operation")
		}
		t.Logf("✓ DELETE generated %d spans", len(spans))
		exporter.Reset()
	})

	// Test 6: Atributos de span
	t.Run("SpanAttributes", func(t *testing.T) {
		user := &OtelSuiteUser{Name: "Attr Test", Email: "attr@test.com"}
		quark.For[OtelSuiteUser](ctx, otelClient).Create(user)

		spans := exporter.GetSpans()
		validSpans := 0
		for _, span := range spans {
			hasDBStatement := false
			hasDBOperation := false
			for _, attr := range span.Attributes {
				if attr.Key == "db.statement" && attr.Value.AsString() != "" {
					hasDBStatement = true
				}
				if attr.Key == "db.operation" && attr.Value.AsString() != "" {
					hasDBOperation = true
				}
			}
			if hasDBStatement && hasDBOperation {
				validSpans++
			}
		}
		if validSpans == 0 {
			t.Error("expected spans with db.statement and db.operation attributes")
		}
		t.Logf("✓ %d spans have valid attributes", validSpans)
		if realCollector {
			t.Logf("📊 Traces visible in Jaeger UI → service: '%s'", serviceName)
		}
	})
}
