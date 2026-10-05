// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package extbench

// The "drivers" family: shipping a database engine for Quark from outside
// this repository. ADR-0023 moved the engines out of the library and gave the
// classifier a leaf contract (quarkdriver); this family measures how far that
// goes for a driver nobody in this repository writes. The bench found the
// query path open and the schema path keyed on the dialect's name (A11 Q2
// closed that), and the dialect contract in package quark (A11 Q3 moved it to
// quarkdriver).

func controlsDrivers() []control {
	return []control{
		{
			id:     "DRV-01",
			family: "drivers",
			title:  "RegisterDialect is safe to call while dialects are being resolved",
			want:   present,
			note:   "Measured in a child process under -race: RegisterDialect writing while DetectDialect and DetectDialectByName read is clean since A11 Q1, which put the registry behind a sync.RWMutex — the same guard the other four registries already had (CON-08). Before it, the registry was a plain map and the detector reported a DATA RACE: a driver that registers from init() is serialised by Go, but a registration after start-up (a test registering its own dialect, a module loaded late) raced with quark.New, which calls DetectDialect. Removing the lock turns this control back to absent.",
			probe:  probeDialectRegistryRace,
		},
		{
			id:     "DRV-02",
			family: "drivers",
			title:  "A driver module outside this repository registers its dialect and its classifier without importing package quark",
			want:   present,
			note:   "Measured with go list -deps on a driver module built standalone (GOWORK=off) as example.com/extdriver, whose three packages register through quarkdriver alone: errs the classifier (163 packages), dialect a SQLite dialect written against quarkdriver.Dialect, LockOptions, ErrUnsupportedFeature, SchemaIntrospector and the schema model (72 packages), and the module root the database/sql driver and quarkdriver.RegisterDialect (165 packages). None imports package quark. Present since A11 Q3 (ADR-0026), which moved the dialect contract to quarkdriver and left every name in package quark as an alias of the same type; against v1.15.0 the dialect half could not avoid package quark — RegisterDialect lived there and Dialect.LockSuffix named quark.LockOptions — and the driver took 208 packages. Making the fixture's dialect import package quark turns this control back to partial (161 packages for the dialect half, 208 for the driver). The fixture's own test is the application, and imports package quark.",
			probe:  probeRegistrationWithoutRoot,
		},
		{
			id:     "DRV-03",
			family: "drivers",
			title:  "A driver module outside this repository, built standalone against this tree, opens its engine by name, reads and writes, classifies its duplicate key and passes drivertest.Verify",
			want:   present,
			note:   "The dialect the fixture registers is its own, written against quarkdriver alone (DRV-02); the end-to-end test also takes a row lock its engine refuses — quarkdriver.ErrUnsupportedFeature from the dialect, matched as quark.ErrUnsupportedFeature — and reads the table back through the dialect's SchemaIntrospector as a quark.Schema. The fixture creates its table by hand: what Migrate writes for an engine name Quark does not know is DRV-04's subject, and the kit it passes checks classifiers only (DRV-05).",
			probe:  probeExternalDriverEndToEnd,
		},
		{
			id:     "DRV-04",
			family: "drivers",
			title:  "A dialect from outside is a full participant: SQLite's own dialect methods under another name behave as SQLite's do",
			want:   present,
			note:   "Measured with a battery of 20 steps on two arms — the same engine and the same dialect methods, one named sqlite and one extlite: all 20 agree since A11 Q2. The 11 query steps always did. The 9 schema steps now ask the dialect instead of reading its name: its column types (quarkdriver.ColumnTyper), its auto-increment key (quarkdriver.AutoIncrementer), how it creates only what is missing (quarkdriver.IdempotentDDL), the current columns for Sync (SchemaIntrospector), whether ApplyPlan can run in one transaction (Dialect.SupportsTransactionalDDL) and whether a column change or a constraint goes through SQLite's table rebuild (quarkdriver.TableRebuilder). Against v1.15.0 all 9 diverged: Migrate wrote BIGINT PRIMARY KEY, so the first insert left the key NULL; a second Migrate failed; PlanMigration proposed six changes against SQLite's own table; Sync failed; ApplyPlan refused even an added column. The extlite wrapper forwards every optional interface SQLite's dialect implements — embedding the Dialect interface promotes nothing else — and the probe fails if it stops mirroring them. Putting back any one of the name checks turns a step red; the transactional-DDL check is seen only by the step whose plan fails half-way, because an op that succeeds succeeds on the resumable path too.",
			probe:  probeFullParticipant,
		},
		{
			id:     "DRV-05",
			family: "drivers",
			title:  "The conformance kit checks a driver's dialect: placeholders, quoting, upsert, limit and savepoints",
			want:   absent,
			note:   "Measured on the kit's type-checked API: no field of drivertest.Case and no function of drivertest takes a Dialect, so a dialect that quotes identifiers unsafely or numbers its placeholders wrong passes the kit — it is never shown one. The kit checks the three classifier predicates and nothing else. When the kit gains a way to take a dialect, this probe stops and asks to be extended to run it against a dialect that is wrong on purpose.",
			probe:  probeKitChecksDialect,
		},
		{
			id:     "DRV-06",
			family: "drivers",
			title:  "Every driver module of this repository runs the conformance kit",
			want:   partial,
			note:   "Measured by running each driver module's tests against this tree (a go.work, as CI's driver lane builds) and looking for the kit's subtests: mssql, mysql, oracle and sqlite run drivertest.Verify; postgres does not. Its module registers no classifier by design — PostgreSQL errors are classified through the SQLState() method every PostgreSQL driver exposes (CON-07) — and the kit has nothing else to check, so the engine the kit never sees is PostgreSQL.",
			probe:  probeEveryDriverRunsKit,
		},
		{
			id:     "DRV-07",
			family: "drivers",
			title:  "A driver module outside this repository can run the engine conformance suite the in-repo engines run",
			want:   absent,
			note:   "Measured: the suite is internal/enginesuite, whose only non-test file is doc.go — SharedSuite and the per-engine suites live in its 100 _test.go files, which no importer can reach — and the go command refuses a module outside the repository that imports an internal package of quark (\"use of internal package … not allowed\"). An external driver can prove its classifier (DRV-03) and nothing about the SQL its dialect writes.",
			probe:  probeEngineSuiteReachable,
		},
		{
			id:     "DRV-08",
			family: "drivers",
			title:  "A driver template module builds standalone (GOWORK=off) and passes the kit",
			want:   absent,
			note:   "Measured over every go.mod of the repository: 12 modules — the library, the CLI, the five drivers, the acceptance harness, the benchmarks, the bug-bash harness, the engine suites and the integration fixtures — and no other module requires the library, so nothing is a template for a driver someone else writes. The nearest things are the five drivers, each one engine's module pinned to a published quark, and the fixture this bench builds for DRV-03, which lives in testdata.",
			probe:  probeDriverTemplate,
		},
	}
}
