// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package extbench

// The "integrations" family: Quark behind the routers and servers it is
// served from. The owner's decision of 2026-10-04 says where they live —
// tested fixtures, modules the CI compiles and runs, not an examples/
// directory — so the bench counts an integration once a module of the
// repository requires the framework, the packages that import it pass their
// tests standalone, and one of those tests holds the guide's section to that
// code. A guide's code block alone does not count: nothing compiles it. And
// `quark init --with` counts a target once what it writes compiles.

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
			title:  "quark init --with writes each official integration — chi, Echo, Gin, gRPC and Nucleus — and what it writes builds, vets and tests in a project of its own against this tree",
			want:   present,
			note:   "Measured by running the CLI's TestInitWithBuilds against this tree (the CLI is a module of its own that the bench cannot import, so it runs the module's test through a workspace, as CI's CLI lane does), after reading the initWithTargets literal the flag's validation consults: --with accepts all five, and for each the test writes a project with `quark init --with <target>`, replaces every Quark module it requires with this tree, and runs go mod tidy, go build, go vet and go test with no workspace — a dialect per target, so the driver rewrite compiles for every engine module. What init writes is the fixture's code (A11 Q10): the CLI embeds byte-for-byte copies of internal/integrations, a CLI test fails when a copy and its fixture differ, and init changes only the package clause and its doc comment, the import paths and the driver module, beside a server main for chi, Echo, Gin and gRPC. Before A11 Q10 --with accepted nucleus alone and what it wrote was built by nothing; a probe that counted a module requiring the framework as compiling the template's output said less than that. Breaking one copy turns its subtest red and this control to partial.",
			probe:  probeInitWith,
		},
	}
}
