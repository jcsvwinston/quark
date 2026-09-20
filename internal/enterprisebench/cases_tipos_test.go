// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enterprisebench

// controlsTipos is the enterprise TYPE surface: the columns an enterprise
// model asks for (uuid, enum, array, range, inet, jsonb) and the rich types
// Quark actually ships. Every verdict here is what the probe beside it
// measured on SQLite — a column type read back out of the catalog, a value
// followed to the database, or an error returned to the caller.
func controlsTipos() []control {
	return []control{
		{
			id:     "TYP-01",
			family: "tipos",
			title:  "native UUID column type for a UUID-shaped Go value",
			want:   present,
			note: "Closed at A8 S5: a 16-byte array — the shape of google/uuid.UUID — gets UUID on " +
				"PostgreSQL and on SQLite (a declared type name there), CHAR(36) on MySQL/MariaDB, " +
				"VARCHAR2(36) on Oracle and NCHAR(36) on SQL Server, whose UNIQUEIDENTIFIER the driver " +
				"scans in the engine's mixed-endian byte order. The value travels through the type's " +
				"own Valuer/Scanner and round-trips.",
			probe: probeTiposUUIDNative,
		},
		{
			id:     "TYP-02",
			family: "tipos",
			title:  "RegisterTypeMapper: custom SQL type in CREATE TABLE, primary key included",
			want:   present,
			note: "Closed at A8 S5 (QK-29): a mapped key column keeps its PRIMARY KEY — the mapper's " +
				"type is taken verbatim and the suffix appended unless the mapper wrote one. Before, " +
				"the documented UUID-key example shipped tables without a key and the same id landed " +
				"twice.",
			probe: probeTiposTypeMapper,
		},
		{
			id:     "TYP-03",
			family: "tipos",
			title:  "enum constrained by a CHECK declared on the model",
			want:   present,
			note: "Closed at A8 S5: both grammars exist — quark:\"check=<expr>\" verbatim and " +
				"db:\"...,enum=a|b\" as an IN list — and Migrate emits them as named table constraints " +
				"(ck_<table>_<column>) in the shape SQLite's rebuild reads back; PlanMigration carries " +
				"them in the desired schema.",
			probe: probeTiposEnumCheck,
		},
		{
			id:     "TYP-04",
			family: "tipos",
			title:  "native array column for a raw Go slice, and the value serialised into it",
			want:   absent,
			note: "A []string never becomes an array column: it takes the TEXT fallback " +
				"and the first Create fails in database/sql's converter — the same on " +
				"every engine, since the refusal happens above the driver, and that half " +
				"is what the verdict rests on. The published type matrix says raw slices " +
				"and maps are \"serialised as text\"; they are not serialised at all. " +
				"Which column type each dialect would emit is not observable from here " +
				"(this bench only opens SQLite), so the title no longer names one: " +
				"whether PostgreSQL could converge on text[] belongs to " +
				"internal/enginesuite and is not measured.",
			probe: probeTiposNativeArray,
		},
		{
			id:     "TYP-05",
			family: "tipos",
			title:  "quark.Array[T]: typed list column, JSON-backed",
			want:   present,
			note: "Measured on SQLite end to end: the column (TEXT), the raw value in it " +
				"([\"go\",\"orm\",\"sql\"], and [] for the empty case), and the documented " +
				"fold of an empty array back to the zero value. Read as a claim about one " +
				"engine, which is all this bench opens: the five other column types come " +
				"from the same per-dialect switch, and their round-trip is proved in " +
				"internal/enginesuite, not here.",
			probe: probeTiposArrayWrapper,
		},
		{
			id:     "TYP-06",
			family: "tipos",
			title:  "range column type for a range value, and a containment operator",
			want:   absent,
			note: "Both halves are missing and both are measured: a range-shaped value " +
				"(lower, upper) takes the TEXT fallback and cannot be written — Quark " +
				"ships Array, JSON and Nullable and nothing range-shaped, so the value has " +
				"no Valuer to cross database/sql with — and asking for containment is " +
				"refused by the operator allowlist with ErrInvalidQuery before any SQL is " +
				"built. The mapper door is measured, not assumed: with a mapper the column " +
				"really is TSTZRANGE and the write still fails. Which column type " +
				"PostgreSQL would want is not observable here, so the title does not " +
				"claim it.",
			probe: probeTiposRanges,
		},
		{
			id:     "TYP-07",
			family: "tipos",
			title:  "inet column type for an IP value, and a network operator over it",
			want:   absent,
			note: "No IP type and no network operator, which is what an inet column is " +
				"for. A net.IP takes the BLOB fallback and does round-trip — storing an " +
				"address is not the problem; storing it AS an address is. The escape " +
				"hatch buys the declaration: with a mapper the column really is INET and " +
				"the value goes in, and containment (>>) over that same column is still " +
				"refused by the operator allowlist before any SQL is built, so there is no " +
				"network semantics to reach. cidr and macaddr are not measured and the " +
				"title no longer claims them. The one INET in the tree is a mapper defined " +
				"inside a test, which only asserts that Migrate returns no error.",
			probe: probeTiposInet,
		},
		{
			id:     "TYP-08",
			family: "tipos",
			title:  "JSON column: typed round-trip and dotted-path filtering",
			want:   partial,
			note: "What works is measured: JSON[T] in and out, and a filter that binds a " +
				"dotted path into the dialect's JSON function. What is missing is what " +
				"JSONB is asked for — a path into an array is refused by the path grammar " +
				"(ErrInvalidJSONPath) and containment is refused by the operator allowlist, " +
				"so @>, ?, jsonb_path_query and GIN indexes have no surface at all. That " +
				"the PostgreSQL column really is JSONB needs a live engine.",
			probe: probeTiposJSON,
		},
		{
			id:     "TYP-09",
			family: "tipos",
			title:  "quark.Nullable[T], including Nullable[Array[T]] and Nullable[JSON[T]]",
			want:   present,
			note: "Measured on SQLite, NULL against the zero value, over the two " +
				"compositions the godoc recommends for exactly that and that no test in " +
				"the repository exercises. The binding trap those compositions were " +
				"written for is SQL Server's, and it needs a live engine to see.",
			probe: probeTiposNullable,
		},
		{
			id:     "TYP-10",
			family: "tipos",
			title:  "built-in rich types: time.Duration and []byte",
			want:   present,
			note: "Measured on SQLite end to end: the column time.Duration takes " +
				"(BIGINT, from the shipped mapper), the column []byte takes (BLOB), and " +
				"the value of each one back out. The db tag's precision/scale used to be " +
				"the third item here and is now TYP-11: a shipped mapper that stops " +
				"mapping and a sizing hint that turns into a type are different defects, " +
				"and sharing one verdict meant every way the hint could break still read " +
				"as this control's recorded `partial`. What the other engines emit for " +
				"these two types comes from the same per-dialect switch and is proved in " +
				"internal/enginesuite, not here.",
			probe: probeTiposRichBuiltins,
		},
		{
			id:     "TYP-11",
			family: "tipos",
			title:  "the db tag's precision/scale sizes a decimal without retyping other fields",
			want:   present,
			note: "Closed at A8 S7 (QK-28): the hint refines float columns only — DECIMAL(p,s), NUMBER(p,s) " +
				"on Oracle — and on any other kind it is ignored with a tag warning instead of replacing " +
				"the base type. Diff reads numeric(p,s) and NUMBER(p,s) as the same family, so the plan " +
				"converges on PostgreSQL and Oracle; proven per engine in internal/enginesuite.",
			probe: probeTiposPrecisionScale,
		},
	}
}
