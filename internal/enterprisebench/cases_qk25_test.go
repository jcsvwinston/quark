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
			want:   absent,
			note: "No public surface reaches it. The probe knocks on every door that " +
				"takes an operator — Where, WhereNot, Having, HavingAggregate and the " +
				"expression form `Cmp(Col, \"LIKE ESCAPE\", …)` — and the guard's " +
				"operator whitelist refuses all five; an ordinary LIKE is emitted with " +
				"no tail. The gap is structural — a condition has nowhere to carry a " +
				"tail — not a missing entry in that whitelist.",
			probe: probeQk25BuilderEmitsEscape,
		},
		{
			id:     "LIKE-02",
			family: "qk25",
			title:  "Quark takes the value of an application-composed LIKE as literal text",
			want:   absent,
			note: "The value reaches the bind untouched: a contains-search where the " +
				"user typed `%` answers every row (3 of 3), from a statement with no " +
				"ESCAPE tail. The title says what this surface can prove and no more — " +
				"`Where(col, \"LIKE\", pattern)` hands Quark one opaque string, so no " +
				"implementation can escape the user's `%` while leaving the two the " +
				"application wrapped around it as wildcards, and the probe asks instead " +
				"for the whole value as text: no rows, from a statement that declares " +
				"the escape character. Taking it as text WITHOUT declaring that " +
				"character turns over-answering into under-answering (see LIKE-08), so " +
				"the rows and the clause are read together. A refusal is not it either " +
				"— it leaves a literal `%` unsearchable — so it measures `partial`.",
			probe: probeQk25LikeValueIsLiteralText,
		},
		{
			id:     "LIKE-03",
			family: "qk25",
			title:  "What Quark emits for a LIKE varies with the engine's default escape rules",
			want:   absent,
			note: "Measured as the variation itself, not as the ESCAPE keyword: the six " +
				"statements are normalised (placeholder spelling and identifier quoting " +
				"removed, since those always differ and mean nothing here) and compared " +
				"against each other along with the six binds. One shape comes out for " +
				"six engines — SQLite, SQL Server and Oracle, which read a backslash in " +
				"a pattern as an ordinary character, get exactly what MySQL gets, value " +
				"included. The only table of which engine escapes by default lives in " +
				"orbit's quarkdatasource, unexported and in another repository.",
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
			title:  "The generated typed `Like` accessor refuses or neutralises a user's wildcard",
			want:   absent,
			note: "The same defect as LIKE-02 on the surface `quark gen` hands an " +
				"application: `Like(\"%\"+typed+\"%\")` with `%` typed answers 3 of 3, " +
				"silently — no error, no escape tail. An application that never writes a " +
				"string operator still meets it. The CLI is a module of its own, so the " +
				"probe cannot import what it emits; it reads the generator's golden " +
				"output instead and fails outright if a string column stopped being a " +
				"`quark.NewTypedStringColumn`, rather than going on measuring an " +
				"accessor no application is handed.",
			probe: probeQk25TypedLikeGuardsValue,
		},
		{
			id:     "LIKE-06",
			family: "qk25",
			title:  "Quark's SQL guard inspects the LIKE value, not only the operator",
			want:   absent,
			note: "On the same client the guard refuses an operator outside its whitelist " +
				"and lets a contains-search whose text is `%` through untouched (3 of 3): " +
				"it is operator-shaped and value-blind. The operator half is asserted, " +
				"not logged — it is what `not only` in the title rests on. The refusal " +
				"that does exist — reject a wildcard value where the engine has no " +
				"default escape character — is in orbit's quarkdatasource, so it " +
				"protects that datasource and nothing else.",
			probe: probeQk25GuardInspectsLikeValue,
		},
		{
			id:     "LIKE-07",
			family: "qk25",
			title:  "The `LIKE ? ESCAPE '<c>'` form is proven across the engines Quark ships on",
			want:   absent,
			note: "Zero of the five engines the CI matrix declares. The proof the form " +
				"needs is per engine, because the spelling does not carry: SQLite " +
				"accepts `ESCAPE '\\'` and rejects `ESCAPE '\\\\'` as more than one " +
				"character, while MySQL and MariaDB need the doubled one because a lone " +
				"backslash leaves the string literal unterminated. Copying the nucleus " +
				"form (`LOWER(x) LIKE ? ESCAPE '\\'`) into Quark would reproduce that " +
				"MySQL syntax error. internal/enginesuite is where that proof belongs " +
				"and not one of its tests asserts the form, so the probe reads the suite " +
				"sources for it; the SQLite half it can run is the probe's own ground, " +
				"asserted rather than scored.",
			probe: probeQk25EscapeLiteralPortability,
		},
		{
			id:     "LIKE-08",
			family: "qk25",
			title:  "A literal `%` in a value can be matched exactly through the builder",
			want:   absent,
			note: "Both builder answers fall on either side of the right one: with the " +
				"value untouched the search returns 3 of 3, with the value escaped by " +
				"hand it returns 0, and the correct answer is 1. Nothing in Quark's tests " +
				"or documentation fixes this contract in either direction.",
			probe: probeQk25LiteralPercentMatch,
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
			want:   partial,
			note: "One of the five engines the matrix declares. MariaDB's suite skips " +
				"after a failed Ping in three places, so its lane can report green with " +
				"the shared suite never run; the other four skip only on an unset DSN, " +
				"which the integration tag makes impossible. The sweep reads every test " +
				"in the suite module, not the files whose name ends in _suite_test.go, " +
				"and charges each hole to the engine its own skip message names — the " +
				"two skips that fire when Redis does not answer belong to a cache " +
				"control, not to an engine lane.",
			probe: probeQk25EngineLaneCannotSkip,
		},
	}
}
