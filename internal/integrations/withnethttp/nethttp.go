// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package withnethttp serves the notes API with the standard library alone:
// http.ServeMux and its method-and-path patterns. It is the baseline the
// router fixtures are read against, and the section of the frameworks guide
// that shows how a process opens its one client.
package withnethttp

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strconv"

	"github.com/jcsvwinston/quark"

	// The driver module registers the database/sql driver AND the error
	// classifier behind quark.IsUniqueViolation. Importing the bare driver
	// (modernc.org/sqlite) boots just the same and turns every 409 into a 500.
	_ "github.com/jcsvwinston/quark/drivers/sqlite"

	"github.com/jcsvwinston/quark/internal/integrations/notes"
)

// Serve opens the process's one client, migrates the model and serves the
// notes API on ln until ctx is done.
func Serve(ctx context.Context, ln net.Listener, dsn string) error {
	client, err := quark.New("sqlite", dsn)
	if err != nil {
		return err
	}
	defer client.Close()
	if err := client.Migrate(ctx, &notes.Note{}); err != nil {
		return err
	}

	srv := &http.Server{Handler: NewMux(client)}
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.WithoutCancel(ctx))
	}()
	if err := srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// NewMux wires the handlers on the client it is given, so a test can hand
// it a throwaway database.
func NewMux(client *quark.Client) *http.ServeMux {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /notes", func(w http.ResponseWriter, r *http.Request) {
		list, err := quark.For[notes.Note](r.Context(), client).OrderBy("id", "DESC").Limit(100).List()
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	})

	mux.HandleFunc("GET /notes/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the id is a number"})
			return
		}
		n, err := quark.For[notes.Note](r.Context(), client).Find(id)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, n)
	})

	mux.HandleFunc("POST /notes", func(w http.ResponseWriter, r *http.Request) {
		var d notes.Draft
		if err := json.NewDecoder(r.Body).Decode(&d); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the body is a JSON note"})
			return
		}
		n, err := d.Note()
		if err == nil {
			err = quark.For[notes.Note](r.Context(), client).Create(&n)
		}
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, n)
	})

	mux.HandleFunc("POST /notes/import", func(w http.ResponseWriter, r *http.Request) {
		var drafts []notes.Draft
		if err := json.NewDecoder(r.Body).Decode(&drafts); err != nil || len(drafts) == 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "the body is a JSON list of notes"})
			return
		}
		created := make([]notes.Note, 0, len(drafts))
		err := client.Tx(r.Context(), func(tx *quark.Tx) error {
			for _, d := range drafts {
				n, err := d.Note()
				if err == nil {
					err = quark.ForTx[notes.Note](r.Context(), tx).Create(&n)
				}
				if err != nil {
					return err // rolls back every note written before it
				}
				created = append(created, n)
			}
			return nil
		})
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, created)
	})

	return mux
}

// writeError answers with the status notes.Status gives err. The body names
// the status, not the error: a driver's message can name tables and
// columns, and it belongs in a log, not in a response.
func writeError(w http.ResponseWriter, err error) {
	status := notes.Status(err)
	writeJSON(w, status, map[string]string{"error": http.StatusText(status)})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
