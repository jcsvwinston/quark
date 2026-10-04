// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package notes is what every fixture of this module serves: the model, the
// one validation a request can fail, and the mapping from an error to the
// HTTP status a handler answers with. The frameworks guide shows this file
// before any framework, and each framework section builds on it.
package notes

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jcsvwinston/quark"
)

// Note is the model. Titles are unique on purpose: creating a second note
// with a taken title gives the handlers a duplicate key to classify.
type Note struct {
	ID        int64     `db:"id" pk:"true" json:"id"`
	Title     string    `db:"title" quark:"unique,not_null" json:"title"`
	Body      string    `db:"body" json:"body"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

// Draft is what a client sends. The server owns the id and the timestamp.
type Draft struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

// ErrNoTitle is the one way a draft can be invalid.
var ErrNoTitle = errors.New("a note needs a title")

// Note turns a draft into the row to insert.
func (d Draft) Note() (Note, error) {
	if strings.TrimSpace(d.Title) == "" {
		return Note{}, ErrNoTitle
	}
	return Note{Title: d.Title, Body: d.Body, CreatedAt: time.Now().UTC()}, nil
}

// Status is the HTTP status for an error a handler meets: its own
// validation, or one Quark returned.
func Status(err error) int {
	switch {
	case errors.Is(err, ErrNoTitle):
		return http.StatusBadRequest
	case errors.Is(err, quark.ErrNotFound):
		return http.StatusNotFound
	case quark.IsUniqueViolation(err):
		return http.StatusConflict
	case errors.Is(err, quark.ErrTimeout):
		return http.StatusGatewayTimeout
	default:
		return http.StatusInternalServerError
	}
}
