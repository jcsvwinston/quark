// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// DX-N3: `quark migrate create <name> --from-models <dir> --emit json` hands
// the rendered DDL to another program instead of writing a Go migration
// file. The first consumer is nucleus's `makemigrations`, which writes the
// up/down pair in its own migration format; it only needs the statements as
// data, so the contract between the two binaries is the JSON printed here,
// not a Go type they would both have to import.
package commands

import (
	"encoding/json"
	"io"
)

// DDLDocumentSchema is the value of the "schema" field DDLDocument carries
// today. It is the contract version of the JSON `--emit json` prints: bump
// it ONLY on an incompatible change (a field renamed or removed, a value
// whose meaning changes), and never for an added field — a consumer that
// reads schema 1 must keep decoding what a later CLI prints while the number
// stays at 1. Say what changed in this constant's godoc when it moves.
const DDLDocumentSchema = 1

// DDLDocument is the JSON document `quark migrate create --from-models <dir>
// --emit json` prints on stdout, and nothing else is printed there in that
// mode. The consumer is another binary, so the contract is the encoded JSON,
// not this type: field names and their meaning are fixed by Schema, and a
// change that breaks a reader of the current Schema value bumps it (see
// DDLDocumentSchema).
//
//	{
//	  "schema": 1,
//	  "tool": "quark",
//	  "version": "v1.2.2",
//	  "dialect": "postgres",
//	  "models": [{"name": "User", "table": "users"}],
//	  "up":   ["CREATE TABLE IF NOT EXISTS users (\n\tid BIGSERIAL PRIMARY KEY\n)"],
//	  "down": ["DROP TABLE IF EXISTS users"]
//	}
//
// Up and Down are the statement lists in execution order — Up creates
// parents before children, Down drops in reverse — exactly as the `--emit
// go` migration would run them through client.Exec: no trailing semicolon,
// embedded newlines and tabs kept. Models lists every struct the DDL was
// rendered from, sorted by Go type name, with the table name each maps to.
type DDLDocument struct {
	// Schema is the contract version; see DDLDocumentSchema.
	Schema int `json:"schema"`
	// Tool is always "quark", so a consumer reading several producers can
	// tell this one apart.
	Tool string `json:"tool"`
	// Version is the CLI's version string, the one `quark version` prints
	// after the word "quark" (a `cmd/quark/vX.Y.Z` tag's version, or
	// "devel" for an unstamped build).
	Version string `json:"version"`
	// Dialect is the normalized dialect name the statements were rendered
	// for: postgres, mysql, mariadb, sqlite, mssql or oracle. The spelling
	// passed to --dialect (postgresql, sqlserver, …) is not echoed back.
	Dialect string `json:"dialect"`
	// Models are the structs the DDL was rendered from, sorted by name.
	Models []DDLDocumentModel `json:"models"`
	// Up holds the CREATE statements in execution order.
	Up []string `json:"up"`
	// Down holds the DROP statements in execution order.
	Down []string `json:"down"`
}

// DDLDocumentModel is one entry of DDLDocument.Models: a model struct and
// the table its DDL targets.
type DDLDocumentModel struct {
	Name  string `json:"name"`
	Table string `json:"table"`
}

// newDDLDocument assembles the document for the models and statements
// buildDDLStatements produced for dialect. Up and Down are never null in the
// output: a list with nothing in it encodes as [].
func newDDLDocument(dialect string, models []ddlModel, up, down []string) DDLDocument {
	doc := DDLDocument{
		Schema:  DDLDocumentSchema,
		Tool:    "quark",
		Version: cliVersion(),
		Dialect: dialect,
		Models:  make([]DDLDocumentModel, 0, len(models)),
		Up:      up,
		Down:    down,
	}
	for _, m := range models {
		doc.Models = append(doc.Models, DDLDocumentModel{Name: m.Name, Table: m.Table})
	}
	if doc.Up == nil {
		doc.Up = []string{}
	}
	if doc.Down == nil {
		doc.Down = []string{}
	}
	return doc
}

// writeDDLDocument encodes doc to w as one JSON document followed by a
// newline. HTML escaping is off so a CHECK such as `score < 100` reads as
// written; both forms decode to the same string.
func writeDDLDocument(w io.Writer, doc DDLDocument) error {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return enc.Encode(doc)
}
