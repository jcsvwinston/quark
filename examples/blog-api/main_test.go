package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/migrate"
	_ "modernc.org/sqlite"
)

func testServer(t *testing.T) *server {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	lims := quark.DefaultLimits()
	lims.AllowRawQueries = true
	client, err := quark.New(db, quark.WithDialect(quark.SQLite()), quark.WithLimits(lims))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	migrate.Reset()
	registerMigrations()
	m := migrate.NewMigrator(client)
	if err := m.Up(context.Background(), 0); err != nil {
		t.Fatal("migrations:", err)
	}
	return &server{client: client}
}

func TestCreateAndListAuthors(t *testing.T) {
	srv := testServer(t)
	ctx := context.Background()

	a := &Author{Name: "Jane Doe", Email: "jane@example.com", Bio: "Go developer.", CreatedAt: time.Now()}
	if err := quark.For[Author](ctx, srv.client).Create(a); err != nil {
		t.Fatal(err)
	}
	if a.ID == 0 {
		t.Fatal("expected non-zero ID after create")
	}

	authors, err := quark.For[Author](ctx, srv.client).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(authors) != 1 {
		t.Fatalf("expected 1 author, got %d", len(authors))
	}
	if authors[0].Name != "Jane Doe" {
		t.Fatalf("unexpected name: %s", authors[0].Name)
	}
}

func TestCreateAndQueryPosts(t *testing.T) {
	srv := testServer(t)
	ctx := context.Background()

	a := &Author{Name: "Bob", Email: "bob@example.com", CreatedAt: time.Now()}
	if err := quark.For[Author](ctx, srv.client).Create(a); err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	p := &Post{
		Title:       "Hello Quark",
		Body:        "Type-safe ORM for Go.",
		AuthorID:    a.ID,
		Published:   true,
		PublishedAt: &now,
		CreatedAt:   time.Now(),
	}
	if err := quark.For[Post](ctx, srv.client).Create(p); err != nil {
		t.Fatal(err)
	}
	if p.ID == 0 {
		t.Fatal("expected non-zero ID after create")
	}

	posts, err := quark.For[Post](ctx, srv.client).Where("published", "=", true).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 1 {
		t.Fatalf("expected 1 published post, got %d", len(posts))
	}
}

func TestHTTPCreateAndGetPost(t *testing.T) {
	srv := testServer(t)

	body, _ := json.Marshal(map[string]any{
		"title":     "HTTP Test Post",
		"body":      "Created via HTTP handler.",
		"author_id": 1,
		"published": false,
	})

	req := httptest.NewRequest(http.MethodPost, "/posts", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("create: expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	var created Post
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}
	if created.ID == 0 {
		t.Fatal("expected non-zero ID in response")
	}

	req2 := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/posts/%d", created.ID), nil)
	rec2 := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("get: expected 200, got %d", rec2.Code)
	}
}

func TestHTTPListPosts(t *testing.T) {
	srv := testServer(t)
	ctx := context.Background()

	p := &Post{Title: "Listed", Body: ".", Published: true, CreatedAt: time.Now()}
	if err := quark.For[Post](ctx, srv.client).Create(p); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodGet, "/posts", nil)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	var posts []Post
	if err := json.NewDecoder(rec.Body).Decode(&posts); err != nil {
		t.Fatal(err)
	}
	if len(posts) < 1 {
		t.Fatal("expected at least one post")
	}
}

func TestHTTPDeletePost(t *testing.T) {
	srv := testServer(t)
	ctx := context.Background()

	p := &Post{Title: "To Delete", Body: ".", Published: false, CreatedAt: time.Now()}
	if err := quark.For[Post](ctx, srv.client).Create(p); err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodDelete, fmt.Sprintf("/posts/%d", p.ID), nil)
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}

	req2 := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/posts/%d", p.ID), nil)
	rec2 := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d", rec2.Code)
	}
}
