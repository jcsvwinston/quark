// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Command migrations is a minimal example of using the
// `quarkmigrate` package to wire a plan/verify/apply CLI workflow
// for a Quark-managed schema. Adapt this to your project by
// replacing the models in `myModels()` and the DSN in `loadDSN()`.
//
//	go run ./examples/migrations plan     # show what would change
//	go run ./examples/migrations verify   # exit 1 if drift (CI gate)
//	go run ./examples/migrations apply    # actually run the plan
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/quarkmigrate"

	_ "modernc.org/sqlite"
)

// User and Post are the example models. In a real project these
// would live in your `models/` package and be passed to
// `quarkmigrate.Run` from a thin wrapper main.go.
type User struct {
	ID    int64  `db:"id" pk:"true"`
	Email string `db:"email" quark:"not_null,unique"`
}

type Post struct {
	ID     int64  `db:"id" pk:"true"`
	UserID int64  `db:"user_id" quark:"not_null"`
	Title  string `db:"title" quark:"not_null"`
}

func myModels() []any {
	return []any{&User{}, &Post{}}
}

// loadDSN reads QUARK_DIALECT + QUARK_DSN from the environment with
// sensible defaults for the example. In a real deployment you'd
// want this to fail loudly when the env vars are missing rather
// than silently using a sqlite file.
func loadDSN() (string, string) {
	dialect := os.Getenv("QUARK_DIALECT")
	if dialect == "" {
		dialect = "sqlite"
	}
	dsn := os.Getenv("QUARK_DSN")
	if dsn == "" {
		dsn = "file:example-migrations.db?cache=shared"
	}
	return dialect, dsn
}

func argOrEmpty(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

func main() {
	dialect, dsn := loadDSN()

	client, err := quark.New(dialect, dsn)
	if err != nil {
		fmt.Fprintf(os.Stderr, "quark.New(%s): %v\n", dialect, err)
		os.Exit(quarkmigrate.ExitError)
	}
	defer client.Close()

	action, err := quarkmigrate.ParseAction(argOrEmpty(os.Args, 1))
	if err != nil {
		fmt.Fprintf(os.Stderr, "argument error: %v\n", err)
		fmt.Fprintln(os.Stderr, "usage: migrations [plan|verify|apply]")
		os.Exit(quarkmigrate.ExitError)
	}

	os.Exit(quarkmigrate.Run(context.Background(), action, client, myModels()...))
}
