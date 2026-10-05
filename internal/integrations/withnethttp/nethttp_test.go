// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package withnethttp

import (
	"context"
	"net"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/jcsvwinston/quark/internal/integrations/guide"
	"github.com/jcsvwinston/quark/internal/integrations/notestest"
)

func TestNotesAPI(t *testing.T) {
	notestest.Exercise(t, NewMux(notestest.Client(t)))
}

// Serve is the startup the guide shows: it has to open, migrate, answer and
// stop when its context ends.
func TestServe(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, ln, "file:"+filepath.Join(t.TempDir(), "notes.db")) }()

	resp, err := http.Get("http://" + ln.Addr().String() + "/notes")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /notes on a served, migrated database: status %d", resp.StatusCode)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Serve returned %v after its context ended, want nil", err)
	}
}

func TestGuideMatchesFixture(t *testing.T) {
	guide.Check(t, "net/http", "nethttp.go")
}
