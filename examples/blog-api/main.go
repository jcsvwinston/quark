package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/migrate"
	_ "modernc.org/sqlite"
)

// server holds the shared Quark client.
type server struct {
	client *quark.Client
}

func newServer(dsn string) (*server, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	client, err := quark.New(db, quark.WithDialect(quark.SQLite()))
	if err != nil {
		return nil, fmt.Errorf("quark client: %w", err)
	}
	return &server{client: client}, nil
}

func (s *server) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /authors", s.listAuthors)
	mux.HandleFunc("POST /authors", s.createAuthor)
	mux.HandleFunc("GET /posts", s.listPosts)
	mux.HandleFunc("POST /posts", s.createPost)
	mux.HandleFunc("GET /posts/{id}", s.getPost)
	mux.HandleFunc("PUT /posts/{id}", s.updatePost)
	mux.HandleFunc("DELETE /posts/{id}", s.deletePost)
	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func errJSON(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// --- Author handlers ---

func (s *server) listAuthors(w http.ResponseWriter, r *http.Request) {
	authors, err := quark.For[Author](r.Context(), s.client).List()
	if err != nil {
		errJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, authors)
}

func (s *server) createAuthor(w http.ResponseWriter, r *http.Request) {
	var a Author
	if err := json.NewDecoder(r.Body).Decode(&a); err != nil {
		errJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	a.CreatedAt = time.Now()
	if err := quark.For[Author](r.Context(), s.client).Create(&a); err != nil {
		errJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

// --- Post handlers ---

func (s *server) listPosts(w http.ResponseWriter, r *http.Request) {
	q := quark.For[Post](r.Context(), s.client)
	if pub := r.URL.Query().Get("published"); pub != "" {
		q = q.Where("published", "=", pub == "true")
	}
	posts, err := q.OrderBy("created_at", "DESC").List()
	if err != nil {
		errJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, posts)
}

func (s *server) createPost(w http.ResponseWriter, r *http.Request) {
	var p Post
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		errJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	p.CreatedAt = time.Now()
	if p.Published {
		now := time.Now()
		p.PublishedAt = &now
	}
	if err := quark.For[Post](r.Context(), s.client).Create(&p); err != nil {
		errJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, p)
}

func (s *server) getPost(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		errJSON(w, http.StatusBadRequest, "invalid id")
		return
	}
	post, err := quark.For[Post](r.Context(), s.client).Find(id)
	if err != nil {
		errJSON(w, http.StatusNotFound, "post not found")
		return
	}
	writeJSON(w, http.StatusOK, post)
}

func (s *server) updatePost(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		errJSON(w, http.StatusBadRequest, "invalid id")
		return
	}
	var p Post
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		errJSON(w, http.StatusBadRequest, err.Error())
		return
	}
	p.ID = id
	rows, err := quark.For[Post](r.Context(), s.client).Update(&p)
	if err != nil || rows == 0 {
		errJSON(w, http.StatusNotFound, "post not found")
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (s *server) deletePost(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		errJSON(w, http.StatusBadRequest, "invalid id")
		return
	}
	post, err := quark.For[Post](r.Context(), s.client).Find(id)
	if err != nil {
		errJSON(w, http.StatusNotFound, "post not found")
		return
	}
	if _, err := quark.For[Post](r.Context(), s.client).HardDelete(&post); err != nil {
		errJSON(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func main() {
	ctx := context.Background()

	srv, err := newServer("file:blog.db?cache=shared")
	if err != nil {
		log.Fatal(err)
	}

	registerMigrations()
	migrator := migrate.NewMigrator(srv.client)
	if err := migrator.Up(ctx, 0); err != nil {
		log.Fatal("migrations:", err)
	}

	addr := ":8080"
	fmt.Printf("Blog API listening on http://localhost%s\n", addr)
	fmt.Println("Endpoints:")
	fmt.Println("  GET    /authors")
	fmt.Println("  POST   /authors")
	fmt.Println("  GET    /posts[?published=true]")
	fmt.Println("  POST   /posts")
	fmt.Println("  GET    /posts/{id}")
	fmt.Println("  PUT    /posts/{id}")
	fmt.Println("  DELETE /posts/{id}")
	log.Fatal(http.ListenAndServe(addr, srv.routes()))
}
