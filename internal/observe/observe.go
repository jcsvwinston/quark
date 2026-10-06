// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package observe lets the packages of this module that send statements on a
// *quark.Client's behalf, but do not live in package quark — the migrate
// package's ledger, quarktenant's policy install and check — send them
// through the client's execution seam, so they pass its middleware chain and
// reach its query observers like every statement package quark sends.
//
// Package quark installs Executor when it is initialised, and a package that
// holds a *quark.Client has imported package quark, so the hook is always set
// by the time it is called. It is internal so the seam stays an
// implementation detail: outside this module a client's chain is reached
// through package quark's API alone.
package observe

import "github.com/jcsvwinston/quark/quarkdriver"

// The statement kinds the subpackages use, spelled as package quark's
// StatementKind values: this package cannot import package quark, which
// imports it.
const (
	KindDDL           = "ddl"
	KindIntrospection = "introspection"
)

// Executor returns exec wrapped so that what it executes reaches client's
// execution seam as a statement of kind execKind, and what it reads as one
// of kind readKind. client is a *quark.Client.
var Executor func(client any, exec quarkdriver.Executor, execKind, readKind string) quarkdriver.Executor
