// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enterprisebench

// controlsQk25 is the family around QK-25 — the wildcard a user types into a
// LIKE — plus the three controls about what the CI actually exercises.
//
// Every verdict here is what the probe next to it measured on SQLite through
// Quark's public API. Where a capability exists in the suite but outside the
// library (orbit's quarkdatasource, nucleus's crud), it is recorded as absent
// for Quark and the note says where it lives: an application that depends on
// Quark cannot reach another repository's unexported helper.
func controlsQk25() []control {
	return []control{
		{
			id:     "LIKE-01",
			family: "qk25",
			title:  "The query builder can emit a `LIKE … ESCAPE '<c>'` tail",
			want:   present,
			note: "Through the escaped surfaces added in A8 S2 — WhereLike / WhereNotLike, " +
				"the typed LikeEscaped, the AST Like — each of which emits the tail and " +
				"answers the one row with a literal percent sign. The plain " +
				"Where(col, \"LIKE\", pattern) is unchanged on purpose: it hands the engine " +
				"an opaque pattern under the engine's default escape rules, and changing " +
				"what a backslash means there on three engines is A12 material (QK-32).",
			probe: probeQk25BuilderEmitsEscape,
		},
		{
			id:     "LIKE-02",
			family: "qk25",
			title:  "A user's text reaches a LIKE as literal text, through a surface that receives the text apart from the pattern (WhereContains)",
			want:   present,
			note: "Retitled at S2. The S0 title measured Where(col, \"LIKE\", pattern) taking " +
				"its whole value as text, and its `present` was reachable only by changing " +
				"what that published form does — a search for \"%%%\" answering no rows. " +
				"The capability an application needs is a surface that receives the user's " +
				"TEXT apart from the wildcards the application wraps around it, so that a " +
				"typed `%` answers the one row that holds one: that surface exists now, and " +
				"this is what the control measures. The plain form keeps answering every row " +
				"for that pattern, asserted here as the ground.",
			probe: probeQk25LikeValueIsLiteralText,
		},
		{
			id:     "LIKE-03",
			family: "qk25",
			title:  "What Quark emits for an escaped LIKE varies with the engine's string-literal rules: the ESCAPE character is doubled where a lone backslash would end the literal",
			want:   present,
			note: "Retitled at S2. With the escape character DECLARED on every statement the " +
				"engines' default-escape rules — the boundary the S0 title measured — stop " +
				"mattering; what still differs is how each engine's parser reads the " +
				"literal that names the character: MySQL and MariaDB need `'\\\\'` and SQLite " +
				"rejects it as more than one character. The probe normalises placeholders " +
				"and quoting away and expects exactly two shapes along that boundary, with " +
				"the six binds identical for plain text — and, for text with a `[`, exactly " +
				"SQL Server binding differently: the bracket is a wildcard there alone, " +
				"and Oracle refuses an escape in front of it (ORA-01424).",
			probe: probeQk25DialectAwareLike,
		},
		{
			id:     "LIKE-04",
			family: "qk25",
			title:  "Client.Raw reaches an engine-native `LIKE … ESCAPE`, outside the builder",
			want:   present,
			probe:  probeQk25RawEscapeHatch,
		},
		{
			id:     "LIKE-05",
			family: "qk25",
			title:  "The generated typed string column offers a search accessor that neutralises a user's wildcard (Contains), beside the pattern form (Like)",
			want:   present,
			note: "Retitled at S2: `Like` keeps the pattern meaning it was published with, " +
				"and `Contains` / `StartsWith` / `EndsWith` / `LikeEscaped` are the accessors " +
				"a search box calls. The probe still reads the generator's golden output " +
				"and fails outright if a string column stopped being a " +
				"`quark.NewTypedStringColumn`, so it measures the surface `quark gen` hands " +
				"an application and not one it built by hand.",
			probe: probeQk25TypedLikeGuardsValue,
		},
		{
			id:     "LIKE-06",
			family: "qk25",
			title:  "Quark's SQL guard inspects an escaped LIKE pattern, not only the operator: a dangling escape character is refused before the engine sees it",
			want:   present,
			note: "Retitled at S2. The operator half is still asserted (ILIKE refused) and an " +
				"escaped LIKE with a sound pattern still runs, so the refusal of `abc\\` with " +
				"no statement recorded is the guard reading the VALUE — the one thing the " +
				"S0 guard could not do.",
			probe: probeQk25GuardInspectsLikeValue,
		},
		{
			id:     "LIKE-07",
			family: "qk25",
			title:  "The `LIKE ? ESCAPE '<c>'` form is proven across the engines Quark ships on",
			want:   present,
			probe:  probeQk25EscapeLiteralPortability,
		},
		{
			id:     "LIKE-08",
			family: "qk25",
			title:  "A literal `%` in a value can be matched exactly through the builder",
			want:   present,
			probe:  probeQk25LiteralPercentMatch,
		},
		{
			id:     "LIKE-09",
			family: "qk25",
			title: "CI declares a lane per real engine and an all-engines acceptance, " +
				"and lets none of them fail soft",
			want:  present,
			probe: probeQk25CIDeclaresEngineLanes,
		},
		{
			id:     "LIKE-10",
			family: "qk25",
			title:  "One aggregating check covers every lane the workflow declares",
			want:   present,
			probe:  probeQk25CIRequiredGateCoversLanes,
		},
		{
			id:     "LIKE-11",
			family: "qk25",
			title:  "An engine lane fails, rather than skips, when its engine does not answer",
			want:   present,
			probe:  probeQk25EngineLaneCannotSkip,
		},
	}
}
