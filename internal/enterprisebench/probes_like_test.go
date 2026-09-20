// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enterprisebench

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
)

// Probes for the qk25 family: what happens to a wildcard a user typed when it
// reaches a LIKE, and what the CI actually exercises.
//
// Every probe on the LIKE half runs a real statement against SQLite and then
// decides on one of two readings:
//
//   - the CLAUSE, read back through e.rec — "does the builder write an ESCAPE
//     tail at all" cannot be answered by counting rows, because the statement
//     with the tail and the statement without it return the same rows for a
//     pattern that holds no wildcard;
//   - the ROWS, when the control is about the answer being wrong — over- or
//     under-answering is exactly a row count.
//
// The fixture is the same three rows everywhere: two ordinary names and one
// that contains a literal percent sign. That single row is what makes the two
// failure modes visible as different numbers — a search for a literal "%" must
// return it and only it, so 3 is over-answering and 0 is under-answering.
//
// EVERY VERDICT HERE HAS ONE ROAD IN. A probe whose `partial` can be reached
// both by a half-built capability and by a broken one goes green through a
// regression, which is the one thing a bench must not do. So each branch below
// is the conjunction of the facts its title claims, every other split of those
// facts lands on a DIFFERENT verdict, and where the probe's own ground moves
// (its fixture engine stops behaving, the sources it reads are gone) it fails
// the test instead of reporting an absence it did not measure.

// qk25Row is this family's fixture model. Its own table name keeps the rows
// away from whatever other families create in a shared in-memory database.
type qk25Row struct {
	ID   int64  `db:"id"`
	Name string `db:"name"`
}

func (qk25Row) TableName() string { return "qk25_rows" }

// qk25WildcardRows is the count of fixture rows whose name contains a literal
// percent sign: the right answer to a search for the literal character.
const qk25WildcardRows = 1

// qk25AllRows is the fixture size: the answer a query gives when the user's
// "%" was taken as a wildcard instead of as text.
const qk25AllRows = 3

// qk25ContainsLiteralPercent is the pattern an application builds for a
// "contains" search when the text the user typed is a single percent sign.
//
// Two answers separate the states this pattern can measure, and a third one is
// NOT among them. `Where(col, "LIKE", pattern)` hands Quark a single opaque
// string: the two `%` the application wrapped around the text and the one the
// user typed are the same character in the same value, so nothing downstream
// can escape the middle one and leave the other two as wildcards. 3 rows means
// the value reached the bind untouched; 0 means the whole value was taken as
// text — neutralised when the statement also declares an escape character
// (LIKE-02), merely under-answering when it does not (LIKE-08). 1 row, the
// "100% off" row alone, is the answer to a search for a literal percent sign,
// and it is reachable only from a surface that receives the user's text apart
// from the pattern the application built around it. This constant is not that
// surface, so no branch below may ask it for that number.
const qk25ContainsLiteralPercent = `%%%`

// qk25Fixture creates the family's table on a client of its own.
//
// It writes through Client.Raw rather than Client.Exec because Exec is behind
// WithLimits(AllowRawQueries) and the bench's clients do not enable it: the
// fixture must not need the very gate that control LIKE-04 measures.
func qk25Fixture(t *testing.T, e *env, c *quark.Client) {
	t.Helper()
	ddl := []string{
		`CREATE TABLE IF NOT EXISTS qk25_rows (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL)`,
		`DELETE FROM qk25_rows`,
		`INSERT INTO qk25_rows (name) VALUES ('alpha'), ('beta'), ('100% off')`,
	}
	for _, stmt := range ddl {
		if _, err := c.Raw().ExecContext(e.ctx, stmt); err != nil {
			t.Fatalf("qk25 fixture %q: %v", stmt, err)
		}
	}
}

// hasEscape reports whether a statement carries a LIKE ... ESCAPE tail. The
// comparison is case-insensitive because the control is about the clause, not
// about how the builder spells its keywords.
func qk25HasEscape(sql string) bool {
	return strings.Contains(strings.ToUpper(sql), "ESCAPE")
}

// qk25LastArgs renders the values bound by the most recent statement.
//
// The recorder exposes the SQL, and for one control (LIKE-03) that is not
// enough: a library can be engine-aware without writing a single different
// keyword, by escaping the VALUE for the engines that need it. Reading the
// bind is the only way to see that, so this reaches into the events the
// recorder already holds rather than widening the shared helper.
func qk25LastArgs(rec *recorder) string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.events) == 0 {
		return ""
	}
	return fmt.Sprintf("%#v", rec.events[len(rec.events)-1].Args)
}

// LIKE-01. Whether the query builder can write a LIKE ... ESCAPE tail.
//
// Read as a clause and not as rows: for a pattern with no wildcard in it both
// statements return the same rows, so only the emitted SQL separates them.
//
// The title says "the query builder", so the probe knocks on every public door
// that takes an operator string — Where, WhereNot, Having, HavingAggregate —
// plus the expression AST, instead of generalising from one of them. (Preload-
// Where is left out on purpose: it resolves the relation before it ever looks
// at the operator, so on this single-table fixture it answers a question about
// relations, not about tails.)
//
// And "accepted the operator" is not "emitted the tail": if the string ever
// enters the guard's whitelist the builder would interpolate it into a
// condition and the statement could still come out without an ESCAPE, or come
// out wrong. So a surface that takes the operator is only `present` when what
// it emitted carries the tail AND answers the one row that holds a literal
// percent sign; taking it and emitting something else is `partial`.
func probeQk25BuilderEmitsEscape(t *testing.T, e *env) verdict {
	c, rec := e.fresh(t, "qk25_builder_escape")
	qk25Fixture(t, e, c)

	// 1. Does an ordinary LIKE already carry the tail on its own?
	rec.reset()
	rows, err := quark.For[qk25Row](e.ctx, c).
		Where("name", "LIKE", qk25ContainsLiteralPercent).List()
	if err != nil {
		t.Fatalf("a plain LIKE should run on SQLite: %v", err)
	}
	emitted := rec.last()
	if emitted == "" {
		t.Fatalf("no statement was recorded for a LIKE query")
	}
	if qk25HasEscape(emitted) {
		if len(rows) == qk25WildcardRows {
			return present
		}
		t.Logf("the builder writes an ESCAPE tail but answered %d rows, want %d: %s",
			len(rows), qk25WildcardRows, emitted)
		return partial
	}

	// 2. Ask for the tail outright, on every surface that takes an operator.
	//    The guard's operator whitelist is what answers, and what it answers
	//    is the measurement.
	const op = `LIKE ? ESCAPE '\'`
	const pattern = `%100\%%`
	type surface struct {
		name string
		// want is the number of fixture rows a working tail owes this
		// surface, or -1 where the row count answers a different question
		// (a HAVING aggregates over the whole fixture, so it cannot).
		want int
		run  func() (int, error)
	}
	surfaces := []surface{
		{"Where", qk25WildcardRows, func() (int, error) {
			r, err := quark.For[qk25Row](e.ctx, c).Where("name", op, pattern).List()
			return len(r), err
		}},
		{"WhereNot", qk25AllRows - qk25WildcardRows, func() (int, error) {
			r, err := quark.For[qk25Row](e.ctx, c).WhereNot("name", op, pattern).List()
			return len(r), err
		}},
		{"Having", -1, func() (int, error) {
			r, err := quark.For[qk25Row](e.ctx, c).Having("name", op, pattern).List()
			return len(r), err
		}},
		{"HavingAggregate", -1, func() (int, error) {
			r, err := quark.For[qk25Row](e.ctx, c).
				HavingAggregate("COUNT", "name", op, pattern).List()
			return len(r), err
		}},
		{"WhereExpr", -1, func() (int, error) {
			r, err := quark.For[qk25Row](e.ctx, c).
				WhereExpr(quark.Cmp(quark.Col("name"), "LIKE ESCAPE", quark.Lit(pattern))).List()
			return len(r), err
		}},
	}

	var accepted, working []string
	for _, s := range surfaces {
		rec.reset()
		n, runErr := s.run()
		if runErr != nil {
			t.Logf("%s refused the operator: %v", s.name, runErr)
			continue
		}
		accepted = append(accepted, s.name)
		stmt := rec.last()
		if qk25HasEscape(stmt) && (s.want < 0 || n == s.want) {
			working = append(working, s.name)
			continue
		}
		t.Logf("%s took the operator but emitted %q answering %d rows (want %d)",
			s.name, stmt, n, s.want)
	}

	switch {
	case len(working) > 0:
		t.Logf("surfaces that emit a working tail: %v", working)
		return present
	case len(accepted) > 0:
		t.Logf("surfaces that take the operator without emitting a usable tail: %v", accepted)
		return partial
	default:
		return absent
	}
}

// LIKE-02. Whether Quark takes the value of an application-composed LIKE as
// literal text.
//
// Rows and clause together, because neither answers alone. The pattern is
// built the way an application builds a "contains" search — surround the
// user's text with wildcards — and the user's text is itself a single "%".
//
// The title is narrower than "neutralises the wildcard the user typed", and
// the arithmetic is why. On this surface Quark receives `%%%` and cannot tell
// the user's percent sign from the two the application added, so the answer
// that would prove a per-character neutralisation — one row, "100% off" — is
// not produced by any correct implementation; a branch that waited for it
// would be a `present` nothing can reach, and the day the capability arrived
// the bench would have gone on publishing a stale `absent` without ever
// turning red. What IS reachable, and what this control now claims, is the
// whole value taken as text with an escape character declared: a search for
// the literal string "%%%", which this fixture answers with no rows.
//
// Escaping the value without declaring that character also answers no rows, so
// the ESCAPE tail is what separates the capability from the defect LIKE-08
// measures. A refusal is neither — it leaves a literal `%` unsearchable — so
// it is `partial` too. The surface that may legitimately refuse is LIKE-05.
func probeQk25LikeValueIsLiteralText(t *testing.T, e *env) verdict {
	c, rec := e.fresh(t, "qk25_user_wildcard")
	qk25Fixture(t, e, c)

	rec.reset()
	rows, err := quark.For[qk25Row](e.ctx, c).
		Where("name", "LIKE", qk25ContainsLiteralPercent).List()
	if err != nil {
		t.Logf("the LIKE was refused rather than taken as text: %v", err)
		return partial
	}
	stmt := rec.last()
	switch {
	case len(rows) == qk25AllRows && !qk25HasEscape(stmt):
		// The value reached the bind untouched and the statement carries no
		// tail: every `%` is still a wildcard, so the search answers every row.
		// This is the capability at its most broken, and it has a verdict of
		// its own — nothing else below may land on it.
		return absent
	case len(rows) == 0 && qk25HasEscape(stmt):
		// The value is text, and the statement says which character makes it
		// text: a search for the literal "%%%", which nothing here is named.
		return present
	case len(rows) == 0:
		t.Logf("the value was taken as text but the statement declares no escape "+
			"character, so the search under-answers (see LIKE-08): %s", stmt)
		return partial
	default:
		t.Logf("neither shape: %d rows (untouched %d, taken as text 0) from %s",
			len(rows), qk25AllRows, stmt)
		return partial
	}
}

// LIKE-03. Whether what Quark emits for a LIKE varies with the engine.
//
// The engines differ: PostgreSQL, MySQL and MariaDB read a backslash in a LIKE
// pattern as an escape character with no ESCAPE clause, while SQLite, SQL
// Server and Oracle read it as an ordinary character. A library that knew that
// would emit something different for the two groups.
//
// So the measurement is the VARIATION itself, not the presence of one keyword:
// counting how many dialects write "ESCAPE" answers a different question (six
// of six writing the same tail is not variation, it is uniformity), and it
// cannot see engine-awareness that lives in the bound VALUE instead of in the
// SQL. The probe normalises away the two things that always differ and carry
// no meaning here — the placeholder spelling and the identifier quoting — and
// then compares the six statements and the six binds against each other.
//
// The clients are dialect-only: they run over SQLite, so the statement fails
// for the dialects whose placeholders SQLite cannot bind. That is irrelevant
// here — Count notifies the observer with the statement it ran before the
// result is scanned, so the SQL is recorded either way, and the SQL is the
// whole measurement.
func probeQk25DialectAwareLike(t *testing.T, e *env) verdict {
	dialects := []quark.Dialect{
		quark.SQLite(), quark.PostgreSQL(), quark.MySQL(),
		quark.MariaDB(), quark.MSSQL(), quark.Oracle(),
	}

	shape := map[string]string{} // dialect -> normalised statement + bind
	byShape := map[string][]string{}
	for _, d := range dialects {
		c, rec := e.fresh(t, "qk25_dialect_"+d.Name(), quark.WithDialect(d))
		rec.reset()
		if _, err := quark.For[qk25Row](e.ctx, c).Where("name", "LIKE", `%50\%%`).Count(); err != nil {
			t.Logf("%s: %v (the statement is what matters, not the result)", d.Name(), err)
		}
		stmt := rec.last()
		if stmt == "" {
			t.Fatalf("%s: no statement recorded", d.Name())
		}
		if !strings.Contains(strings.ToUpper(stmt), " LIKE ") {
			t.Fatalf("%s: the recorded statement is not the LIKE query: %s", d.Name(), stmt)
		}
		s := qk25Normalise(stmt) + " || " + qk25LastArgs(rec)
		shape[d.Name()] = s
		byShape[s] = append(byShape[s], d.Name())
		t.Logf("%s: %s", d.Name(), s)
	}

	if len(byShape) == 1 {
		// One shape for six engines: the same escape-less LIKE and the same
		// bind everywhere, which is the absence this control is about.
		return absent
	}

	// It varies. Whether that variation is the one the title means — the
	// engines that need an escape treated differently from the ones that do
	// not — decides between a capability and a coincidence.
	wantGroup := map[bool][]string{}
	for name := range shape {
		wantGroup[qk25DefaultEscape[name]] = append(wantGroup[qk25DefaultEscape[name]], name)
	}
	aligned := len(byShape) == 2
	if aligned {
		for _, group := range wantGroup {
			first := shape[group[0]]
			for _, name := range group {
				if shape[name] != first {
					aligned = false
				}
			}
		}
	}
	if aligned {
		return present
	}
	t.Logf("the emitted forms vary, but not along the default-escape boundary: %v", byShape)
	return partial
}

// qk25DefaultEscape says, per dialect name, whether a backslash in a LIKE
// pattern already means "escape the next character" with no ESCAPE clause.
// This is the boundary a dialect-aware LIKE would have to straddle, and it is
// what LIKE-03 compares the emitted forms against.
var qk25DefaultEscape = map[string]bool{
	"postgres": true,
	"mysql":    true,
	"mariadb":  true,
	"sqlite":   false,
	"mssql":    false,
	"oracle":   false,
}

var qk25Placeholder = regexp.MustCompile(`\$\d+|@p\d+|:\d+|\?`)

// qk25Normalise removes what always differs between dialects and says nothing
// about escaping — the placeholder spelling and the identifier quoting — so
// that what is left compares only the shape of the clause. Case goes with it
// because Oracle folds unquoted identifiers to upper case.
func qk25Normalise(sql string) string {
	s := qk25Placeholder.ReplaceAllString(sql, "@")
	s = strings.NewReplacer(`"`, "", "`", "", "[", "", "]", "").Replace(s)
	return strings.ToUpper(strings.Join(strings.Fields(s), " "))
}

// LIKE-04. Whether an application can reach an engine-native LIKE ... ESCAPE
// at all, outside the builder.
//
// Client.Raw is exported, ungated and documented as the escape hatch, so this
// runs the statement an application would write there and checks the ANSWER,
// not just that the call returned: the point of the hatch is that it gives the
// one row that holds a literal percent sign, which no builder path does.
//
// The second half records the shape of the hatch: RawQuery, the guarded raw
// path, is closed unless the client was built with WithLimits, so "raw SQL is
// available" and "RawQuery is available" are not the same statement.
func probeQk25RawEscapeHatch(t *testing.T, e *env) verdict {
	c, _ := e.fresh(t, "qk25_raw_hatch")
	qk25Fixture(t, e, c)

	var got int
	err := c.Raw().
		QueryRowContext(e.ctx, `SELECT COUNT(*) FROM qk25_rows WHERE name LIKE ? ESCAPE '\'`, `%100\%%`).
		Scan(&got)
	if err != nil {
		t.Logf("the raw ESCAPE statement failed: %v", err)
		return absent
	}

	if _, rawErr := c.RawQuery(e.ctx, `SELECT id FROM qk25_rows WHERE name LIKE ? ESCAPE '\'`, `%100\%%`); rawErr != nil {
		t.Logf("the guarded raw path is closed by default: %v", rawErr)
	}

	if got == qk25WildcardRows {
		return present
	}
	t.Logf("the raw ESCAPE statement answered %d rows, want %d", got, qk25WildcardRows)
	return partial
}

// LIKE-05. Whether the typed column accessor — the surface generated code
// hands a search box — refuses or neutralises a wildcard the user typed.
//
// The title says GENERATED, so the equivalence between what this probe builds
// by hand and what `quark gen` emits has to be measured, not assumed: the CLI
// is a module of its own (ADR-0024) and cannot be imported from here, but its
// golden output is checked into the tree and regenerated by the codegen tests,
// so the probe reads that file and fails outright if the generator has moved
// to a different accessor. A probe that quietly keeps exercising the old
// surface would report on a door no application is handed any more.
//
// Same defect as LIKE-02 seen from the other public WHERE surface, and worth
// its own control: this is the path an application gets for free, so one that
// never writes a string operator still meets it. Unlike LIKE-02 the title
// admits a refusal, so a refusal is `present` here.
func probeQk25TypedLikeGuardsValue(t *testing.T, e *env) verdict {
	const golden = "cmd/quark/internal/codegen/sample/quark_gen.go"
	gen, ok := qk25RepoFile(golden)
	if !ok {
		t.Fatalf("cannot read %s: this probe claims to measure the generated surface and has nothing to check it against", golden)
	}
	if !strings.Contains(gen, "quark.NewTypedStringColumn(") {
		t.Fatalf("%s no longer builds a string column with quark.NewTypedStringColumn: "+
			"this probe exercises an accessor `quark gen` does not hand an application any more", golden)
	}

	c, rec := e.fresh(t, "qk25_typed_like")
	qk25Fixture(t, e, c)

	name := quark.NewTypedStringColumn("name")

	// Ground before verdict. The branch below reads an error as "the accessor
	// refused the user's wildcard", and that reading only holds once the
	// accessor is known to run at all. `Like` is a plain Predicate{col,
	// "LIKE", value}: an error can come from anywhere along the way — an
	// unbounded-read guard turning on by default, a dialect change, WhereP
	// itself — and every one of those is the capability BROKEN wearing the
	// verdict of the capability working. Without this check, "the accessor is
	// broken" and "the accessor protects the user" are the same verdict.
	ground, err := quark.For[qk25Row](e.ctx, c).WhereP(name.Like("%alpha%")).List()
	if err != nil || len(ground) != 1 {
		t.Fatalf("the typed accessor cannot run an ordinary contains-search "+
			"(rows=%d err=%v, want 1): the probe's ground moved, which is not "+
			"Quark refusing a wildcard", len(ground), err)
	}

	rec.reset()
	rows, err := quark.For[qk25Row](e.ctx, c).WhereP(name.Like(qk25ContainsLiteralPercent)).List()
	if err != nil {
		// Attributable now: the same accessor answered a wildcard-free value a
		// moment ago, so what it turned down is the VALUE.
		t.Logf("the typed accessor refused the value: %v", err)
		return present
	}
	stmt := rec.last()
	switch {
	case len(rows) == qk25AllRows && !qk25HasEscape(stmt):
		return absent
	case len(rows) == 0 && qk25HasEscape(stmt):
		// Neutralised: see the constant's comment for why the neutralised
		// shape on a pattern the application composed is no rows and not one.
		return present
	default:
		t.Logf("neither shape: %d rows (untouched %d, neutralised 0) from %s",
			len(rows), qk25AllRows, stmt)
		return partial
	}
}

// LIKE-06. Whether Quark's SQL guard looks at the LIKE VALUE, not only at the
// operator.
//
// The title's "not only" presupposes the operator half, so the probe asserts
// it instead of logging it: if the whitelist stopped refusing an operator
// outside it, "operator-shaped and value-blind" would no longer describe this
// guard and every verdict below would be about something else. That is a
// broken probe, not an absence, so it fails the test and asks for the control
// to be rewritten.
//
// The value it then sends is the contains-search pattern, not a bare "%": a
// bare one cannot distinguish a guard that neutralised the value (no name is
// exactly "%", so zero rows) from one that escaped it without declaring an
// escape character (also zero rows). Wrapped, the three states are three
// different numbers, so `present` is reachable — a probe whose success is
// arithmetically impossible cannot see its own control being closed.
func probeQk25GuardInspectsLikeValue(t *testing.T, e *env) verdict {
	c, rec := e.fresh(t, "qk25_guard_value")
	qk25Fixture(t, e, c)

	if _, err := quark.For[qk25Row](e.ctx, c).Where("name", "ILIKE", "x").List(); err == nil {
		t.Fatalf("the operator whitelist no longer refuses ILIKE: this control is the " +
			"contrast between a guard that reads the operator and one that reads the " +
			"value, and half of it just disappeared — rewrite the control before " +
			"trusting its verdict")
	} else {
		t.Logf("operator refused: %v", err)
	}

	// The other half of the contrast: an ordinary LIKE has to RUN on this
	// fixture. Without it a generic failure of List() — an unbounded-read
	// guard defaulting on, say, which would also have failed the ILIKE above
	// and so sails through the assertion before this one — would read as "the
	// guard inspected the value", which is the opposite of what happened. A
	// control that measures a contrast needs both of its halves alive for the
	// contrast to mean anything.
	if plain, groundErr := quark.For[qk25Row](e.ctx, c).
		Where("name", "LIKE", "%alpha%").List(); groundErr != nil || len(plain) != 1 {
		t.Fatalf("an ordinary LIKE no longer runs on this fixture (rows=%d err=%v, "+
			"want 1): the other half of the contrast just disappeared — rewrite the "+
			"control before trusting its verdict", len(plain), groundErr)
	}

	rec.reset()
	rows, err := quark.For[qk25Row](e.ctx, c).
		Where("name", "LIKE", qk25ContainsLiteralPercent).List()
	if err != nil {
		// Both premises hold — the whitelist refuses an operator, an ordinary
		// LIKE runs — so this refusal is the guard reading the VALUE.
		t.Logf("the value was refused: %v", err)
		return present
	}
	stmt := rec.last()
	switch {
	case len(rows) == qk25AllRows && !qk25HasEscape(stmt):
		// Operator-shaped and value-blind: the refusal happens on one and
		// never on the other.
		return absent
	case len(rows) == 0 && qk25HasEscape(stmt):
		// The guard read the value and made it text, declaring the character
		// that makes it text (see the constant's comment for why that is no
		// rows here and not one).
		return present
	case len(rows) == 0:
		t.Logf("the value was escaped but the statement declares no escape character: %s", stmt)
		return partial
	default:
		t.Logf("neither shape: %d rows from %s", len(rows), stmt)
		return partial
	}
}

// LIKE-07. Whether the `LIKE ? ESCAPE '<c>'` form is proven across the engines
// Quark ships on.
//
// Two halves, and only one of them is about this bench. The first is the
// SPELLING: SQLite accepts a single backslash and rejects the doubled one
// ("ESCAPE expression must be a single character"), while MySQL and MariaDB
// need the doubled one because a lone backslash inside a string literal leaves
// it unterminated. That is why the form has to be proven per engine instead of
// once — and it is the probe's own ground, so when it moves the probe fails
// rather than reporting an absence in Quark that it did not observe.
//
// The second half is the coverage the title actually claims, and it is what
// decides the verdict: does the proof EXIST for each engine of the CI matrix?
// That lives in internal/enginesuite, which this bench cannot run but can
// read — the same reading the three CI controls below do. Without it this
// control had no road to `present` at all: every branch returned partial or
// absent, so the day the proof is written the bench would have gone on
// publishing `partial` and never asked for the update.
//
// What counts as the proof is a SQL form and not a pair of words, and it is
// credited to the engine that runs it and not to all five — qk25EnginesProving-
// Escape explains both, and both are the difference between reading a test and
// reading a comment.
func probeQk25EscapeLiteralPortability(t *testing.T, e *env) verdict {
	c, _ := e.fresh(t, "qk25_escape_literal")
	qk25Fixture(t, e, c)

	var single int
	singleErr := c.Raw().
		QueryRowContext(e.ctx, `SELECT COUNT(*) FROM qk25_rows WHERE name LIKE ? ESCAPE '\'`, `%100\%%`).
		Scan(&single)
	if singleErr != nil || single != qk25WildcardRows {
		t.Fatalf("the single-backslash ESCAPE form no longer holds on this bench's own "+
			"engine (rows=%d err=%v): the probe's ground moved, which is not the same "+
			"thing as Quark losing a capability", single, singleErr)
	}

	var doubled int
	doubledErr := c.Raw().
		QueryRowContext(e.ctx, `SELECT COUNT(*) FROM qk25_rows WHERE name LIKE ? ESCAPE '\\'`, `%100\%%`).
		Scan(&doubled)
	if doubledErr == nil {
		t.Logf("both escape literals are accepted here, so this engine does not show the " +
			"spelling difference the other engines have")
	} else {
		t.Logf("the doubled form MySQL requires is rejected here: %v", doubledErr)
	}

	engines := qk25MatrixEngines(t)
	proven := qk25EnginesProvingEscape(t, engines)
	missing := []string{}
	for _, name := range engines {
		if !proven[name] {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return present
	}
	t.Logf("engines with no assertion on the LIKE ... ESCAPE form in internal/enginesuite: %v", missing)
	if len(missing) == len(engines) {
		return absent
	}
	return partial
}

// qk25SharedEntryPoint is the file that holds SharedSuite, the function every
// engine lane calls. It is the only file whose content can be credited to all
// five engines at once.
const qk25SharedEntryPoint = "suite_test.go"

// qk25EscapeAssertion is the SQL SHAPE this control counts: a LIKE whose
// pattern is bound (or written as a literal) followed by an ESCAPE tail that
// names its character.
//
// The form is read instead of the two words "LIKE" and "ESCAPE" because the
// WORDS are what prose writes and the FORM is what a test writes. This module
// is full of comments about an "escape hatch" — client_options_strict_test.go,
// join_builder_test.go, strict_reads_test.go, update_zero_values_test.go — and
// a single "Unlike ..." in one of those lines would have spelled both tokens
// and credited every engine with a proof nobody wrote.
var qk25EscapeAssertion = regexp.MustCompile(`(?i)LIKE\s+(\?|:\w+|\$\d+|@\w+|'[^']*')\s+ESCAPE\s+'`)

// qk25EnginesProvingEscape reads internal/enginesuite and reports, per engine,
// whether a test there asserts on the `LIKE ... ESCAPE` form.
//
// Attribution is the other half of the measurement. An assertion in the shared
// entry point counts for every engine, because every lane calls SharedSuite —
// but "the file name carries no engine prefix" is not the same statement as
// "this is the shared entry point", and treating it as such credited five
// engines for one line in any of the ninety-odd files here, including files
// that are specific to one engine without being named for it
// (rls_native_postgres_test.go, replicas_postgres_test.go). So the shared
// credit comes only from suite_test.go, an engine's own credit from its name
// in the file or in the line, and an assertion nothing attributes fails the
// probe: spreading it over everyone is exactly the silent green this control
// exists to prevent.
//
// The suite is another module, so this is a read and not a run — which is the
// most a bench on SQLite can do about five engines it cannot boot.
func qk25EnginesProvingEscape(t *testing.T, engines []string) map[string]bool {
	t.Helper()
	proven := map[string]bool{}
	paths, err := filepath.Glob(filepath.Join("..", "enginesuite", "*_test.go"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("internal/enginesuite holds no tests to read (%v): the probe cannot see "+
			"the proof it is looking for, which is not the same as the proof being absent", err)
	}
	for _, p := range paths {
		b, readErr := os.ReadFile(p)
		if readErr != nil {
			t.Fatalf("read %s: %v", p, readErr)
		}
		base := filepath.Base(p)
		for i, ln := range strings.Split(string(b), "\n") {
			// A commented-out or described form is not an assertion.
			if strings.HasPrefix(strings.TrimSpace(ln), "//") {
				continue
			}
			if !qk25EscapeAssertion.MatchString(ln) {
				continue
			}
			if base == qk25SharedEntryPoint {
				for _, engine := range engines {
					proven[engine] = true
				}
				continue
			}
			named := false
			hay := strings.ToLower(base + " " + ln)
			for _, engine := range engines {
				if strings.Contains(hay, engine) {
					proven[engine] = true
					named = true
				}
			}
			if !named {
				t.Fatalf("%s:%d asserts the LIKE ... ESCAPE form but names no engine and "+
					"is not %s: this control counts the proof PER engine, and handing an "+
					"unattributable one to all of them is how it would go green without a "+
					"proof. Name the engine in the file or in the assertion, or move it "+
					"into SharedSuite.", base, i+1, qk25SharedEntryPoint)
			}
		}
	}
	return proven
}

// LIKE-08. Whether a literal percent sign in a value can be matched exactly
// through the builder.
//
// The absence is measured as two wrong numbers around the right one: with the
// value left alone the answer is every row, and with the value escaped by hand
// the answer is no rows, because without an ESCAPE clause SQLite reads the
// backslash as an ordinary character and looks for a name that contains one.
// The two halves of the fix — emit the clause and escape the value — are only
// correct together; either one alone moves the defect instead of closing it.
func probeQk25LiteralPercentMatch(t *testing.T, e *env) verdict {
	c, _ := e.fresh(t, "qk25_literal_percent")
	qk25Fixture(t, e, c)

	plain, err := quark.For[qk25Row](e.ctx, c).Where("name", "LIKE", `%%%`).List()
	if err != nil {
		t.Fatalf("a LIKE with a wildcard pattern should run: %v", err)
	}
	escaped, err := quark.For[qk25Row](e.ctx, c).Where("name", "LIKE", `%\%%`).List()
	if err != nil {
		t.Fatalf("a LIKE with a backslash in the value should run: %v", err)
	}

	if len(plain) == qk25WildcardRows || len(escaped) == qk25WildcardRows {
		return present
	}
	t.Logf("literal %%-search answers: unescaped %d rows, hand-escaped %d rows, correct %d",
		len(plain), len(escaped), qk25WildcardRows)
	return absent
}

// --- what the CI exercises -------------------------------------------------
//
// These three controls are about the workflow that decides whether a change
// can merge, so the artefact under measurement IS the workflow — there is no
// engine to drive from here. They parse its job graph (job ids, the matrix
// entries, the aggregating gate's needs list) rather than searching for words
// in it, and their titles claim only what parsing can support: what the
// workflow DECLARES. Whether a GitHub branch protection rule actually requires
// the gate is outside this repository and outside this bench.

// qk25RepoFile reads a file by its path from the repository root. The bench
// package sits two directories down, which is what anchors the walk.
func qk25RepoFile(rel string) (string, bool) {
	b, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
	if err != nil {
		return "", false
	}
	return string(b), true
}

var (
	qk25JobID    = regexp.MustCompile(`^  ([a-z][a-z0-9_-]*):\s*$`)
	qk25NeedsRef = regexp.MustCompile(`^      - ([a-z][a-z0-9_-]*)\s*$`)
	qk25Engine   = regexp.MustCompile(`^\s+- engine: ([a-z0-9]+)\s*$`)
	qk25SoftFlag = regexp.MustCompile(`continue-on-error:\s*(\S+)`)
)

// qk25JobBlocks splits a workflow into its jobs: id -> the lines of that job,
// starting after the top-level `jobs:` key so the `on:` block's own two-space
// keys (push, pull_request) cannot be mistaken for job ids.
func qk25JobBlocks(workflow string) (order []string, blocks map[string][]string) {
	blocks = map[string][]string{}
	inJobs := false
	current := ""
	for _, ln := range strings.Split(workflow, "\n") {
		if strings.HasPrefix(ln, "jobs:") {
			inJobs = true
			continue
		}
		if !inJobs {
			continue
		}
		if m := qk25JobID.FindStringSubmatch(ln); m != nil {
			current = m[1]
			order = append(order, current)
			continue
		}
		if current != "" {
			blocks[current] = append(blocks[current], ln)
		}
	}
	return order, blocks
}

// qk25JobNeeds reads a job's `needs:` list.
func qk25JobNeeds(lines []string) map[string]bool {
	needs := map[string]bool{}
	inNeeds := false
	for _, ln := range lines {
		trimmed := strings.TrimSpace(ln)
		if trimmed == "needs:" {
			inNeeds = true
			continue
		}
		if strings.HasPrefix(trimmed, "needs:") {
			// Inline form: `needs: [a, b]` or `needs: a`.
			rest := strings.Trim(strings.TrimPrefix(trimmed, "needs:"), " []")
			for _, id := range strings.Split(rest, ",") {
				if id = strings.TrimSpace(id); id != "" {
					needs[id] = true
				}
			}
			continue
		}
		if inNeeds {
			if m := qk25NeedsRef.FindStringSubmatch(ln); m != nil {
				needs[m[1]] = true
				continue
			}
			if trimmed != "" {
				inNeeds = false
			}
		}
	}
	return needs
}

// qk25AggregatingGate finds the gate by its SHAPE — the job that waits on
// every other job in the file — instead of by a literal id.
//
// A rename is not an absence: looking the gate up by name turns "I did not
// find that string" into "this workflow has no gate", which is the one reading
// a bench must never publish.
func qk25AggregatingGate(order []string, blocks map[string][]string) (string, map[string]bool, bool) {
	for _, id := range order {
		needs := qk25JobNeeds(blocks[id])
		if len(needs) == 0 {
			continue
		}
		covers := true
		for _, other := range order {
			if other == id {
				continue
			}
			if !needs[other] {
				covers = false
				break
			}
		}
		if covers {
			return id, needs, true
		}
	}
	return "", nil, false
}

// qk25MatrixEngines reads the engines the CI integration matrix declares. It
// is the denominator for "every engine" in three controls, so it comes from
// the workflow rather than from a list written here that could drift from it.
func qk25MatrixEngines(t *testing.T) []string {
	t.Helper()
	workflow, ok := qk25RepoFile(".github/workflows/ci.yml")
	if !ok {
		t.Fatalf("no .github/workflows/ci.yml to read: the probe cannot see the lanes")
	}
	_, blocks := qk25JobBlocks(workflow)
	var engines []string
	for _, ln := range blocks["integration"] {
		if m := qk25Engine.FindStringSubmatch(ln); m != nil {
			engines = append(engines, m[1])
		}
	}
	return engines
}

// LIKE-09. Whether the workflow declares a lane per real engine and an
// all-engines acceptance, and whether any of them is allowed to fail soft.
//
// A lane that names an engine but carries continue-on-error reports green
// whatever it found, so the halves are one control: the lanes exist AND a
// failure in them is a failure.
//
// Two things the earlier reading got wrong, and both would have moved this
// verdict for reasons the title does not name. The soft check was a substring,
// so `continue-on-error: false` — an explicit hardening — read as a soft lane;
// it parses the VALUE now. And it swept every workflow in the repository, so a
// soft step in a release or scorecard workflow moved a control about engine
// lanes; it is scoped now to the jobs this same probe identified as lanes.
func probeQk25CIDeclaresEngineLanes(t *testing.T, e *env) verdict {
	workflow, ok := qk25RepoFile(".github/workflows/ci.yml")
	if !ok {
		t.Logf("no .github/workflows/ci.yml to read")
		return absent
	}
	order, blocks := qk25JobBlocks(workflow)

	want := map[string]bool{"postgres": false, "mysql": false, "mariadb": false, "mssql": false, "oracle": false}
	for _, engine := range qk25MatrixEngines(t) {
		if _, known := want[engine]; known {
			want[engine] = true
		}
	}
	missing := []string{}
	for engine, found := range want {
		if !found {
			missing = append(missing, engine)
		}
	}

	// The acceptance lane is named in the title because it moves this verdict:
	// it is the one job that runs every engine at once, under a strict gate.
	allEngines := false
	for _, ln := range blocks["superapp"] {
		if strings.Contains(ln, "-engines=all") && strings.Contains(ln, "-gate=strict") {
			allEngines = true
		}
	}

	// Which jobs are lanes: the ones the aggregating gate waits on. A soft
	// failure outside them is someone else's control.
	lanes := map[string]bool{"integration": true, "superapp": true}
	if _, needs, found := qk25AggregatingGate(order, blocks); found {
		for id := range needs {
			lanes[id] = true
		}
	}
	soft := []string{}
	for id := range lanes {
		for _, ln := range blocks[id] {
			m := qk25SoftFlag.FindStringSubmatch(ln)
			if m == nil {
				continue
			}
			// `false` is a hardening, not a soft lane. An expression is read
			// as soft because this probe cannot evaluate it.
			if v := strings.Trim(m[1], `"'`); v == "true" || strings.HasPrefix(v, "${{") {
				soft = append(soft, id+": "+strings.TrimSpace(ln))
			}
		}
	}

	switch {
	case len(missing) == 0 && allEngines && len(soft) == 0:
		return present
	case len(missing) == len(want):
		t.Logf("no engine lane declared")
		return absent
	default:
		t.Logf("engine lanes missing %v; all-engines acceptance %v; soft-failing lanes %v",
			missing, allEngines, soft)
		return partial
	}
}

// LIKE-10. Whether one aggregating check covers every lane the workflow
// declares.
//
// "Covers" is three facts, and the probe used to read two. The gate's needs
// list is compared with the set of job ids in the same file, so a lane added
// without being listed turns this control red; `if: always()` belongs to the
// same control, because a gate skipped when a lane fails is a gate that goes
// green by not running. The third is the one that was missing: a gate with
// every need and always() that never READS the results is strictly worse than
// no gate at all — it is guaranteed green. So the probe now also requires a
// step to consume the `needs` context and to exit non-zero on it.
func probeQk25CIRequiredGateCoversLanes(t *testing.T, e *env) verdict {
	workflow, ok := qk25RepoFile(".github/workflows/ci.yml")
	if !ok {
		t.Logf("no .github/workflows/ci.yml to read")
		return absent
	}
	order, blocks := qk25JobBlocks(workflow)

	gate, needs, found := qk25AggregatingGate(order, blocks)
	if !found {
		t.Logf("jobs declared: %v, none of them waits on all the others", order)
		return absent
	}
	gateLines := blocks[gate]

	always := false
	readsNeeds := false
	failsOnResult := false
	for _, ln := range gateLines {
		trimmed := strings.TrimSpace(ln)
		if trimmed == "if: always()" {
			always = true
		}
		if strings.Contains(ln, "toJSON(needs)") || strings.Contains(ln, "needs.") {
			readsNeeds = true
		}
		if strings.Contains(ln, "exit 1") {
			failsOnResult = true
		}
	}

	uncovered := []string{}
	for _, job := range order {
		if job == gate {
			continue
		}
		if !needs[job] {
			uncovered = append(uncovered, job)
		}
	}

	if len(uncovered) == 0 && always && readsNeeds && failsOnResult {
		return present
	}
	t.Logf("gate %q: lanes outside its needs %v; if: always() %v; reads the needs context %v; "+
		"exits non-zero on a result %v", gate, uncovered, always, readsNeeds, failsOnResult)
	return partial
}

// LIKE-11. Whether an engine lane fails, rather than skips, when its engine
// does not answer.
//
// The mechanism that decides this is in the suite sources, so that is what the
// probe reads, and it reads a SHAPE rather than a word: a skip that follows a
// failed connectivity check. A suite that skips when its DSN is unset is not
// the same thing — under the integration tag the DSN always resolves — but a
// suite that skips because the server did not answer turns a lane green while
// its engine was never exercised.
//
// What changed here is the denominator and the attribution. A lane runs TESTS,
// not files whose name ends in _suite_test.go: that glob both missed a hole in
// a file it did not match and counted files that are no lane's entry point, so
// the partial/absent cut moved with how many unrelated files exist. The sweep
// is the whole suite module now, the denominator is the engines the CI matrix
// declares, and a hole is charged to the engine its own skip message names —
// a suite skipping because REDIS did not answer is a different control, and
// counting it here would have pinned a cache dependency on an engine lane.
//
// And a hole that names no engine is charged to ALL of them. The natural home
// of such a skip is SharedSuite, the entry point all five lanes call: a failed
// Ping there turns every lane green with its engine never exercised, which is
// the most broken state this control can meet. Logging it and charging it to
// nobody let that state report `present` — the best verdict for the worst
// fact — so the shared floor is now read the way qk25EnginesProvingEscape
// reads it, in the direction that cannot manufacture green. Nothing may end in
// a t.Logf alone any more: a hole is charged to a lane, or to a non-engine
// dependency, or the probe fails, because an invisible hole is precisely what
// this control exists to see.
func probeQk25EngineLaneCannotSkip(t *testing.T, e *env) verdict {
	engines := qk25MatrixEngines(t)
	if len(engines) == 0 {
		t.Fatalf("the CI matrix declares no engine: the denominator of this control is gone")
	}

	paths, err := filepath.Glob(filepath.Join("..", "enginesuite", "*_test.go"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no tests to read under internal/enginesuite (%v): the probe is broken, "+
			"which is not the same as a lane that cannot skip", err)
	}

	// Dependencies a suite may also ping, and which belong to another control:
	// a skip because the cache is down says nothing about the engine lane.
	otherDeps := []string{"redis", "memcached"}

	holes := map[string][]string{}
	for _, p := range paths {
		b, readErr := os.ReadFile(p)
		if readErr != nil {
			t.Fatalf("read %s: %v", p, readErr)
		}
		lines := strings.Split(string(b), "\n")
		for i, ln := range lines {
			if !strings.Contains(ln, "Ping(") {
				continue
			}
			// A skip within the handful of lines that follow a connectivity
			// check is that check being treated as "not my problem".
			for j := i; j < len(lines) && j < i+6; j++ {
				if !strings.Contains(lines[j], "t.Skip") {
					continue
				}
				where := fmt.Sprintf("%s:%d", filepath.Base(p), j+1)
				owner := qk25SkipOwner(lines[i], lines[j], filepath.Base(p), engines, otherDeps)
				switch owner {
				case "":
					t.Fatalf("%s: a skip after a connectivity check was attributed to "+
						"nothing at all — qk25SkipOwner owes every hole a lane, the "+
						"shared floor or a non-engine dependency", where)
				case qk25SkipShared:
					// The shared floor. SharedSuite is what all five lanes call,
					// so one skip there is one hole in each of them; charging it
					// to a single lane, or to none, would read the state where
					// every engine goes unexercised as the state where one does.
					t.Logf("%s: a skip on the shared floor, charged to every lane", where)
					for _, engine := range engines {
						holes[engine] = append(holes[engine], where)
					}
				case "-":
					t.Logf("%s: skipped because a non-engine dependency did not answer", where)
				default:
					holes[owner] = append(holes[owner], where)
				}
				break
			}
		}
	}

	if len(holes) == 0 {
		return present
	}
	t.Logf("engine lanes whose suite skips after a failed connectivity check: %v", holes)
	if len(holes) == len(engines) {
		return absent
	}
	return partial
}

// qk25SkipShared marks a skip that belongs to the ground every lane runs: no
// engine names it and it does not sit in an engine's own file, so the only
// honest reading is that all five lanes carry it.
const qk25SkipShared = "*"

// qk25SkipOwner attributes a skip that follows a connectivity check: the
// engine named in the check or in the skip message, "-" when it names another
// dependency entirely, and qk25SkipShared when nothing names it and the file
// is no engine's.
//
// The last case used to be "", a hole charged to nobody. It is the shared
// floor now, and that is the safe direction: an unattributed skip over there
// is a skip in SharedSuite, which every lane calls.
func qk25SkipOwner(pingLine, skipLine, file string, engines, otherDeps []string) string {
	hay := strings.ToLower(pingLine + " " + skipLine)
	for _, dep := range otherDeps {
		if strings.Contains(hay, dep) {
			return "-"
		}
	}
	for _, engine := range engines {
		if strings.Contains(hay, engine) {
			return engine
		}
	}
	for _, engine := range engines {
		if strings.HasPrefix(strings.ToLower(file), engine+"_") {
			return engine
		}
	}
	return qk25SkipShared
}
