// Command chi serves a notes API with go-chi/chi on top of a Quark client.
//
// The pattern is the same for every net/http router: build ONE *quark.Client
// at startup, share it between handlers, and open each query with the
// request's context so cancellation and deadlines reach the database:
//
//	quark.For[Note](r.Context(), client)
//
// Run it (no infrastructure needed, it writes notes.db next to the binary):
//
//	go run .
//	curl -s localhost:8080/notes
//	curl -s -X POST localhost:8080/notes -H 'Content-Type: application/json' -d '{"title":"first","body":"hello"}'
package main

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jcsvwinston/quark"

	// The driver module registers the database/sql driver AND the error
	// classifier behind quark.IsUniqueViolation. Importing modernc.org/sqlite
	// directly would boot just the same and silently turn the 409 into a 500.
	_ "github.com/jcsvwinston/quark/drivers/sqlite"
)

// Note is the example model. Titles are unique so the POST handler has a
// duplicate to classify.
type Note struct {
	ID        int64     `db:"id" pk:"true" json:"id"`
	Title     string    `db:"title" quark:"unique,not_null" json:"title"`
	Body      string    `db:"body" json:"body"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

func main() {
	client, err := quark.New("sqlite", "notes.db")
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()
	if err := client.Migrate(context.Background(), &Note{}); err != nil {
		log.Fatal(err)
	}

	addr := ":8080"
	if p := os.Getenv("PORT"); p != "" {
		addr = ":" + p
	}
	log.Printf("listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, newRouter(client)))
}

// newRouter wires the handlers. It takes the client as a parameter so a test
// can hand it a throwaway database (see main_test.go).
func newRouter(client *quark.Client) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)

	r.Get("/notes", func(w http.ResponseWriter, r *http.Request) {
		notes, err := quark.For[Note](r.Context(), client).OrderBy("id", "DESC").Limit(100).List()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"notes": notes, "count": len(notes)})
	})

	r.Post("/notes", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Title string `json:"title"`
			Body  string `json:"body"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Title == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "title is required"})
			return
		}
		n := Note{Title: in.Title, Body: in.Body, CreatedAt: time.Now()}
		if err := quark.For[Note](r.Context(), client).Create(&n); err != nil {
			if quark.IsUniqueViolation(err) {
				writeJSON(w, http.StatusConflict, map[string]string{"error": "a note with that title already exists"})
				return
			}
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusCreated, n)
	})

	return r
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
