package recorder

import (
	"context"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/examples/superapp/control"

	_ "modernc.org/sqlite"
)

// widget es un modelo mínimo, local al test: el end-to-end sólo necesita probar
// que el Recorder intercepta el SQL real de Quark y lo atribuye al símbolo. Los
// modelos ricos viven en domain/ y se ejercen en los slices de exercisers.
type widget struct {
	ID    int64  `db:"id" pk:"true"`
	Name  string `db:"name"`
	Score int    `db:"score"`
}

// claves de símbolo de juguete (no son la superficie real; sólo prueban el
// plumbing símbolo→SQL).
const (
	symCreate = "quark.(*Query[widget]).Create"
	symList   = "quark.(*Query[widget]).List"
	symFirst  = "quark.(*Query[widget]).First"
	symDelete = "quark.(*Query[widget]).Delete"
	symOption = "quark.WithReplicas"
)

func newWidgetClient(t *testing.T, rec *Recorder) *quark.Client {
	t.Helper()
	l := quark.DefaultLimits()
	l.SafeMigrations = false
	opts := append(rec.Options(), quark.WithLimits(l))
	client, err := quark.New("sqlite", ":memory:", opts...)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	if err := client.Migrate(context.Background(), &widget{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return client
}

// TestRecorderEndToEnd conduce un Client de SQLite real a través del Recorder y
// verifica las tres responsabilidades del slice: cobertura por símbolo, captura
// de SQL atribuida, y conteo de filas exacto (incluido el enriquecimiento del
// SELECT multi-fila vía observer).
func TestRecorderEndToEnd(t *testing.T) {
	rec := New(control.SQLite)
	client := newWidgetClient(t, rec)
	defer client.Close()
	ctx := context.Background()

	// El DDL de Migrate se ejecuta sin símbolo estampado; lo descartamos para
	// que los conteos midan sólo las operaciones marcadas.
	rec.Reset()

	// --- Create: en SQLite es INSERT ... RETURNING, así que viaja por la vía
	// query_row, no exec. El Recorder lo atribuye al símbolo igualmente. ---
	widgets := []*widget{
		{Name: "alpha", Score: 10},
		{Name: "beta", Score: 20},
	}
	for _, w := range widgets {
		cctx := rec.Mark(ctx, symCreate)
		if err := quark.For[widget](cctx, client).Create(w); err != nil {
			t.Fatalf("create %s: %v", w.Name, err)
		}
	}

	// --- List (QUERY multi-fila) ---
	lctx := rec.Mark(ctx, symList)
	got, err := quark.For[widget](lctx, client).Limit(10).List()
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("list devolvió %d filas, esperaba 2", len(got))
	}

	// --- First: SELECT ... LIMIT 1, también vía query (no query_row). ---
	fctx := rec.Mark(ctx, symFirst)
	one, err := quark.For[widget](fctx, client).Where("name", "=", "alpha").First()
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if one.Name != "alpha" {
		t.Fatalf("first devolvió %q, esperaba alpha", one.Name)
	}

	// --- Delete: DELETE ... WHERE id = ?, la vía exec inequívoca. ---
	dctx := rec.Mark(ctx, symDelete)
	n, err := quark.For[widget](dctx, client).Delete(widgets[1]) // beta
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if n != 1 {
		t.Fatalf("delete afectó %d filas, esperaba 1", n)
	}

	// Símbolo no-SQL marcado por Note.
	rec.Note(symOption)

	// --- Cobertura: los 5 símbolos quedaron registrados para SQLite ---
	inv := Collect(rec)
	seen := inv[control.SQLite]
	for _, sym := range []string{symCreate, symList, symFirst, symDelete, symOption} {
		if !seen[sym] {
			t.Errorf("cobertura: símbolo %q no registrado en %s", sym, control.SQLite)
		}
	}

	// --- Captura de SQL: atribución por símbolo + enriquecimiento de filas ---
	stmts := rec.Statements()
	for _, s := range stmts {
		sqlPrefix := s.SQL
		if len(sqlPrefix) > 60 {
			sqlPrefix = sqlPrefix[:60]
		}
		t.Logf("stmt sym=%-40s op=%-9s rows=%d sql=%q", s.Symbol, s.Op, s.Rows, sqlPrefix)
	}
	var sawCreate, sawListQuery, sawFirst, sawDeleteExec bool
	for _, s := range stmts {
		if s.Engine != control.SQLite {
			t.Errorf("statement con motor %q, esperaba %q", s.Engine, control.SQLite)
		}
		if s.SQL == "" {
			t.Errorf("statement sin SQL: %+v", s)
		}
		switch {
		case s.Symbol == symCreate:
			sawCreate = true
			if s.Rows < 1 {
				t.Errorf("create reportó %d filas, esperaba >=1", s.Rows)
			}
		case s.Symbol == symList && s.Op == OpQuery:
			sawListQuery = true
			if s.Rows != int64(len(got)) {
				t.Errorf("list QUERY: filas=%d, esperaba %d (enriquecimiento del observer)", s.Rows, len(got))
			}
		case s.Symbol == symFirst:
			sawFirst = true
			if s.Rows != 1 {
				t.Errorf("first: filas=%d, esperaba 1", s.Rows)
			}
		case s.Symbol == symDelete && s.Op == OpExec:
			sawDeleteExec = true
			if s.Rows != 1 {
				t.Errorf("delete EXEC: filas afectadas=%d, esperaba 1", s.Rows)
			}
		}
	}
	if !sawCreate {
		t.Error("no se capturó ningún statement atribuido a Create")
	}
	if !sawListQuery {
		t.Error("no se capturó ningún QUERY atribuido a List")
	}
	if !sawFirst {
		t.Error("no se capturó ningún statement atribuido a First")
	}
	if !sawDeleteExec {
		t.Error("no se capturó ningún EXEC atribuido a Delete (la vía WrapExec)")
	}

	// --- Telemetría del observer: conteo de filas exacto agregado ---
	tele := rec.Telemetry()
	if tele.Queries < 3 {
		t.Errorf("telemetría: %d eventos de observer, esperaba >=3 (create x2 / list / first)", tele.Queries)
	}
	if tele.Rows < int64(len(got)) {
		t.Errorf("telemetría: %d filas agregadas, esperaba >=%d", tele.Rows, len(got))
	}
}

// TestCoveragePlumbing prueba Mark/Note/ContributeTo/Collect sin tocar la base
// de datos: el plegado a control.Invoked es determinista y por-motor.
func TestCoveragePlumbing(t *testing.T) {
	rSQLite := New(control.SQLite)
	rPG := New(control.Postgres)

	ctx := rSQLite.Mark(context.Background(), "pkg.Foo")
	if got := symbolFrom(ctx); got != "pkg.Foo" {
		t.Fatalf("symbolFrom = %q, esperaba pkg.Foo", got)
	}
	rSQLite.Note("pkg.Bar", "", "pkg.Baz") // el vacío se ignora
	rPG.Note("pkg.Foo")

	inv := Collect(rSQLite, rPG, nil) // nil se salta

	want := map[control.Engine][]string{
		control.SQLite:   {"pkg.Foo", "pkg.Bar", "pkg.Baz"},
		control.Postgres: {"pkg.Foo"},
	}
	for e, syms := range want {
		for _, s := range syms {
			if !inv[e][s] {
				t.Errorf("inv[%s] no contiene %q", e, s)
			}
		}
	}
	if inv[control.SQLite][""] {
		t.Error("la clave vacía no debería registrarse")
	}
	if inv[control.Postgres]["pkg.Bar"] {
		t.Error("pkg.Bar se filtró al motor equivocado")
	}
}

// TestReset limpia statements/telemetría pero conserva la cobertura.
func TestReset(t *testing.T) {
	rec := New(control.SQLite)
	rec.Note("pkg.Kept")
	rec.record(Statement{Symbol: "pkg.Kept", Engine: control.SQLite, SQL: "SELECT 1", Op: OpQuery})
	if rec.Count() != 1 {
		t.Fatalf("Count = %d, esperaba 1", rec.Count())
	}
	rec.Reset()
	if rec.Count() != 0 {
		t.Errorf("tras Reset Count = %d, esperaba 0", rec.Count())
	}
	if !rec.Invoked()["pkg.Kept"] {
		t.Error("Reset borró la cobertura; debía conservarla")
	}
}
