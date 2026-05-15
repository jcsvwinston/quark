// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Example: quark tenant install-rls-policies — minimal embedding of
// the quarktenant library into a user-owned binary that can be run
// from CI / Makefile to install PostgreSQL row-level security
// policies for every model registered on the [quark.Client].
//
// Usage:
//
//	QUARK_DSN=postgres://app:pw@localhost:5432/myapp \
//	  go run ./examples/tenant-rls-native install-rls-policies --dry-run
//
// or, to apply for real:
//
//	go run ./examples/tenant-rls-native install-rls-policies
//
// The library only supports PostgreSQL; running this against any
// other dialect returns ExitError with a clear message.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/quarktenant"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// Order and Invoice stand in for the real models the host
// application would register. Both carry a tenant_id column matching
// the default policy template emitted by quarktenant.
type Order struct {
	ID       int64  `db:"id" pk:"true"`
	TenantID string `db:"tenant_id"`
	Status   string `db:"status"`
}

type Invoice struct {
	ID       int64  `db:"id" pk:"true"`
	TenantID string `db:"tenant_id"`
	Amount   int64  `db:"amount"`
}

func main() {
	dsn := os.Getenv("QUARK_DSN")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "QUARK_DSN must be set to a PostgreSQL DSN")
		os.Exit(quarktenant.ExitError)
	}

	client, err := quark.New("pgx", dsn, quark.WithLimits(quark.Limits{
		AllowRawQueries: true,
	}))
	if err != nil {
		fmt.Fprintf(os.Stderr, "quark.New: %v\n", err)
		os.Exit(quarktenant.ExitError)
	}
	defer func() { _ = client.Close() }()

	if err := client.RegisterModel(&Order{}, &Invoice{}); err != nil {
		fmt.Fprintf(os.Stderr, "RegisterModel: %v\n", err)
		os.Exit(quarktenant.ExitError)
	}

	os.Exit(quarktenant.Run(context.Background(), os.Args[1:], client))
}
