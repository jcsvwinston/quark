package quark_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/jcsvwinston/quark"

	_ "modernc.org/sqlite"
)

// setupTestDBWithOrders creates users + orders tables for JOIN tests.
func setupTestDBWithOrders(t *testing.T) (*quark.Client, func()) {
	limits := quark.DefaultLimits()
	limits.AllowRawQueries = true
	client, err := quark.New("sqlite", ":memory:", quark.WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	err = client.Exec(context.Background(), `
		CREATE TABLE users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			email TEXT NOT NULL, name TEXT, active BOOLEAN DEFAULT 1,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE orders (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id INTEGER, amount REAL, status TEXT,
			FOREIGN KEY(user_id) REFERENCES users(id)
		);
	`)
	if err != nil {
		client.Close()
		t.Fatal(err)
	}
	return client, func() { client.Close() }
}

// --- Fase 1: Transaction Tests ---

func TestTxCommit(t *testing.T) {
	client, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	err := client.Tx(ctx, func(tx *quark.Tx) error {
		user := User{Email: "tx@test.com", Name: "quark.TxUser", Active: true}
		return quark.ForTx[User](ctx, tx).Create(&user)
	})
	if err != nil {
		t.Fatalf("tx commit: %v", err)
	}

	// Verify user exists after commit
	found, err := quark.For[User](ctx, client).Where("email", "=", "tx@test.com").First()
	if err != nil {
		t.Fatalf("find after commit: %v", err)
	}
	if found.Name != "quark.TxUser" {
		t.Errorf("expected quark.TxUser, got %s", found.Name)
	}
	fmt.Println("✓ quark.Tx commit works")
}

func TestTxRollback(t *testing.T) {
	client, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	errIntentional := errors.New("intentional rollback")
	err := client.Tx(ctx, func(tx *quark.Tx) error {
		user := User{Email: "rollback@test.com", Name: "RollbackUser"}
		if err := quark.ForTx[User](ctx, tx).Create(&user); err != nil {
			return err
		}
		return errIntentional
	})
	if !errors.Is(err, errIntentional) {
		t.Fatalf("expected intentional error, got %v", err)
	}

	// Verify user does NOT exist after rollback
	count, err := quark.For[User](ctx, client).Where("email", "=", "rollback@test.com").Count()
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("expected 0 users after rollback, got %d", count)
	}
	fmt.Println("✓ quark.Tx rollback works")
}

func TestTxManual(t *testing.T) {
	client, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	tx, err := client.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	user := User{Email: "manual@test.com", Name: "ManualTx"}
	if err := quark.ForTx[User](ctx, tx).Create(&user); err != nil {
		tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	found, err := quark.For[User](ctx, client).Where("email", "=", "manual@test.com").First()
	if err != nil {
		t.Fatal(err)
	}
	if found.Name != "ManualTx" {
		t.Errorf("expected ManualTx, got %s", found.Name)
	}
	fmt.Println("✓ Manual quark.Tx (BeginTx/Commit) works")
}

func TestTxSavepoint(t *testing.T) {
	client, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	err := client.Tx(ctx, func(tx *quark.Tx) error {
		u1 := User{Email: "sp1@test.com", Name: "SP1"}
		if err := quark.ForTx[User](ctx, tx).Create(&u1); err != nil {
			return err
		}

		if err := tx.Savepoint("before_u2"); err != nil {
			return err
		}

		u2 := User{Email: "sp2@test.com", Name: "SP2"}
		if err := quark.ForTx[User](ctx, tx).Create(&u2); err != nil {
			return err
		}

		// Rollback to savepoint — u2 should be undone, u1 kept
		if err := tx.RollbackTo("before_u2"); err != nil {
			return err
		}

		return nil // commit
	})
	if err != nil {
		t.Fatalf("tx savepoint: %v", err)
	}

	all, err := quark.For[User](ctx, client).Limit(100).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Errorf("expected 1 user after savepoint rollback, got %d", len(all))
	}
	if len(all) > 0 && all[0].Email != "sp1@test.com" {
		t.Errorf("expected sp1@test.com, got %s", all[0].Email)
	}
	fmt.Println("✓ quark.Tx savepoints work")
}

// --- Fase 3: Immutable Query Tests ---

func TestQueryClone(t *testing.T) {
	client, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		u := User{Email: fmt.Sprintf("clone%d@test.com", i), Name: fmt.Sprintf("Clone%d", i), Active: i < 3}
		quark.For[User](ctx, client).Create(&u)
	}

	base := quark.For[User](ctx, client).Where("active", "=", true)
	q1 := base.Limit(1)
	q2 := base.Limit(10)

	r1, err := q1.List()
	if err != nil {
		t.Fatal(err)
	}
	r2, err := q2.List()
	if err != nil {
		t.Fatal(err)
	}

	if len(r1) != 1 {
		t.Errorf("q1 expected 1, got %d", len(r1))
	}
	if len(r2) != 3 {
		t.Errorf("q2 expected 3, got %d", len(r2))
	}
	fmt.Println("✓ Query clone/immutability works")
}

func TestQueryConcurrentSafe(t *testing.T) {
	client, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	for i := 0; i < 50; i++ {
		u := User{Email: fmt.Sprintf("conc%d@test.com", i), Name: fmt.Sprintf("Conc%d", i)}
		quark.For[User](ctx, client).Create(&u)
	}

	base := quark.For[User](ctx, client)
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			_, _ = base.Where("name", "=", fmt.Sprintf("Conc%d", n)).Limit(1).List()
		}(i)
	}
	wg.Wait()
	fmt.Println("✓ Concurrent query usage is race-free")
}

// --- Fase 4: JOIN Tests ---

type Order struct {
	ID     int64   `db:"id"`
	UserID int64   `db:"user_id"`
	Amount float64 `db:"amount"`
	Status string  `db:"status"`
}

func TestInnerJoin(t *testing.T) {
	client, cleanup := setupTestDBWithOrders(t)
	defer cleanup()
	ctx := context.Background()

	u := User{Email: "join@test.com", Name: "JoinUser", Active: true}
	quark.For[User](ctx, client).Create(&u)

	o := Order{UserID: u.ID, Amount: 99.99, Status: "paid"}
	quark.For[Order](ctx, client).Create(&o)

	// Use Join — just verify it doesn't error and generates valid SQL
	results, err := quark.For[Order](ctx, client).
		Join("users", "users.id = orders.user_id").
		Where("status", "=", "paid").
		Limit(10).List()
	if err != nil {
		t.Fatalf("join query: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result, got %d", len(results))
	}
	fmt.Println("✓ INNER JOIN works")
}

func TestLeftJoin(t *testing.T) {
	client, cleanup := setupTestDBWithOrders(t)
	defer cleanup()
	ctx := context.Background()

	u1 := User{Email: "lj1@test.com", Name: "WithOrder"}
	quark.For[User](ctx, client).Create(&u1)
	u2 := User{Email: "lj2@test.com", Name: "NoOrder"}
	quark.For[User](ctx, client).Create(&u2)

	o := Order{UserID: u1.ID, Amount: 50.0, Status: "pending"}
	quark.For[Order](ctx, client).Create(&o)

	// LeftJoin — count users, including those without orders
	count, err := quark.For[User](ctx, client).
		LeftJoin("orders", "orders.user_id = users.id").
		Count()
	if err != nil {
		t.Fatalf("left join: %v", err)
	}
	if count < 2 {
		t.Errorf("expected >= 2 with left join, got %d", count)
	}
	fmt.Println("✓ LEFT JOIN works")
}

// --- Fase 5: OR Conditions Tests ---

func TestOrCondition(t *testing.T) {
	client, cleanup := setupTestDB(t)
	defer cleanup()
	ctx := context.Background()

	users := []User{
		{Email: "admin@test.com", Name: "Admin", Active: true},
		{Email: "user@test.com", Name: "Regular", Active: false},
		{Email: "mod@test.com", Name: "Mod", Active: true},
	}
	for i := range users {
		quark.For[User](ctx, client).Create(&users[i])
	}

	// WHERE active = true OR name = 'Regular'
	results, err := quark.For[User](ctx, client).
		Where("active", "=", true).
		Or(func(q *quark.Query[User]) *quark.Query[User] {
			return q.Where("name", "=", "Regular")
		}).
		Limit(10).List()
	if err != nil {
		t.Fatalf("or query: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("expected 3 results (2 active + 1 Regular), got %d", len(results))
	}
	fmt.Println("✓ OR conditions work")
}

// --- Fase 6: Middleware Tests ---

type mockMiddleware struct {
	quark.BaseMiddleware
	queries   int
	execs     int
	queryRows int
}

func (m *mockMiddleware) WrapQuery(next quark.QueryFunc) quark.QueryFunc {
	return func(ctx context.Context, exec quark.Executor, sqlStr string, args []any) (*sql.Rows, error) {
		m.queries++
		return next(ctx, exec, sqlStr, args)
	}
}

func (m *mockMiddleware) WrapExec(next quark.ExecFunc) quark.ExecFunc {
	return func(ctx context.Context, exec quark.Executor, sqlStr string, args []any) (sql.Result, error) {
		m.execs++
		return next(ctx, exec, sqlStr, args)
	}
}

func (m *mockMiddleware) WrapQueryRow(next quark.QueryRowFunc) quark.QueryRowFunc {
	return func(ctx context.Context, exec quark.Executor, sqlStr string, args []any) *sql.Row {
		m.queryRows++
		return next(ctx, exec, sqlStr, args)
	}
}

func TestMiddlewareChain(t *testing.T) {
	limits := quark.DefaultLimits()
	limits.AllowRawQueries = true
	client, err := quark.New("sqlite", ":memory:", quark.WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx := context.Background()

	err = client.Exec(ctx, `CREATE TABLE users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		email TEXT NOT NULL, name TEXT, active BOOLEAN DEFAULT 1,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		t.Fatal(err)
	}

	m1 := &mockMiddleware{}
	m2 := &mockMiddleware{}

	// Wire both middlewares into the client via WithOptions
	client, err = client.WithOptions(
		quark.WithLimits(limits),
		quark.WithMiddleware(m1),
		quark.WithMiddleware(m2),
	)
	if err != nil {
		t.Fatal(err)
	}

	// Recreate table on the new client
	err = client.Exec(ctx, `CREATE TABLE users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		email TEXT NOT NULL, name TEXT, active BOOLEAN DEFAULT 1,
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		t.Fatal(err)
	}

	// Trigger an Exec (INSERT)
	user := User{Email: "mid@test.com", Name: "Middleware"}
	if err := quark.For[User](ctx, client).Create(&user); err != nil {
		t.Fatal(err)
	}

	// Trigger a Query (SELECT)
	_, err = quark.For[User](ctx, client).Where("email", "=", "mid@test.com").Limit(1).List()
	if err != nil {
		t.Fatal(err)
	}

	// SQLite uses RETURNING clause for INSERT, so it goes through WrapQueryRow
	// rather than WrapExec. Either path is correct depending on the dialect.
	m1Writes := m1.execs + m1.queryRows
	m2Writes := m2.execs + m2.queryRows
	if m1Writes == 0 {
		t.Errorf("m1: expected at least 1 write operation (exec or queryRow), got 0")
	}
	if m2Writes == 0 {
		t.Errorf("m2: expected at least 1 write operation (exec or queryRow), got 0")
	}
	if m1.queries != 1 {
		t.Errorf("m1: expected 1 query (SELECT), got %d", m1.queries)
	}
	if m2.queries != 1 {
		t.Errorf("m2: expected 1 query (SELECT), got %d", m2.queries)
	}
	fmt.Println("✓ Middleware chain works")
}
