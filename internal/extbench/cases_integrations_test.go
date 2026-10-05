// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package extbench

// The "integrations" family: Quark behind the routers and servers it is
// served from. The owner's decision of 2026-10-04 says where they live —
// tested fixtures, modules the CI compiles and runs, not an examples/
// directory — so the bench counts an integration once a module of the
// repository requires the framework, the packages that import it pass their
// tests standalone, and one of those tests holds the guide's section to that
// code. A guide's code block alone does not count: nothing compiles it.

func controlsIntegrations() []control {
	return []control{
		{
			id:     "INT-01",
			family: "integrations",
			title:  "chi: a module of the repository serves Quark behind a chi router, its tests pass standalone, and they hold the guide's chi section to that code",
			want:   present,
			probe:  probeIntegrationChi,
		},
		{
			id:     "INT-02",
			family: "integrations",
			title:  "Echo: a module of the repository serves Quark behind an Echo server, its tests pass standalone, and they hold the guide's Echo section to that code",
			want:   present,
			probe:  probeIntegrationEcho,
		},
		{
			id:     "INT-03",
			family: "integrations",
			title:  "Gin: a module of the repository serves Quark behind a Gin engine, its tests pass standalone, and they hold the guide's Gin section to that code",
			want:   present,
			probe:  probeIntegrationGin,
		},
		{
			id:     "INT-04",
			family: "integrations",
			title:  "gRPC: a module of the repository serves Quark behind a gRPC service, its tests pass standalone, and they hold the guide's gRPC section to that code",
			want:   absent,
			note:   "Measured: no module of the repository requires google.golang.org/grpc directly — the acceptance harness and the engine suites carry it only as an indirect requirement, which no package of theirs imports — and the frameworks guide has no gRPC section. Of the five integrations it is the only one with no text to start from.",
			probe:  probeIntegrationGRPC,
		},
		{
			id:     "INT-05",
			family: "integrations",
			title:  "Nucleus: a module of the repository serves Quark from a Nucleus module, its tests pass standalone, and they hold the guide's Nucleus section to that code",
			want:   absent,
			note:   "Measured: no module of the repository requires github.com/jcsvwinston/nucleus. The library does not depend on Nucleus by decision (QADR-0001, QADR-0006), so the fixture has to be a module of its own that requires both — the shape of the acceptance harness — or live on Nucleus's side. The guide's Nucleus section shows 6 lines and points at quark init --with nucleus, whose output nothing in this repository compiles (INT-06).",
			probe:  probeIntegrationNucleus,
		},
		{
			id:     "INT-06",
			family: "integrations",
			title:  "quark init --with writes each official integration, and what it writes is compiled by a module of the repository",
			want:   partial,
			note:   "Measured on the CLI's flag validation — the CLI is a module of its own (ADR-0024) the bench cannot import, so it reads the initWithTargets literal the validation consults, with go/parser: --with accepts nucleus and refuses chi, echo, gin and grpc. What --with nucleus writes is source for a framework no module of this repository requires, so nothing here compiles it.",
			probe:  probeInitWith,
		},
	}
}
