// Command echo serves a notes API with labstack/echo on top of a Quark client.
//
// The pattern is the same for every router: build ONE *quark.Client at
// startup, share it between handlers, and open each query with the request's
// context so cancellation and deadlines reach the database. In Echo the
// request lives behind c.Request():
//
//	quark.For[Note](c.Request().Context(), client)
//
// Run it (no infrastructure needed, it writes notes.db next to the binary):
//
//	go run .
//	curl -s localhost:8080/notes
//	curl -s -X POST localhost:8080/notes -H 'Content-Type: application/json' -d '{"title":"first","body":"hello"}'
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/labstack/echo/v4"
	"github.com/labstack/echo/v4/middleware"

	// The driver module registers the database/sql driver AND the error
	// classifier behind quark.IsUniqueViolation. Importing modernc.org/sqlite
	// directly would boot just the same and silently turn the 409 into a 500.
	_ "github.com/jcsvwinston/quark/drivers/sqlite"
)

// Note is the example model. Titles are unique so the POST handler has a
// duplicate to classify.
type Note struct {
	ID        int64     `db:"id" pk:"true" json:"id"`
	Title     string    `db:"title" quark:"unique,not_null" json:"title"`
	Body      string    `db:"body" json:"body"`
	CreatedAt time.Time `db:"created_at" json:"created_at"`
}

func main() {
	client, err := quark.New("sqlite", "notes.db")
	if err != nil {
		log.Fatal(err)
	}
	defer client.Close()
	if err := client.Migrate(context.Background(), &Note{}); err != nil {
		log.Fatal(err)
	}

	addr := ":8080"
	if p := os.Getenv("PORT"); p != "" {
		addr = ":" + p
	}
	log.Fatal(newServer(client).Start(addr))
}

// newServer wires the handlers. It takes the client as a parameter so a test
// can hand it a throwaway database (see main_test.go).
func newServer(client *quark.Client) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.Use(middleware.Recover())

	e.GET("/notes", func(c echo.Context) error {
		notes, err := quark.For[Note](c.Request().Context(), client).OrderBy("id", "DESC").Limit(100).List()
		if err != nil {
			return err
		}
		return c.JSON(http.StatusOK, map[string]any{"notes": notes, "count": len(notes)})
	})

	e.POST("/notes", func(c echo.Context) error {
		var in struct {
			Title string `json:"title"`
			Body  string `json:"body"`
		}
		if err := c.Bind(&in); err != nil || in.Title == "" {
			return c.JSON(http.StatusBadRequest, map[string]string{"error": "title is required"})
		}
		n := Note{Title: in.Title, Body: in.Body, CreatedAt: time.Now()}
		if err := quark.For[Note](c.Request().Context(), client).Create(&n); err != nil {
			if quark.IsUniqueViolation(err) {
				return c.JSON(http.StatusConflict, map[string]string{"error": "a note with that title already exists"})
			}
			return err
		}
		return c.JSON(http.StatusCreated, n)
	})

	return e
}
