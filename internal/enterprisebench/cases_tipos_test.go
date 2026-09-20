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
			title:  "a raw Go slice is stored and round-trips: a native array column on PostgreSQL, a JSON-backed column elsewhere",
			want:   present,
			note: "Closed at A8 S6. A []string, []int64 or map[string]any is bound and scanned by " +
				"Quark: on PostgreSQL a slice of a scalar kind gets TEXT[] / BIGINT[] / … and travels " +
				"as the array literal; everywhere else the column is the dialect's JSON type and the " +
				"value JSON text. This bench opens SQLite and measures the round trip and the JSON in " +
				"the column; the native array and its introspection are proven in internal/enginesuite.",
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
			title:  "quark.Range[T]: a range column (TSTZRANGE / INT8RANGE on PostgreSQL, JSON elsewhere) that round-trips, and a containment operator the builder knows and gates by engine",
			want:   present,
			note: "Closed at A8 S6. Range[T] carries lower, upper and bounds; PostgreSQL stores it in its " +
				"range type and the value travels as the range literal, every other engine as JSON. " +
				"`@>` / `<@` are known to the builder: accepted on PostgreSQL, refused with " +
				"ErrUnsupportedFeature naming the engine elsewhere, with no SQL sent. A plain struct " +
				"that only looks like a range is still refused by database/sql.",
			probe: probeTiposRanges,
		},
		{
			id:     "TYP-07",
			family: "tipos",
			title:  "net.IP: an inet column on PostgreSQL, the address as text elsewhere, and the network operators the builder knows and gates by engine",
			want:   present,
			note: "Closed at A8 S6. net.IP is bound as its textual form and scanned back from it (or " +
				"from the raw bytes a pre-S6 row may hold): INET on PostgreSQL, a text column elsewhere. " +
				"`>>`, `<<`, `>>=`, `<<=` and `&&` are known to the builder and refused by engine outside " +
				"PostgreSQL. cidr and macaddr are not measured.",
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
				"(ErrInvalidJSONPath), and containment (@>) is known to the builder since " +
				"A8 S6 but refused by engine on this bench's SQLite; ?, jsonb_path_query and " +
				"GIN indexes have no surface. That the PostgreSQL column really is JSONB " +
				"needs a live engine.",
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
