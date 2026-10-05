// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package extbench

// The "drivers" family: shipping a database engine for Quark from outside
// this repository. ADR-0023 moved the engines out of the library and gave the
// classifier a leaf contract (quarkdriver); this family measures how far that
// goes for a driver nobody in this repository writes. The bench found the
// query path open and the schema path keyed on the dialect's name (A11 Q2
// closed that), the dialect contract in package quark (A11 Q3 moved it to
// quarkdriver), and a kit that never saw a dialect and an engine suite no
// module outside the repository could reach (A11 Q4: drivertest.VerifyDialect
// and quarkdriver/drivertest/suite).

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
			note:   "The dialect the fixture registers is its own, written against quarkdriver alone (DRV-02); the end-to-end test also takes a row lock its engine refuses — quarkdriver.ErrUnsupportedFeature from the dialect, matched as quark.ErrUnsupportedFeature — and reads the table back through the dialect's SchemaIntrospector as a quark.Schema. Since A11 Q4 the same module also runs the dialect kit against its dialect (DRV-05) and the public engine suite against its engine (DRV-07); its dialect gained the AutoIncrementer and TableRebuilder its engine needs and an introspector that reads indexes and foreign keys, which is what the kit asked of it.",
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
			want:   present,
			note:   "Measured by running the kit from the fixture driver built standalone (GOWORK=off): drivertest.VerifyDialect takes the dialect and a live *sql.DB the driver's test opens, and checks every method of quarkdriver.Dialect and every optional interface the dialect implements (skipping, with the reason logged, the ones it does not), on queries and on the schema path — Migrate, PlanMigration, ApplyPlan, Sync, IntrospectSchema, savepoints, row locks, the migration lock — judged by what the engine then holds or refuses, never by the text of a statement. The fixture's own dialect passes with the engine half run; six dialects wrong on purpose each fail, and the failing subtest names the method: placeholders that all bind the first value (engine/Placeholder), quoting that does not escape the quote character (engine/Quote), an upsert that ignores the columns to update (engine/UpsertSQL), LIMIT and OFFSET swapped (engine/LimitOffset), a SavepointDialect whose rollback releases (engine/SavepointDialect), and an AutoIncrementer whose key the engine does not number (engine/AutoIncrementer). The six built-in dialects pass it against their engines in the engine suites; it found two of them wrong — MariaDB dropped a CHECK with MySQL's DROP CHECK (Error 1064) and SQL Server accepted a shared lock that skips locked rows, which the engine refuses (error 650) — both fixed in the same change. A kit that stops catching any one of the six turns this control partial.",
			probe:  probeKitChecksDialect,
		},
		{
			id:     "DRV-06",
			family: "drivers",
			title:  "Every driver module of this repository runs the conformance kit",
			want:   present,
			note:   "Measured by running each driver module's tests against this tree (a go.work, as CI's driver lane builds) and looking for the kits' subtests: all five run the dialect kit (drivertest.VerifyDialect on the built-in dialect their driver serves — MySQL's module both MySQL's and MariaDB's), and the four that register a classifier also run drivertest.Verify; postgres registers none by design (CON-07), and the dialect kit is what it runs now. The kit's engine half needs a server: in this bench only SQLite's runs (in memory), and the other modules log that they had no DSN. CI's driver lane gives postgres, mysql and mariadb a server each (services), the oracle lane runs the oracle module's kit against its Oracle, and SQL Server's dialect is held to the same kit in the mssql lane through internal/enginesuite. The standalone lane vets those test files out (-tags pinnedquark) until the release train raises the modules' floor to a quark that has the kit.",
			probe:  probeEveryDriverRunsKit,
		},
		{
			id:     "DRV-07",
			family: "drivers",
			title:  "A driver module outside this repository can run the engine conformance suite the in-repo engines run",
			want:   present,
			note:   "Measured from the fixture driver built standalone (GOWORK=off): it imports quarkdriver/drivertest/suite, whose graph is 165 packages against the library's 160 — no container library, no engine driver, no Redis or OpenTelemetry — and its TestEngineSuite passes suite.Run's 43 subtests on its own engine. internal/enginesuite's TestSuiteSQLite runs the same suite.Run, with the same 43 subtests, and so do the five integration lanes (TestSuite<Engine>/EngineSuite). The 43 are the engine-generic subtests of the shared suite, moved out of internal/enginesuite unchanged but for a portable DROP TABLE; the 29 that stay there branch on a built-in engine's name, start a container, need Redis or an OpenTelemetry collector, or touch the tenancy paths — and the dialect kit (DRV-05) is the engine-agnostic form of their dialect half.",
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
