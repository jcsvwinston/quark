// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package extbench

// The "contract" family: the extension points a third party programs
// against. Four of its controls are already present and say so — a hook, a
// store, a bus, a type mapper all work from outside the package — because a
// contract is first a list of what works, and the arc's first deliverable is
// to write that list down and freeze it. The gaps are in the writing down and
// the freezing, and in what the two observation points cannot see.

func controlsContract() []control {
	return []control{
		{
			id:     "CON-01",
			family: "contract",
			title:  "A published page declares, for every type a third party can implement and hand to Quark, whether it is an extension point and at what stability",
			want:   present,
			note:   "Measured on the type-checked API: 52 exported types of quark and quarkdriver can be implemented outside the package — 39 interfaces with no unexported method, 12 function types and one record of functions (quarkdriver.Classifier). Since A11 Q6 website/docs/reference/extension-contract.mdx carries one table with a row for each: the type (by the package that declares it, its quark alias beside it), whether it is an extension point (yes/no), its stability (stable/experimental/internal-use) and how it reaches Quark. The probe finds the tables with a stability column that name a census type, requires exactly one, and checks it covers the census exactly: no type missing, none twice (an alias and its target are one type), no row naming something outside the census, no value outside the vocabulary. Before it no page declared any of the 49 types then in the census. Dropping a row, adding a dangling one, repeating a type under its alias or writing a fourth stability each turns this control back to partial, and so does a new implementable type without a row. The page draws the line the census could not, as a proposal pending the owner's decision (until it is adopted, the v1 promise of upgrade.mdx stands for every exported type, and the page says so): 40 extension points and 12 plumbing (Option, Expr, Event, IdentifierValidator, Executor, the four migration-lock adapters, the two codegen function types and the deprecated NewListenerFunc); 43 stable, 6 experimental (the schema-path interfaces of A11 Q2) and 3 internal-use (the codegen function types and NewListenerFunc).",
			probe:  probeContractPage,
		},
		{
			id:     "CON-02",
			family: "contract",
			title:  "The extension surface is frozen by a test that fails when a member a third party depends on changes",
			want:   present,
			note:   "Measured against acceptance/apisurface.json, the surface CI regenerates and diffs on every pull request. Since A11 Q6 every func, method and type in it carries a sig — parameter and result types without names, a struct's exported fields, a func type's signature, and for an alias of an internal type (quark.TypeMapper, quark.TableNamer) the whole shape — and the probe renders, from the compiler's export data and by the generator's rules, the sig of each of the 94 symbols that fix what a third-party implementation depends on (every method of every implementable interface, every function type, quarkdriver.Classifier's fields): all 94 are in the file with the compiler's signature. Before it each entry held pkg, name and kind, Classifier's predicates were not recorded at all, and changing a parameter of Dialect.UpsertSQL left the file byte-identical. Changing that parameter from int to int64 now moves seven lines (the interface's UpsertSQL and the six built-ins', which CI's freshness check prints with the symbol and both signatures) and turns this control to partial until the file is regenerated.",
			probe:  probeSurfaceFreeze,
		},
		{
			id:     "CON-03",
			family: "contract",
			title:  "The eight model hooks (Before/After Create, Update, Delete, Find) fire on a model declared outside package quark, and a Before hook's error aborts the write",
			want:   present,
			probe:  probeModelHooks,
		},
		{
			id:     "CON-04",
			family: "contract",
			title:  "Every statement Quark sends to the engine passes through the Middleware chain and reaches the QueryObserver",
			want:   present,
			note:   "Measured with a recording database/sql driver underneath Quark, on a battery of what an application does in its first week (migrate, CRUD, upsert, a batch insert, a transaction with a savepoint, client.Exec and client.RawQuery, PlanMigration, Sync): 27 statements reached the engine, and since A11 Q7 the middleware and the observer each see all 27, in the engine's order, with the same kind — 8 query, 2 exec, 3 ddl, 10 introspection, 2 savepoint, 2 raw. Before it the middleware saw 10 and the observer 11: neither saw schema work (the CREATE and ALTER TABLE of Migrate and Sync, the catalog reads of PlanMigration and Sync) nor the savepoints, CreateBatch skipped the observer, and client.Exec and client.RawQuery skipped the middleware. One execution seam now composes the chain around the call that reaches the engine and reports the event; the schema paths, a dialect's SchemaIntrospector, ColumnAlterer and TableRebuilder, the migration lock and the migrate and quarktenant packages get an Executor that goes through it. QueryEvent.Kind and quark.StatementKindFromContext name what each statement is for, and the probe requires the battery to produce all six kinds. Two middlewares compose, the first registered outermost, as WithMiddleware's godoc now says. Sending Migrate's DDL past the seam turns this control back to partial; so does a battery that stops producing one kind. The OpenTelemetry package and Orbit's SQL bridge are middlewares: a trace now shows the schema work and the savepoints, each span tagged with its kind, and Orbit's feed will once its bridge requires a Quark with this change.",
			probe:  probeInterception,
		},
		{
			id:     "CON-05",
			family: "contract",
			title:  "A third-party CacheStore serves cached reads and is invalidated by a write; a third-party EventBus hears committed writes only, after the commit",
			want:   present,
			probe:  probeStoresAndBus,
		},
		{
			id:     "CON-06",
			family: "contract",
			title:  "A registered TypeMapper decides the column type Migrate writes for a Go type Quark does not know, and is handed the dialect's name",
			want:   present,
			probe:  probeTypeMapper,
		},
		{
			id:     "CON-07",
			family: "contract",
			title:  "Every method Quark calls on a caller's type has an exported interface to implement and assert against",
			want:   present,
			note:   "Measured: the three conventions Quark honours on a caller's type each have an exported interface since A11 Q6 — quark.TableNamer for a model's TableName() string, quark.Validator for a model's Validate(context.Context) error, and quarkdriver.SQLStater for an error's SQLState() string, which classifies any driver's error by its PostgreSQL code. Each was exercised: the table took the name, the validation error aborted the insert, a 23505 classified as a unique violation and a 40P01 as a deadlock. Quark asserts against these types itself: quark.TableNamer is an alias of the internal/schema interface the model metadata asserts, Client.Validate asserts quark.Validator, and the PostgreSQL classification asserts quarkdriver.SQLStater through errors.As. Each is additive: a type with the method already satisfies the interface. Before A11 Q6 Quark asserted them against an internal interface and two anonymous ones, and none had a name a third party could write var _ quark.X = (*T)(nil) against; removing any of the three turns this control back to partial.",
			probe:  probeImplicitConventions,
		},
		{
			id:     "CON-08",
			family: "contract",
			title:  "The global registries other than the dialect's — classifiers, listener factories, type mappers, generated scanners and binders — are race-free under the race detector",
			want:   present,
			probe:  probeOtherRegistriesRace,
		},
	}
}
