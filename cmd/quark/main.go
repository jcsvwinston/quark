// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Command quark is the Quark ORM CLI: model scaffolding, migrations,
// seeders, multi-tenancy helpers, and code generation.
//
// Install it with:
//
//	go install github.com/jcsvwinston/quark/cmd/quark@latest
//
// and drive code generation from a model package with:
//
//	//go:generate quark gen ./...
package main

import (
	"fmt"
	"os"

	"github.com/jcsvwinston/quark/cmd/quark/commands"
)

func main() {
	if err := commands.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
