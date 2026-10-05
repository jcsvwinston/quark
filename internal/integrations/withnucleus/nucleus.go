// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package withnucleus serves the notes API from a Nucleus module that wraps
// a *quark.Client — the seam `quark init --with nucleus` writes as source.
// The library has no dependency on Nucleus; this module of fixtures, which
// is never published, is where the two meet in code a build checks.
//
// The handlers read the request context from c.Request.Context() and return
// their errors; fail turns a Quark error into the Nucleus error that carries
// its status, and Nucleus's error handler writes the response.
package withnucleus

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	nerrors "github.com/jcsvwinston/nucleus/pkg/errors"
	"github.com/jcsvwinston/nucleus/pkg/nucleus"

	"github.com/jcsvwinston/quark"

	// The driver module: registers the database/sql driver and the
	// classifier behind quark.IsUniqueViolation.
	_ "github.com/jcsvwinston/quark/drivers/sqlite"

	"github.com/jcsvwinston/quark/internal/integrations/notes"
)

// Module returns the notes feature as a Nucleus module wrapping client. The
// application builds the client and mounts the module; the module owns its
// schema (migrated in OnStart), its routes and its access rules.
func Module(client *quark.Client) nucleus.ModuleSpec {
	return nucleus.Module[struct{}]{
		Name: "notes",

		// Nucleus denies by default: a route without a policy row answers
		// 403 even when it is registered. These rows open the routes to
		// anonymous callers; scope them to a role before the service faces
		// a network. A deny row in the host's policy file overrides them.
		Policies: []nucleus.PolicyRule{
			{Subject: "anonymous", Object: "/notes", Action: "read"},
			{Subject: "anonymous", Object: "/notes/*", Action: "read"},
			{Subject: "anonymous", Object: "/notes", Action: "create"},
			{Subject: "anonymous", Object: "/notes/*", Action: "create"},
		},

		// A JSON API carries no browser session, so CSRF does not apply.
		CSRFExempt: []string{"/notes"},

		OnStart: func(ctx context.Context, _ nucleus.Runtime, _ struct{}) error {
			return client.Migrate(ctx, &notes.Note{})
		},

		Routes: func(r nucleus.Router, _ struct{}) {
			r.Get("/notes", func(c *nucleus.Context) error {
				list, err := quark.For[notes.Note](c.Request.Context(), client).OrderBy("id", "DESC").Limit(100).List()
				if err != nil {
					return fail(err)
				}
				return c.JSON(http.StatusOK, list)
			})

			r.Get("/notes/{id}", func(c *nucleus.Context) error {
				id, err := strconv.ParseInt(c.Param("id"), 10, 64)
				if err != nil {
					return nerrors.BadRequest("the id is a number")
				}
				n, err := quark.For[notes.Note](c.Request.Context(), client).Find(id)
				if err != nil {
					return fail(err)
				}
				return c.JSON(http.StatusOK, n)
			})

			r.Post("/notes", func(c *nucleus.Context) error {
				var d notes.Draft
				if err := c.BindJSON(&d); err != nil {
					return err
				}
				n, err := d.Note()
				if err == nil {
					err = quark.For[notes.Note](c.Request.Context(), client).Create(&n)
				}
				if err != nil {
					return fail(err)
				}
				return c.JSON(http.StatusCreated, n)
			})

			r.Post("/notes/import", func(c *nucleus.Context) error {
				// BindJSON validates what it decodes as a struct, and a list
				// is not one: it answers 400 to any JSON array. A list body
				// is decoded with encoding/json.
				var drafts []notes.Draft
				if err := json.NewDecoder(c.Request.Body).Decode(&drafts); err != nil || len(drafts) == 0 {
					return nerrors.BadRequest("the body is a JSON list of notes")
				}
				ctx := c.Request.Context()
				created := make([]notes.Note, 0, len(drafts))
				err := client.Tx(ctx, func(tx *quark.Tx) error {
					for _, d := range drafts {
						n, err := d.Note()
						if err == nil {
							err = quark.ForTx[notes.Note](ctx, tx).Create(&n)
						}
						if err != nil {
							return err // rolls back every note written before it
						}
						created = append(created, n)
					}
					return nil
				})
				if err != nil {
					return fail(err)
				}
				return c.JSON(http.StatusCreated, created)
			})
		},
	}.Build()
}

// fail is the Nucleus error for err, carrying the status notes.Status gives
// it. The message names the status, not the error: a driver's text can name
// tables and columns, and it belongs in a log, not in a response.
func fail(err error) error {
	status := notes.Status(err)
	return &nerrors.DomainError{
		Code:       strings.ToUpper(strings.ReplaceAll(http.StatusText(status), " ", "_")),
		Message:    http.StatusText(status),
		StatusCode: status,
	}
}
