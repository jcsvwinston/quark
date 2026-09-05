package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
)

// The handlers behave the same through every router: an empty list, a
// created note, the duplicate title classified as 409 (which needs the driver
// module linked — see the blank import in main.go), and the list again.
func TestNotesAPI(t *testing.T) {
	client, err := quark.New("sqlite", filepath.Join(t.TempDir(), "notes.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.Migrate(context.Background(), &Note{}); err != nil {
		t.Fatal(err)
	}
	handler := newRouter(client)

	for _, tc := range []struct {
		method, body string
		want         int
	}{
		{"GET", "", http.StatusOK},
		{"POST", `{"title":"first","body":"hello"}`, http.StatusCreated},
		{"POST", `{"title":"first"}`, http.StatusConflict},
		{"POST", `{"body":"no title"}`, http.StatusBadRequest},
		{"GET", "", http.StatusOK},
	} {
		req := httptest.NewRequest(tc.method, "/notes", strings.NewReader(tc.body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("%s /notes %s: got %d want %d\nbody: %s", tc.method, tc.body, rec.Code, tc.want, rec.Body.String())
		}
	}
}
