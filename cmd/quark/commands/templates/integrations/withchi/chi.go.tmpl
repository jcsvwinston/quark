// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package withchi serves the notes API with go-chi/chi. chi handlers are
// plain http.HandlerFuncs, so the request context is r.Context() as in the
// standard library; what chi adds is the router, its middleware and
// chi.URLParam.
package withchi

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jcsvwinston/quark"

	// The driver module: registers the database/sql driver and the
	// classifier behind quark.IsUniqueViolation.
	_ "github.com/jcsvwinston/quark/drivers/sqlite"

	"github.com/jcsvwinston/quark/internal/integrations/notes"
)

// NewRouter wires the handlers on the client it is given, so a test can
// hand it a throwaway database.
func NewRouter(client *quark.Client) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)

	r.Get("/notes", func(w http.ResponseWriter, r *http.Request) {
		list, err := quark.For[notes.Note](r.Context(), client).OrderBy("id", "DESC").Limit(100).List()
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	})

	r.Get("/notes/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
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

	r.Post("/notes", func(w http.ResponseWriter, r *http.Request) {
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

	r.Post("/notes/import", func(w http.ResponseWriter, r *http.Request) {
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

	return r
}

// writeError and writeJSON are the standard library's: chi needs nothing
// else to answer.
func writeError(w http.ResponseWriter, err error) {
	status := notes.Status(err)
	writeJSON(w, status, map[string]string{"error": http.StatusText(status)})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
