// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package engines

// referenceCPUs are the CPU models of the reference runs the recorded ratios
// are the medians of, as /proc/cpuinfo names them. The time checks are
// asserted only on these (see referenceEnv): on any other model a run reports
// its verdicts and ratios and asserts its allocations.
var referenceCPUs = []string{
	"AMD EPYC 7763 64-Core Processor",
	"AMD EPYC 9V45 96-Core Processor",
	"AMD EPYC 9V74 80-Core Processor",
	"Intel(R) Xeon(R) 6973P-C",
	"Intel(R) Xeon(R) Platinum 8370C CPU @ 2.80GHz",
}

// controls is the catalogue: one control per operation, the baselines it is
// judged against, and what the bench RECORDS — the verdict and, per baseline,
// the ratio of the reference run.
//
// The single-row operations on PostgreSQL are judged against both baselines:
// a program that reads one row through pgx's own pool pays no more than one
// that goes through database/sql, so there is no reason quark should be
// further from either. The multi-row operations are judged against
// database/sql only: pgx's native pool scans rows faster than database/sql
// can, before any ORM does anything (a 100-row list costs 10–25 % more through
// database/sql than through pgx), so 15 % from pgx would ask quark to beat
// the interface it is built on.
//
// MySQL's FindByPK controls are judged against both ways a hand-written
// program can send the query: per call (the driver prepares, executes and
// closes) and with the statement prepared once and reused — MY-01 with
// Quark's defaults, MY-02 with WithStatementCache. MY-03, the batch, is judged
// against the same multi-row INSERT written by hand. The proposed target names
// PostgreSQL only; the same 15 % is applied here so that the statement work
// has a number to move, and it is as open as the rest.
//
// PG-06, MY-02 and MY-03 (A12 Q3) record the medians of five runs on the
// reference machine on 2026-10-06, which drew three of the five models in
// referenceCPUs: AMD EPYC 7763 twice, AMD EPYC 9V74 twice, Intel Xeon
// Platinum 8370C once.
//
// MY-01's ratio to the reused statement is also recorded per CPU model
// (QK-56). Its median across the ten runs of 2026-10-05, 2.71, sits on the
// EPYC 7763, which drew five of them; in the 36 runs on the five models that
// followed, up to 2026-10-06, the EPYC 9V74 measured 3.12–3.29 and failed the
// drift check in three of its nine (three of twelve before the record), while
// no run on the EPYC 7763 moved more than 5 % from 2.71. The ratio is
// Quark's over the per-call program (1.37–1.60 across those runs) times the
// per-call program's over the reused statement (1.85–2.10), which has no
// Quark code in it, and both move with the model: the medians per model run
// from 2.68 to 3.21, 20 % apart, which driftFloor cannot absorb around one
// figure. The per-model ratios are the medians of those 36 runs, per model.
//
// The recorded ratios are the medians of ten runs on the reference machine,
// the CI runner (see referenceEnv), on 2026-10-05: GitHub drew five CPU models
// for them, listed in referenceCPUs and named on the published page. The
// allocations are machine-independent and were the same on the runner and on
// a laptop; PG-01's were retaken on 2026-10-06 (A11 Q7), on a laptop that
// measured the old figures on the code before it. TestEngineBenchCatalogue
// checks that each recorded verdict is one its recorded ratios allow.
func controls() []control {
	return []control{
		{
			id: "PG-01", engine: "postgres", op: "InsertOne",
			title:   "Insert one row and read its generated id back",
			targets: []target{{base: armSQL, recorded: 1.26}, {base: armPgx, recorded: 1.29}},
			want:    absent,
			allocs:  memory{79, 3706},
			note: "Over the limit against both baselines on the reference runners (1.20–1.36 against database/sql and 1.22–1.41 against pgx across ten runs). " +
				"Quark allocates 79 times and 3.7 KB per insert, against 21 times and 1.1 KB for database/sql and 13 times and 0.6 KB for pgx: " +
				"the INSERT is built from the struct on every call, 9 % of the client's CPU in a profile. " +
				"Validating a model that declares no rule and reading the clock for one with no timestamp columns no longer happen (A12 Q2); they were five allocations, and no move a run can see in the ratio. " +
				"Since A11 Q7 the single-row read of the generated id goes through the execution seam, which calls the engine directly when no middleware is registered: the per-call func value that captured the query is gone, and with it the heap copy of the query Create makes for the insert — two allocations and 0.65 KB (81 and 4.4 KB before, measured on the same machine). " +
				"Quark's share costs about 30 µs per insert on the EPYC runner and a few µs on a laptop, where the same code sits on the threshold (1.14 and 1.16).",
		},
		{
			id: "PG-02", engine: "postgres", op: "FindByPK",
			title:   "Select one row by primary key",
			targets: []target{{base: armSQL, recorded: 1.24}, {base: armPgx, recorded: 1.29}},
			want:    absent,
			allocs:  memory{72, 4755},
			note: "Over the limit on the reference runners: 1.10–1.25 against database/sql, inside the band, and 1.12–1.30 against pgx; on a laptop it sits on the threshold (1.09 and 1.13). " +
				"Find goes through the list path — the SELECT is assembled by the query builder (8 % of the client's CPU in a profile) and the row goes through the generic scanner — " +
				"and allocates 72 times and 4.8 KB per row, against 29 times for database/sql and 10 for pgx.",
		},
		{
			id: "PG-03", engine: "postgres", op: "List100",
			title:   "Select 100 rows with a WHERE, an ORDER BY and a LIMIT",
			targets: []target{{base: armSQL, recorded: 1.17}},
			want:    absent,
			allocs:  memory{573, 25780},
			note: "17 % over database/sql on the reference runners (1.14–1.29 across ten runs), inside the band, so the verdict is not asserted; 15 % on a laptop. " +
				"It was 35 % until A12 Q2: List serialized the whole result to JSON when no cache was configured and discarded it (QK-34), and decided for every row and column whether the field needed a scan target of its own. " +
				"It now allocates 25.8 KB per call instead of 37 KB. What remains, in a CPU profile: pointing the scan targets at each row's fields through reflection (about 8 % of the client's CPU besides the driver's own Scan) and assembling the SELECT (5 %).",
		},
		{
			id: "PG-04", engine: "postgres", op: "Preload100",
			title:   "Select 100 parents and their 500 children",
			targets: []target{{base: armSQL, recorded: 1.25}},
			want:    absent,
			allocs:  memory{2869, 156240},
			note: "A quarter over database/sql (1.22–1.31 across ten runs); it was twice. Since A12 Q2 the children are selected with = ANY($1) and one array parameter instead of an IN list of 100 placeholders, " +
				"and assembled onto their parents with one growth per parent instead of a reflect.Append per row (QK-35). " +
				"The informational arm keeps the old shape: the hand-written program sending the IN list took 1.25 ms on the EPYC runner where = ANY($1) took 0.79 ms. " +
				"What remains: the parent list (PG-03), and mapping the 500 children — each row's key read through reflection and looked up, the row copied — about 15 % of the client's CPU besides the driver's Next and Scan.",
		},
		{
			id: "PG-05", engine: "postgres", op: "InsertBatch1000",
			title:   "Insert 1000 rows in one statement with their ids back",
			targets: []target{{base: armSQL, recorded: 1.09}},
			want:    present,
			allocs:  memory{13040, 1401900},
			note: "Within the limit in every run (1.08–1.11 across ten), and inside the band, so the verdict is not asserted. It was a third over database/sql until A12 Q2 (QK-36): " +
				"each of the 4000 placeholders was formatted with fmt.Sprintf, the validator walked every struct of the batch although the model declares no rule, and each row's placeholders went through a slice and a join. " +
				"Quark allocates 13 000 times per batch against 9 000 for database/sql; reading each field through reflection to bind it is 8 % of the client's CPU in a profile.",
		},
		{
			id: "PG-06", engine: "postgres", op: "InsertBatch10000",
			title:   "Insert 10 000 rows in one statement with their ids back",
			targets: []target{{base: armSQL, recorded: 1.09}},
			want:    present,
			allocs:  memory{129900, 17667762},
			note: "The same statement as the baseline — one INSERT of 10 000 rows, 40 000 parameters, RETURNING id — and the distance PG-05 has: 1.09 (1.08–1.10 across five runs on three CPU models), inside the band, so the verdict is not asserted (A12 Q3). " +
				"The informational arm is pgx's CopyFrom, COPY in binary format, which reads no ids back: 25.9 ms against 66.8 ms for Quark and 61.4 ms for database/sql on the EPYC 7763 runner, so 2.6 times faster than any form that hands each entity its key. " +
				"Quark does not use COPY: it cannot return the generated keys CreateBatch promises, and for rows whose keys the caller supplies it is not the same operation — it ignores rules, refuses tables under row-level security, and needs a binary encoding for every column type. An opt-in bulk load is what would close that distance, and it is not built.",
		},
		{
			id: "MY-01", engine: "mysql", op: "FindByPK",
			title: "Select one row by primary key",
			targets: []target{
				{base: armSQL, recorded: 1.40},
				{base: armSQLStmt, recorded: 2.71, perCPU: map[string]float64{
					"AMD EPYC 7763 64-Core Processor":               2.68,
					"AMD EPYC 9V45 96-Core Processor":               2.89,
					"AMD EPYC 9V74 80-Core Processor":               3.21,
					"Intel(R) Xeon(R) 6973P-C":                      2.75,
					"Intel(R) Xeon(R) Platinum 8370C CPU @ 2.80GHz": 3.10,
				}},
			},
			want:   absent,
			allocs: memory{67, 4390},
			note: "1.4 times a program that sends the query per call, and 2.7 to 3.2 times one that prepares the statement once, depending on the CPU model the runner draws. " +
				"With arguments and without interpolateParams, the driver prepares, executes and closes a server-side statement on every query; Quark reuses none, so every Find pays all three. " +
				"Across 36 runs of the reference runner on its five CPU models, on 2026-10-05 and 06, the ratio to the per-call program, which pays the same three, had medians from 1.40 (AMD EPYC 7763 and 9V45) to 1.57 (EPYC 9V74), 12 % apart. " +
				"The ratio to the reused statement multiplies that by the per-call program's own time over the reused statement's, which has no Quark code in it and moves with the model too (1.88–2.05 in medians): " +
				"its medians run from 2.68 on the EPYC 7763 to 3.21 on the 9V74 (2.60–3.29 across the 36 runs), 20 % apart, more than the drift tolerance absorbs around one figure. " +
				"So that ratio is recorded per CPU model, and a run is checked against the ratio of the model it drew. " +
				"With interpolateParams=true in the DSN the driver sends one text query instead: on the EPYC runner Quark's Find drops from 268 µs to 157 µs, against 102 µs for the reused statement.",
		},
		{
			id: "MY-02", engine: "mysql", op: "FindByPKStmtCache",
			title:   "Select one row by primary key, with WithStatementCache",
			targets: []target{{base: armSQLStmt, recorded: 1.65}, {base: armSQL, recorded: 0.81}},
			want:    partial,
			allocs:  memory{61, 4242},
			note: "MY-01's operation with the option on (A12 Q3): Quark prepares each statement once per connection and reuses it, as the reused-statement baseline does. " +
				"In the same rounds, Find took 46–48 % less time with the cache than without it on every reference runner (145 µs against 270 µs on the EPYC 7763); 0.81 times the program that sends the query per call (0.74–0.83 across five runs) and 1.65 times the one that reuses its statement (1.44–1.73). " +
				"What remains over that baseline is MY-01's CPU share — the SELECT assembled by the query builder and the row through the generic scanner — not the protocol. The option is off by default; with it off, Find is MY-01.",
		},
		{
			id: "MY-03", engine: "mysql", op: "InsertBatch1000",
			title:   "Insert 1000 rows with their ids back (innodb_autoinc_lock_mode=1)",
			targets: []target{{base: armSQL, recorded: 1.10}},
			want:    present,
			allocs:  memory{11954, 857862},
			note: "Measured on a server started with innodb_autoinc_lock_mode=1, the setting under which MySQL documents that a multi-row INSERT's keys are consecutive (A12 Q3): CreateBatch sends one INSERT for the chunk and computes the keys, as the baseline does. 1.10 (1.04–1.12 across five runs), inside the band, so the verdict is not asserted. " +
				"Quark adds three round trips the baseline does not take — BEGIN, the read of auto_increment_increment on the INSERT's connection, COMMIT — so that a failed chunk can be undone and run row by row. " +
				"Under MySQL 8's default, 2, the manual does not promise consecutive keys and CreateBatch keeps one INSERT per row: the informational arm, 237–245 ms on the EPYC 7763 runner against 9.6–9.7 ms for Quark.",
		},
	}
}
