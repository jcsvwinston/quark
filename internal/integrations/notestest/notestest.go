// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package notestest is the one battery every fixture of this module passes:
// the same requests, over HTTP, answered the same way whichever router
// serves them. A fixture's test builds its router on a fresh SQLite client
// and hands it to Exercise; what differs between fixtures is the framework,
// never the contract.
package notestest

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/quarktest"

	"github.com/jcsvwinston/quark/internal/integrations/notes"
)

// Client returns a client on a fresh SQLite file with the notes table
// migrated — what a fixture's router is built on in a test.
func Client(t *testing.T) *quark.Client {
	t.Helper()
	client := quarktest.SQLite(t)
	quarktest.Migrate(t, client, &notes.Note{})
	return client
}

// Exercise serves h on a loopback HTTP server and runs the battery against
// it, with the deadline request served through Expired(h).
func Exercise(t *testing.T, h http.Handler) {
	t.Helper()
	srv := httptest.NewServer(h)
	defer srv.Close()
	late := httptest.NewServer(Expired(h))
	defer late.Close()
	Run(t, srv.URL, late.URL)
}

// Expired wraps h so that every request reaches it with a deadline that has
// already passed — what a timeout middleware hands a handler whose time is
// up.
func Expired(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithDeadline(r.Context(), time.Now().Add(-time.Second))
		defer cancel()
		h.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Run sends the battery to the server at base, in order: the steps share
// one database and each starts from what the previous left. late serves the
// same handlers through Expired, for the last step.
func Run(t *testing.T, base, late string) {
	t.Helper()
	c := &caller{t: t, base: base}

	c.expect("GET", "/notes", "", http.StatusOK)
	if got := c.list(); len(got) != 0 {
		t.Fatalf("GET /notes on an empty table: %d notes, want none", len(got))
	}

	var first notes.Note
	c.decode(c.expect("POST", "/notes", `{"title":"first","body":"hello"}`, http.StatusCreated), &first)
	if first.ID == 0 || first.Title != "first" || first.Body != "hello" || first.CreatedAt.IsZero() {
		t.Fatalf("POST /notes answered %+v: want the created note with its id and timestamp", first)
	}

	// The duplicate is a 409 only because the driver module is linked: it
	// registers the classifier behind quark.IsUniqueViolation. With the bare
	// database/sql driver the same insert fails as a 500.
	c.expect("POST", "/notes", `{"title":"first"}`, http.StatusConflict)
	c.expect("POST", "/notes", `{"body":"no title"}`, http.StatusBadRequest)
	c.expect("POST", "/notes", `not json`, http.StatusBadRequest)

	var got notes.Note
	c.decode(c.expect("GET", "/notes/"+strconv.FormatInt(first.ID, 10), "", http.StatusOK), &got)
	if got.ID != first.ID || got.Title != "first" {
		t.Fatalf("GET /notes/%d answered %+v: want the note created above", first.ID, got)
	}
	c.expect("GET", "/notes/999999", "", http.StatusNotFound)
	c.expect("GET", "/notes/abc", "", http.StatusBadRequest)

	// One transaction per request: either every note of an import is
	// written, or none is.
	var imported []notes.Note
	c.decode(c.expect("POST", "/notes/import", `[{"title":"second"},{"title":"third","body":"!"}]`, http.StatusCreated), &imported)
	if len(imported) != 2 || imported[0].ID == 0 || imported[1].Title != "third" {
		t.Fatalf("POST /notes/import answered %+v: want the two created notes", imported)
	}
	c.expect("POST", "/notes/import", `[{"title":"fourth"},{"title":"first"}]`, http.StatusConflict)
	c.expect("POST", "/notes/import", `[{"title":"fifth"},{"body":"untitled"}]`, http.StatusBadRequest)
	c.expect("POST", "/notes/import", `[]`, http.StatusBadRequest)

	titles := []string{}
	for _, n := range c.list() {
		titles = append(titles, n.Title)
	}
	if want := []string{"third", "second", "first"}; !slices.Equal(titles, want) {
		t.Fatalf("GET /notes after the imports lists %q, want %q: a failed import left rows behind, or the order is not newest first", titles, want)
	}

	// The handler queries with the request's context: a deadline that has
	// already passed reaches the database and comes back as a 504. A
	// handler that used context.Background() would answer 200 here.
	(&caller{t: t, base: late}).expect("GET", "/notes", "", http.StatusGatewayTimeout)
}

type caller struct {
	t    *testing.T
	base string
}

func (c *caller) expect(method, path, body string, status int) []byte {
	c.t.Helper()
	var rd io.Reader
	if body != "" {
		rd = bytes.NewBufferString(body)
	}
	req, err := http.NewRequest(method, c.base+path, rd)
	if err != nil {
		c.t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	if resp.StatusCode != status {
		c.t.Fatalf("%s %s %s: status %d, want %d\nbody: %s", method, path, body, resp.StatusCode, status, out)
	}
	return out
}

func (c *caller) decode(body []byte, v any) {
	c.t.Helper()
	if err := json.Unmarshal(body, v); err != nil {
		c.t.Fatalf("decode %s: %v", body, err)
	}
}

func (c *caller) list() []notes.Note {
	c.t.Helper()
	var out []notes.Note
	c.decode(c.expect("GET", "/notes", "", http.StatusOK), &out)
	return out
}
