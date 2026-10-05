// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package withecho serves the notes API with labstack/echo v5. Echo wraps
// the request — the context is c.Request().Context() — and handlers return
// their errors, so the mapping from a Quark error to a status lives in one
// place: the server's HTTPErrorHandler.
package withecho

import (
	"net/http"

	"github.com/jcsvwinston/quark"
	"github.com/labstack/echo/v5"
	"github.com/labstack/echo/v5/middleware"

	// The driver module: registers the database/sql driver and the
	// classifier behind quark.IsUniqueViolation.
	_ "github.com/jcsvwinston/quark/drivers/sqlite"

	"github.com/jcsvwinston/quark/internal/integrations/notes"
)

// NewServer wires the handlers on the client it is given, so a test can
// hand it a throwaway database.
func NewServer(client *quark.Client) *echo.Echo {
	e := echo.New()
	e.Use(middleware.Recover())

	// Handlers return Quark's errors as they come; this decides their
	// status. An error that already carries one — Echo's own, from a failed
	// bind or path parameter — keeps it.
	e.HTTPErrorHandler = func(c *echo.Context, err error) {
		if echo.StatusCode(err) == 0 {
			err = echo.NewHTTPError(notes.Status(err), "").Wrap(err)
		}
		echo.DefaultHTTPErrorHandler(false)(c, err)
	}

	e.GET("/notes", func(c *echo.Context) error {
		list, err := quark.For[notes.Note](c.Request().Context(), client).OrderBy("id", "DESC").Limit(100).List()
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, list)
	})

	e.GET("/notes/:id", func(c *echo.Context) error {
		id, err := echo.PathParam[int64](c, "id")
		if err != nil {
			return err
		}
		n, err := quark.For[notes.Note](c.Request().Context(), client).Find(id)
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, n)
	})

	e.POST("/notes", func(c *echo.Context) error {
		var d notes.Draft
		if err := c.Bind(&d); err != nil {
			return err
		}
		n, err := d.Note()
		if err != nil {
			return err
		}
		if err := quark.For[notes.Note](c.Request().Context(), client).Create(&n); err != nil {
			return err
		}
		return c.JSON(http.StatusCreated, n)
	})

	e.POST("/notes/import", func(c *echo.Context) error {
		var drafts []notes.Draft
		if err := c.Bind(&drafts); err != nil {
			return err
		}
		if len(drafts) == 0 {
			return echo.NewHTTPError(http.StatusBadRequest, "the body is a JSON list of notes")
		}
		ctx := c.Request().Context()
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
			return err
		}
		return c.JSON(http.StatusCreated, created)
	})

	return e
}
