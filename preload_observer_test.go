// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark_test

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/jcsvwinston/quark"
	_ "modernc.org/sqlite"
)

// preloadEventRecorder captures every QueryEvent so the test can assert
// which operations reached the observer pipeline.
type preloadEventRecorder struct {
	mu     sync.Mutex
	events []quark.QueryEvent
}

func (r *preloadEventRecorder) ObserveQuery(e quark.QueryEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *preloadEventRecorder) byOperation(op string) []quark.QueryEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []quark.QueryEvent
	for _, e := range r.events {
		if e.Operation == op {
			out = append(out, e)
		}
	}
	return out
}

type poAuthor struct {
	ID    int64    `db:"id" pk:"true"`
	Name  string   `db:"name"`
	Books []poBook `rel:"has_many" join:"author_id"`
}

type poBook struct {
	ID       int64  `db:"id" pk:"true"`
	AuthorID int64  `db:"author_id"`
	Title    string `db:"title"`
}

// TestPreloadQueriesReachQueryObserver is the AQ-04 regression: the batched
// child SELECTs that Preload issues used to bypass notifyObservers entirely,
// so neither QueryObserver implementations nor the slow-query log (which
// piggybacks on the same pipeline) ever saw them. They must now arrive as
// QueryEvents with Operation "PRELOAD".
func TestPreloadQueriesReachQueryObserver(t *testing.T) {
	ctx := context.Background()
	rec := &preloadEventRecorder{}

	client, err := quark.New("sqlite", "file:preloadobs?mode=memory&cache=shared",
		quark.WithQueryObserver(rec))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	defer client.Close()

	if err := client.Migrate(ctx, &poAuthor{}, &poBook{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	author := poAuthor{Name: "ursula"}
	if err := quark.For[poAuthor](ctx, client).Create(&author); err != nil {
		t.Fatalf("seed author: %v", err)
	}
	for _, title := range []string{"dispossessed", "left hand"} {
		b := poBook{AuthorID: author.ID, Title: title}
		if err := quark.For[poBook](ctx, client).Create(&b); err != nil {
			t.Fatalf("seed book: %v", err)
		}
	}

	got, err := quark.For[poAuthor](ctx, client).
		Where("id", "=", author.ID).
		Preload("Books").
		List()
	if err != nil {
		t.Fatalf("list+preload: %v", err)
	}
	if len(got) != 1 || len(got[0].Books) != 2 {
		t.Fatalf("preload shape wrong: %+v", got)
	}

	events := rec.byOperation("PRELOAD")
	if len(events) == 0 {
		t.Fatalf("no PRELOAD QueryEvent reached the observer; the batched child SELECT is invisible to observers and the slow-query log (AQ-04)")
	}
	found := false
	for _, e := range events {
		if strings.Contains(e.SQL, "po_books") && strings.Contains(e.SQL, "IN (") {
			found = true
			if e.Table != "po_books" {
				t.Errorf("PRELOAD event Table = %q, want po_books", e.Table)
			}
			if e.Duration < 0 {
				t.Errorf("PRELOAD event carries no duration")
			}
		}
	}
	if !found {
		t.Errorf("no PRELOAD event carried the batched IN-select over po_books; events: %+v", events)
	}
}
