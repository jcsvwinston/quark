// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package withgin serves the notes API with gin-gonic/gin. In Gin the
// request is the c.Request field, so the context is c.Request.Context();
// handlers record a failed Quark call with c.Error and stop, and one
// middleware turns the recorded error into the response.
package withgin

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"github.com/jcsvwinston/quark"

	// The driver module: registers the database/sql driver and the
	// classifier behind quark.IsUniqueViolation.
	_ "github.com/jcsvwinston/quark/drivers/sqlite"

	"github.com/jcsvwinston/quark/internal/integrations/notes"
)

// NewEngine wires the handlers on the client it is given, so a test can
// hand it a throwaway database.
func NewEngine(client *quark.Client) *gin.Engine {
	r := gin.New()
	r.Use(gin.Recovery(), answerErrors)

	r.GET("/notes", func(c *gin.Context) {
		list, err := quark.For[notes.Note](c.Request.Context(), client).OrderBy("id", "DESC").Limit(100).List()
		if err != nil {
			_ = c.Error(err)
			return
		}
		c.JSON(http.StatusOK, list)
	})

	r.GET("/notes/:id", func(c *gin.Context) {
		id, err := strconv.ParseInt(c.Param("id"), 10, 64)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "the id is a number"})
			return
		}
		n, err := quark.For[notes.Note](c.Request.Context(), client).Find(id)
		if err != nil {
			_ = c.Error(err)
			return
		}
		c.JSON(http.StatusOK, n)
	})

	r.POST("/notes", func(c *gin.Context) {
		var d notes.Draft
		if err := c.ShouldBindJSON(&d); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "the body is a JSON note"})
			return
		}
		n, err := d.Note()
		if err == nil {
			err = quark.For[notes.Note](c.Request.Context(), client).Create(&n)
		}
		if err != nil {
			_ = c.Error(err)
			return
		}
		c.JSON(http.StatusCreated, n)
	})

	r.POST("/notes/import", func(c *gin.Context) {
		var drafts []notes.Draft
		if err := c.ShouldBindJSON(&drafts); err != nil || len(drafts) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "the body is a JSON list of notes"})
			return
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
			_ = c.Error(err)
			return
		}
		c.JSON(http.StatusCreated, created)
	})

	return r
}

// answerErrors runs the handler, then answers for the last error it
// recorded with the status notes.Status gives it — unless the handler
// already wrote a response.
func answerErrors(c *gin.Context) {
	c.Next()
	if last := c.Errors.Last(); last != nil && !c.Writer.Written() {
		status := notes.Status(last.Err)
		c.JSON(status, gin.H{"error": http.StatusText(status)})
	}
}
