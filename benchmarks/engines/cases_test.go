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
// MySQL's one control is judged against both ways a hand-written program can
// send the query: per call (the driver prepares, executes and closes) and
// with the statement prepared once and reused. The proposed target names
// PostgreSQL only; the same 15 % is applied here so that the statement work
// has a number to move, and it is as open as the rest.
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
			targets: []target{{armSQL, 1.26}, {armPgx, 1.29}},
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
			targets: []target{{armSQL, 1.24}, {armPgx, 1.29}},
			want:    absent,
			allocs:  memory{72, 4755},
			note: "Over the limit on the reference runners: 1.10–1.25 against database/sql, inside the band, and 1.12–1.30 against pgx; on a laptop it sits on the threshold (1.09 and 1.13). " +
				"Find goes through the list path — the SELECT is assembled by the query builder (8 % of the client's CPU in a profile) and the row goes through the generic scanner — " +
				"and allocates 72 times and 4.8 KB per row, against 29 times for database/sql and 10 for pgx.",
		},
		{
			id: "PG-03", engine: "postgres", op: "List100",
			title:   "Select 100 rows with a WHERE, an ORDER BY and a LIMIT",
			targets: []target{{armSQL, 1.17}},
			want:    absent,
			allocs:  memory{573, 25780},
			note: "17 % over database/sql on the reference runners (1.14–1.29 across ten runs), inside the band, so the verdict is not asserted; 15 % on a laptop. " +
				"It was 35 % until A12 Q2: List serialized the whole result to JSON when no cache was configured and discarded it (QK-34), and decided for every row and column whether the field needed a scan target of its own. " +
				"It now allocates 25.8 KB per call instead of 37 KB. What remains, in a CPU profile: pointing the scan targets at each row's fields through reflection (about 8 % of the client's CPU besides the driver's own Scan) and assembling the SELECT (5 %).",
		},
		{
			id: "PG-04", engine: "postgres", op: "Preload100",
			title:   "Select 100 parents and their 500 children",
			targets: []target{{armSQL, 1.25}},
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
			targets: []target{{armSQL, 1.09}},
			want:    present,
			allocs:  memory{13040, 1401900},
			note: "Within the limit in every run (1.08–1.11 across ten), and inside the band, so the verdict is not asserted. It was a third over database/sql until A12 Q2 (QK-36): " +
				"each of the 4000 placeholders was formatted with fmt.Sprintf, the validator walked every struct of the batch although the model declares no rule, and each row's placeholders went through a slice and a join. " +
				"Quark allocates 13 000 times per batch against 9 000 for database/sql; reading each field through reflection to bind it is 8 % of the client's CPU in a profile.",
		},
		{
			id: "MY-01", engine: "mysql", op: "FindByPK",
			title:   "Select one row by primary key",
			targets: []target{{armSQL, 1.40}, {armSQLStmt, 2.71}},
			want:    absent,
			allocs:  memory{67, 4390},
			note: "1.4 times a program that sends the query per call, and 2.7 times one that prepares the statement once. " +
				"With arguments and without interpolateParams, the driver prepares, executes and closes a server-side statement on every query; Quark reuses none, so every Find pays all three. " +
				"With interpolateParams=true in the DSN the driver sends one text query instead: on the EPYC runner Quark's Find drops from 268 µs to 157 µs, against 102 µs for the reused statement.",
		},
	}
}
