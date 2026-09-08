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
//
// Releases cut from the first signed tag onward also publish a prebuilt
// archive per platform, each with an SPDX SBOM beside it, a cosign signature
// over the checksum file and a build provenance attestation. The
// documentation page "Verifying a release", under Operations, has the two
// commands that check one.
package main

import (
	"github.com/jcsvwinston/quark/cmd/quark/commands"
)

// commands.Main prints any error to stderr and exits 1 — the same contract
// embedded runners get from the documented recipe (QCD-CLI-2).
func main() {
	commands.Main()
}
