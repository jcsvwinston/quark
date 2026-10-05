// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package integrations holds Quark served from the routers and servers it is
// used with — net/http, chi, Echo, Gin, gRPC and Nucleus — as tested
// fixtures.
//
// A fixture here is a package that builds a small notes API on a
// *quark.Client and a test that drives it over the network against SQLite
// (HTTP, or gRPC through the generated client). They are
// not examples: there is no main to run and nothing to copy a directory from.
// They exist so that the code the frameworks guide
// (website/docs/guides/frameworks.mdx) shows is code a build checks. Every
// fixture package carries TestGuideMatchesFixture, which fails when a Go block
// of its section of the guide is not, line for line, code of the package —
// so the page cannot drift from what CI compiles.
//
// It is a module of its own for the reason the engine suites and the
// acceptance harness are (ADR-0024): the frameworks it requires must never
// reach the library's go.mod, and an application that imports Quark must not
// find chi, Echo, Gin, gRPC or Nucleus in its build list — the library does
// not depend on Nucleus at all, by decision; a module nothing publishes may. Nothing publishes it — the local
// replace directives point every Quark requirement back into this tree — and
// it sits under internal/ so that Go itself refuses an import of it from
// outside the repository: it is unpublishable by construction, which is also
// how the suite's release tooling recognises a module it must not release.
//
// What every HTTP fixture serves, and answers the same way (see notestest;
// the gRPC service has the same four methods and answers with codes):
//
//	GET  /notes          the newest hundred notes
//	GET  /notes/{id}     one note, or 404
//	POST /notes          create one; 400 without a title, 409 on a taken title
//	POST /notes/import   create several in ONE transaction: all or none
package integrations
