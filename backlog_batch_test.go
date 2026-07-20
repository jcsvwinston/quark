// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Regression tests for the v1.2.1 backlog query-builder items QK-P1-4
// (UpsertBatch chunking) and QK-P2-3 (per-dialect bind-parameter ceilings).
package quark_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"

	_ "modernc.org/sqlite"
)

// chunkTestLimits returns the Limits for the 11000-row chunking tests
// below: DefaultLimits except for QueryTimeout. A single chunked batch
// pass takes 15-35s under -race even on fast hardware, so the default
// 30s per-query budget made the -race gate fail on modest machines
// (QK7-2). What these tests pin is chunking behaviour, not latency —
// the generous budget keeps them deterministic while go test's own
// -timeout still bounds a real hang.
func chunkTestLimits() quark.Limits {
	l := quark.DefaultLimits()
	l.AllowRawQueries = true // the CREATE TABLE in each test goes through Exec
	l.QueryTimeout = 3 * time.Minute
	return l
}

type chunkUser struct {
	ID    int64  `db:"id" pk:"true"`
	Email string `db:"email"`
	Name  string `db:"name"`
	Age   int    `db:"age"`
}

// QK-P1-4: UpsertBatch used to build ONE statement regardless of size. On
// SQLite the bind-parameter ceiling is 32766 (SQLITE_MAX_VARIABLE_NUMBER
// default); 11000 rows × 3 bound columns = 33000 params overruns it, so
// this test fails with "too many SQL variables" without chunking.
func TestUpsertBatchChunksToDialectCeiling(t *testing.T) {
	client, err := quark.New("sqlite", ":memory:", quark.WithLimits(chunkTestLimits()))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx := context.Background()
	if err := client.Exec(ctx, `CREATE TABLE chunk_users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		email TEXT NOT NULL UNIQUE,
		name TEXT, age INTEGER
	)`); err != nil {
		t.Fatal(err)
	}

	const n = 11000
	rows := make([]*chunkUser, n)
	for i := range rows {
		rows[i] = &chunkUser{Email: fmt.Sprintf("u%05d@test.com", i), Name: "v1", Age: i}
	}
	if err := quark.For[chunkUser](ctx, client).UpsertBatch(rows, []string{"email"}, []string{"name", "age"}); err != nil {
		t.Fatalf("UpsertBatch insert pass: %v", err)
	}
	count, err := quark.For[chunkUser](ctx, client).Count()
	if err != nil {
		t.Fatal(err)
	}
	if count != n {
		t.Fatalf("expected %d rows after insert pass, got %d", n, count)
	}

	// Second pass with the same emails: every row must UPDATE, not duplicate.
	for i := range rows {
		rows[i] = &chunkUser{Email: fmt.Sprintf("u%05d@test.com", i), Name: "v2", Age: i}
	}
	if err := quark.For[chunkUser](ctx, client).UpsertBatch(rows, []string{"email"}, []string{"name", "age"}); err != nil {
		t.Fatalf("UpsertBatch update pass: %v", err)
	}
	count, err = quark.For[chunkUser](ctx, client).Count()
	if err != nil {
		t.Fatal(err)
	}
	if count != n {
		t.Fatalf("expected %d rows after update pass (no duplicates), got %d", n, count)
	}
	updated, err := quark.For[chunkUser](ctx, client).Where("name", "=", "v2").Count()
	if err != nil {
		t.Fatal(err)
	}
	if updated != n {
		t.Fatalf("expected all %d rows updated to v2, got %d", n, updated)
	}
}

// QK-P2-3: CreateBatch also chunks to the dialect ceiling — the same 11000
// rows used to fit under the universal 2000-param budget only because the
// chunks were tiny; with per-dialect ceilings the statement count drops but
// the result must be identical.
func TestCreateBatchLargeSQLite(t *testing.T) {
	client, err := quark.New("sqlite", ":memory:", quark.WithLimits(chunkTestLimits()))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx := context.Background()
	if err := client.Exec(ctx, `CREATE TABLE chunk_users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		email TEXT NOT NULL, name TEXT, age INTEGER
	)`); err != nil {
		t.Fatal(err)
	}

	const n = 11000
	rows := make([]*chunkUser, n)
	for i := range rows {
		rows[i] = &chunkUser{Email: fmt.Sprintf("c%05d@test.com", i), Name: "n", Age: i}
	}
	if err := quark.For[chunkUser](ctx, client).CreateBatch(rows); err != nil {
		t.Fatalf("CreateBatch: %v", err)
	}
	count, err := quark.For[chunkUser](ctx, client).Count()
	if err != nil {
		t.Fatal(err)
	}
	if count != n {
		t.Fatalf("expected %d rows, got %d", n, count)
	}
}

// QK-P2-2: with the MariaDB dialect the INTERSECT/EXCEPT keywords render
// (MariaDB 10.3+ supports them); with the MySQL dialect they keep returning
// ErrUnsupportedFeature (8.0.31+ cannot be assumed). SQLite executes the
// rendered SQL, so pinning the MariaDB dialect onto a SQLite connection
// exercises the full render+exec path.
func TestSetOpsMariaDBEnabledMySQLBlocked(t *testing.T) {
	limits := quark.DefaultLimits()
	limits.AllowRawQueries = true
	client, err := quark.New("sqlite", ":memory:", quark.WithDialect(quark.MariaDB()), quark.WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	ctx := context.Background()
	if err := client.Exec(ctx, `CREATE TABLE chunk_users (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		email TEXT NOT NULL, name TEXT, age INTEGER
	)`); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		u := chunkUser{Email: fmt.Sprintf("s%d@test.com", i), Name: "s", Age: i % 2}
		if err := quark.For[chunkUser](ctx, client).Create(&u); err != nil {
			t.Fatal(err)
		}
	}

	lhs := quark.For[chunkUser](ctx, client).Select("email").Where("age", "=", 0)
	rhs := quark.For[chunkUser](ctx, client).Select("email").Where("age", "=", 0)
	if _, err := lhs.Intersect(rhs).List(); err != nil {
		t.Errorf("Intersect on mariadb dialect should render and execute, got: %v", err)
	}
	if _, err := lhs.Except(rhs).List(); err != nil {
		t.Errorf("Except on mariadb dialect should render and execute, got: %v", err)
	}

	// MySQL dialect: still rejected with the version rationale.
	myClient, err := quark.New("sqlite", ":memory:", quark.WithDialect(quark.MySQL()), quark.WithLimits(limits))
	if err != nil {
		t.Fatal(err)
	}
	defer myClient.Close()
	if err := myClient.Exec(ctx, `CREATE TABLE chunk_users (id INTEGER PRIMARY KEY, email TEXT, name TEXT, age INTEGER)`); err != nil {
		t.Fatal(err)
	}
	l2 := quark.For[chunkUser](ctx, myClient).Select("email")
	if _, err := l2.Intersect(l2).List(); err == nil {
		t.Error("Intersect on mysql dialect should return ErrUnsupportedFeature")
	}
}
