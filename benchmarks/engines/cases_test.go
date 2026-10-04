// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package engines

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
// The recorded ratios come from the reference run named on the published page.
// TestEngineBenchCatalogue checks that each recorded verdict is one its
// recorded ratios allow.
func controls() []control {
	return []control{
		{
			id: "PG-01", engine: "postgres", op: "InsertOne",
			title:   "Insert one row and read its generated id back",
			targets: []target{{armSQL, 1.12}, {armPgx, 1.16}},
			want:    partial,
			allocs:  memory{86, 4425},
			note: "Both ratios sit inside the band around the limit, so a single run cannot tell met from missed; the record is the verdict of the reference medians. " +
				"Quark allocates 86 times and 4.4 KB per insert, against 21 times and 1.1 KB for database/sql and 13 times and 0.6 KB for pgx: the statement is built from the struct, " +
				"validated and stamped on every call. On loopback that is about the size of the gap to the limit.",
		},
		{
			id: "PG-02", engine: "postgres", op: "FindByPK",
			title:   "Select one row by primary key",
			targets: []target{{armSQL, 1.10}, {armPgx, 1.13}},
			want:    present,
			allocs:  memory{74, 4830},
			note: "On the threshold against both baselines: present by the reference medians, inside the band on every run measured. " +
				"Find goes through the list path — the SELECT is assembled by the query builder (8 % of the client's CPU in a profile) and the row goes through the generic scanner — " +
				"and allocates 74 times and 4.8 KB per row, against 28 times for database/sql and 10 for pgx.",
		},
		{
			id: "PG-03", engine: "postgres", op: "List100",
			title:   "Select 100 rows with a WHERE, an ORDER BY and a LIMIT",
			targets: []target{{armSQL, 1.30}},
			want:    absent,
			allocs:  memory{576, 37000},
			note: "About 30 % over database/sql. The largest single item: List serializes the whole result to JSON even when no cache is configured, and discards it — " +
				"15 % of the client's CPU and about 12 KB of the 37 KB allocated per call, in a CPU and a memory profile of this operation. " +
				"Building scan destinations through reflection for every row is another 11 %.",
		},
		{
			id: "PG-04", engine: "postgres", op: "Preload100",
			title:   "Select 100 parents and their 500 children",
			targets: []target{{armSQL, 1.95}},
			want:    absent,
			allocs:  memory{4757, 208500},
			note: "About twice database/sql, and most of it is the shape of the children's query. The informational arm runs the hand-written program with the query Quark sends — " +
				"SELECT * and an IN list of 100 placeholders — and that alone takes 560 µs where = ANY($1) with one array parameter takes 320 µs. " +
				"Quark is about 85 µs over that arm: mapping the children onto their parents through reflection, which is half the client's CPU in a profile.",
		},
		{
			id: "PG-05", engine: "postgres", op: "InsertBatch1000",
			title:   "Insert 1000 rows in one statement with their ids back",
			targets: []target{{armSQL, 1.32}},
			want:    absent,
			allocs:  memory{27880, 1906000},
			note: "About 30 % over database/sql, sending the same single statement. In a CPU profile: each of the 4000 placeholders is formatted with fmt.Sprintf (14 % of the client's CPU), " +
				"and the validator walks every struct of the batch although the model declares no validation rule (9 %). " +
				"Quark allocates 27 900 times per batch against 9 000 for database/sql.",
		},
		{
			id: "MY-01", engine: "mysql", op: "FindByPK",
			title:   "Select one row by primary key",
			targets: []target{{armSQL, 1.20}, {armSQLStmt, 2.50}},
			want:    absent,
			allocs:  memory{69, 4470},
			note: "Near the limit against a program that sends the query per call, and two and a half times one that prepares the statement once. " +
				"With arguments and without interpolateParams, the driver prepares, executes and closes a server-side statement on every query; Quark reuses none, so every Find pays all three. " +
				"With interpolateParams=true in the DSN the driver sends one text query instead, and Quark's Find drops from about 93 µs to 59 µs; the reused statement takes 39 µs.",
		},
	}
}
