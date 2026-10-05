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
			want:   present,
			probe:  probeIntegrationGRPC,
		},
		{
			id:     "INT-05",
			family: "integrations",
			title:  "Nucleus: a module of the repository serves Quark from a Nucleus module, its tests pass standalone, and they hold the guide's Nucleus section to that code",
			want:   present,
			probe:  probeIntegrationNucleus,
		},
		{
			id:     "INT-06",
			family: "integrations",
			title:  "quark init --with writes each official integration, and what it writes is compiled by a module of the repository",
			want:   partial,
			note:   "Measured on the CLI's flag validation — the CLI is a module of its own (ADR-0024) the bench cannot import, so it reads the initWithTargets literal the validation consults, with go/parser: --with accepts nucleus and refuses chi, echo, gin and grpc. What --with nucleus writes is still built by nothing. Since A11 Q9 a module of the repository requires Nucleus — internal/integrations, whose Nucleus fixture is written by hand in the shape the template writes — and this probe counts a module that requires the framework as compiling the target's output, so it no longer lists nucleus as uncompiled; that check has to read the template's output itself before it can say so.",
			probe:  probeInitWith,
		},
	}
}
