// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enterprisebench

// controlsMigraciones is the migration family: what an enterprise application
// can do to change its schema on purpose — compare, plan, apply, roll back,
// serialise and backfill.
//
// Every title here says only what its probe reads back from the database. Where
// the probe could measure half of a capability, the title is the half it
// measured: a title that promises more than its probe checks is a control that
// passes for ever.
//
// And every verdict here has ONE way to be reached. A probe that answers
// "partial" both when a capability is half there and when it is gone measures
// nothing: the bench stays green through the regression. So each probe's
// recorded verdict is the conjunction of the facts its title states, and every
// other split returns something else, with a log saying which fact moved.
func controlsMigraciones() []control {
	const fam = "migraciones"
	return []control{
		{
			id:     "MIG-01",
			family: fam,
			title:  "Declarative diff: a desired schema is compared against the live one, applied, and the loop converges — tables, columns, indexes and foreign keys",
			want:   present,
			note: "Closed at A8 S3 (QK-27): OpCreateTable is emitted whole — foreign keys and checks inline, " +
				"indexes as CREATE INDEX — Diff orders new tables parents-first, and a foreign key is matched " +
				"by what it is (columns, target) rather than by a name SQLite does not keep, so re-diffing the " +
				"applied schema proposes nothing.",
			probe: probeMigDeclarativeDiff,
		},
		{
			id:     "MIG-02",
			family: fam,
			title: "A desired schema can come from a versionable document instead of compiled Go models, " +
				"and everything it declares arrives",
			want: present,
			note: "A JSON document deserialises into quark.Schema (every field is exported) and goes through " +
				"quark.Diff and ApplyPlan with no Go model compiled in; since A8 S3 the index and the foreign " +
				"key it declares arrive with the table. PlanMigration is not the way in: it reflects over Go " +
				"values, which is MIG-11's subject.",
			probe: probeMigSchemaFromDocument,
		},
		{
			id:     "MIG-03",
			family: fam,
			title:  "The plan built from models carries the model's index set: a declared index (quark:\"index\") that is missing is proposed, and an undeclared live index is left alone",
			want:   present,
			note: "Retitled at S3. The S0 title asked for \"creating or dropping\"; dropping an index the model " +
				"does not name is the one thing the plan refuses on purpose, because a model that cannot " +
				"describe every catalog object must not propose destroying the ones it is silent about. " +
				"What the plan does carry is the index set the model DECLARES: Migrate creates it, " +
				"PlanMigration proposes it when missing, and a quark:\"unique\" column is matched to its " +
				"engine-named backing index by shape.",
			probe: probeMigModelDeclaredIndexes,
		},
		{
			id:     "MIG-04",
			family: fam,
			title:  "Plan.Hash is a deterministic 64-character digest that separates plans that differ",
			want:   present,
			probe:  probeMigPlanHash,
		},
		{
			id:     "MIG-05",
			family: fam,
			title:  "Applying a plan detects that the schema changed between planning and applying",
			want:   absent,
			note: "The plan's digest is a function of its ops alone, so it does not move when the database " +
				"does, and ApplyPlan re-reads nothing. Measured twice: a plan stale against a table it does " +
				"not touch applies with no error and with no log line about it at any level — the probe " +
				"installs a Debug-level sink rather than assuming the silence — and a plan that collides " +
				"with the change is stopped by SQLite's own \"table already exists\", not by anything that " +
				"knows the plan is out of date. That layer is measured, not read: a collision stopped for " +
				"any other reason, or stopped by nothing at all, fails this control instead of sharing " +
				"this absent.",
			probe: probeMigStaleSchemaDetected,
		},
		{
			id:     "MIG-06",
			family: fam,
			title:  "ApplyPlan is all-or-nothing on SQLite, and a CREATE TABLE it reports as applied is applied whole",
			want:   present,
			note: "The rollback half: a failing second op leaves nothing of the first behind. The other half " +
				"since A8 S3: an OpCreateTable carrying an index creates the table WITH it (QK-27). The " +
				"engines with no transactional DDL (MySQL, MariaDB, Oracle) and their resumable checkpoint " +
				"path need a live engine — internal/enginesuite.",
			probe: probeMigApplyPlan,
		},
		{
			id:     "MIG-07",
			family: fam,
			title:  "ALTER COLUMN covers type, nullable, default and primary key",
			want:   present,
			note: "Closed at A8 S4. On SQLite every delta goes through the table rebuild the engine's manual " +
				"documents (no ALTER COLUMN there), and each is read back from the catalog. On the other " +
				"engines the deltas are native ALTERs — PostgreSQL per facet, MySQL/MariaDB as one MODIFY, " +
				"SQL Server with its named default and key constraints, Oracle as one MODIFY of what " +
				"changed — proven per engine in internal/enginesuite.",
			probe: probeMigAlterColumn,
		},
		{
			id:     "MIG-08",
			family: fam,
			title:  "Plan.Down derives a rollback, applies it, and refuses what it cannot invert",
			want:   present,
			probe:  probeMigPlanDown,
		},
		{
			id:     "MIG-09",
			family: fam,
			title:  "Reversible round trips beyond CREATE/DROP TABLE: column, index and foreign key",
			want:   present,
			note: "Closed at A8 S4: on SQLite a foreign key is added and dropped by rebuilding the table, and " +
				"the name the op carries is kept in the CREATE TABLE text so the generated rollback can " +
				"find it. Add-column and create-index round-tripped already.",
			probe: probeMigReversibleBeyondTables,
		},
		{
			id:     "MIG-10",
			family: fam,
			title:  "Distributed migration lock: refused on SQLite, and the versioned migrator runs anyway without it",
			want:   partial,
			note: "AcquireMigrationLock answers ErrUnsupportedFeature on SQLite, which a caller can act on. " +
				"The migrator that asks for it by default degrades to running unserialised and reports it at " +
				"Debug only — measured with a sink on the migrator's logger: the degradation line is there, " +
				"at Debug, and nothing at Warn or above came with it. A migrator that refused, or that " +
				"returned nil without applying, answers absent. The five engines that implement the lock " +
				"need internal/enginesuite.",
			probe: probeMigLock,
		},
		{
			id:     "MIG-11",
			family: fam,
			title:  "PlanMigration serves a binary that has none of the user's compiled models",
			want:   absent,
			note: "Planning with no models — all a precompiled CLI can pass — yields a plan that drops the " +
				"live tables, because the desired schema is whatever Go values the caller supplies. This is " +
				"the obstacle a `quark migrate diff` subcommand would have to clear through this entry " +
				"point; the route that does work without compiled models is a schema document through " +
				"quark.Diff and ApplyPlan, which MIG-02 measures with its own limits.",
			probe: probeMigDiffWithoutCompiledModels,
		},
		{
			id:     "MIG-12",
			family: fam,
			title:  "Embeddable plan/verify/apply wrapper with CI exit codes",
			want:   present,
			probe:  probeMigEmbeddableWrapper,
		},
		{
			id:     "MIG-13",
			family: fam,
			title:  "A plan can be rendered as the DDL of a migration file from the library",
			want:   absent,
			note: "The only rendering the library offers names the table and drops its columns, their types " +
				"and its indexes, so nothing callable from Go can write a migration file. The CLI's " +
				"`migrate create --from-models` does generate one, but it lives in the cmd/quark module, " +
				"out of this bench's reach, and its emitted DDL is unbranched per dialect. WHAT the " +
				"rendering drops decides this verdict: a rendering that carried the columns and their " +
				"types and still dropped the indexes would be the half MIG-01 and MIG-02 call partial, " +
				"and answers partial here.",
			probe: probeMigRenderPlanAsDDL,
		},
		{
			id:     "MIG-14",
			family: fam,
			title:  "Hand-written versioned migrations: ledger, dry run, up, down",
			want:   present,
			note: "The cycle needs the client the migration guide prescribes — AllowRawQueries enabled. " +
				"Measured, not read: on a default-limits client Init and the migration body go through " +
				"Raw() and are not refused, and the run fails at the LEDGER INSERT, which the migrator does " +
				"through Client.Exec (ErrInvalidQuery, \"raw queries are disabled by default\") — after the " +
				"schema change already happened.",
			probe: probeMigVersionedMigrations,
		},
		{
			id:     "MIG-15",
			family: fam,
			title:  "Data backfill resumes where an interrupted run stopped",
			want:   present,
			probe:  probeMigBackfill,
		},
		{
			id:     "MIG-16",
			family: fam,
			title:  "On SQLite the resumable checkpoint table is never created (ApplyPlan takes the transactional path)",
			want:   absent,
			note: "Measured here, on the one dialect this bench runs: after a successful ApplyPlan, " +
				"quark_migration_state is not in sqlite_master. Read, not measured: supportsTransactionalDDL " +
				"in migrate_execute.go puts PostgreSQL and SQL Server on the same path, so the checkpoint " +
				"DDL written for those three dialects never runs there either — proving that needs a live " +
				"engine. MySQL, MariaDB and Oracle take the other path and do exercise it, in " +
				"internal/enginesuite.",
			probe: probeMigCheckpointTable,
		},
	}
}
