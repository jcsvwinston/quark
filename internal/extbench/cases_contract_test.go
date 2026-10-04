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
			want:   absent,
			note:   "Measured on the type-checked API: 43 exported types of quark and quarkdriver can be implemented outside the package — 30 interfaces with no unexported method, 12 function types and one record of functions (quarkdriver.Classifier). No table on any page under website/docs carries a stability column, so none of the 43 is declared; the page that names the most of them in code spans (reference/api/observability.mdx) names 9. The census mixes invitations (Dialect, Middleware, CacheStore, EventBus, the hooks) with plumbing a caller is not meant to implement (Option, Scope, Executor, Expr, Result, Row): drawing that line is the contract's job, and nothing draws it today.",
			probe:  probeContractPage,
		},
		{
			id:     "CON-02",
			family: "contract",
			title:  "The extension surface is frozen by a test that fails when a member a third party depends on changes",
			want:   partial,
			note:   "Measured against acceptance/apisurface.json, the surface CI regenerates and diffs on every pull request: 79 of the 82 members a third-party implementation depends on are recorded by name — every method of every implementable interface, every function type — and quarkdriver.Classifier's three predicates are not recorded at all (the generator lists a struct's methods, not its fields). No entry carries a signature: each holds pkg, name and kind, so changing a parameter of Dialect.UpsertSQL leaves the file byte-identical and every check green. Adding or removing a method does move the file, which CI reports as stale until it is regenerated — a freeze of names, not of the contract.",
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
			want:   partial,
			note:   "Measured with a recording database/sql driver underneath Quark, on a battery of what an application does in its first week (migrate, CRUD, upsert, a batch insert, a transaction with a savepoint, client.Exec and client.RawQuery, PlanMigration, Sync): 23 statements reached the engine, 10 passed through the middleware and 11 reached the observer. Neither sees schema work — the CREATE TABLE of Migrate and Sync, the ALTER TABLE of Sync, the introspection queries PlanMigration and Sync run (sqlite_master and five PRAGMAs) — nor the SAVEPOINT and ROLLBACK TO SAVEPOINT of a transaction. CreateBatch passes the middleware and never reaches the observer; client.Exec and client.RawQuery reach the observer and skip the middleware. Two middlewares do compose, the first registered outermost — the order the godoc's \"applied in the order they are added\" leaves to the reader. The OpenTelemetry package and Orbit's SQL bridge are both middlewares, so what the middleware does not see, neither a trace nor Orbit's feed shows.",
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
			want:   partial,
			note:   "Measured: three conventions Quark honours have no exported interface in quark or quarkdriver — a model's TableName() string, a model's Validate(context.Context) error, and an error's SQLState() string, which classifies any driver's error by its PostgreSQL code. Each was exercised: the table took the name, the validation error aborted the insert, a 23505 classified as a unique violation and a 40P01 as a deadlock. None has a name a third party can write `var _ quark.X = (*T)(nil)` against; Quark asserts them against an interface in an internal package (internal/schema.TableNamer) and two anonymous ones (validator.go, db_errors.go). The hooks of CON-03 show the shape the other three lack.",
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
