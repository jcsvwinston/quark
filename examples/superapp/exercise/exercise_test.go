package exercise

import (
	"context"
	"os"
	"testing"

	"github.com/jcsvwinston/quark/examples/superapp/control"
	"github.com/jcsvwinston/quark/examples/superapp/engine"

	_ "modernc.org/sqlite"
)

// TestExercisersSQLite corre los exercisers (crud + tx) contra SQLite
// in-process: asserts funcionales verdes, sin fugas, y la cobertura incluye los
// símbolos marcados.
func TestExercisersSQLite(t *testing.T) {
	ctx := context.Background()
	conns, err := engine.Up(ctx, control.SQLite)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer func() {
		engine.Down(control.SQLite)
		_ = os.Remove(conns[control.SQLite].DSN)
	}()

	results := Run(conns, 2, []Exerciser{CRUD, TX, BUILDER})
	r := results[control.SQLite]
	if r.Err != nil {
		t.Fatalf("exerciser: %v", r.Err)
	}
	if !r.Leak.OK() {
		t.Errorf("fuga: %s", r.Leak)
	}

	cov := Coverage(results)
	seen := cov[control.SQLite]
	for _, k := range []string{
		QM("Create"), QM("First"), QM("Count"), QM("Update"), QM("Delete"), QM("List"),
		QM("Where"), QM("Limit"), CM("Migrate"), CM("Tx"), QF("For"), QF("ForTx"), QF("New"),
		QM("Sum"), QM("Avg"), QM("Min"), QM("Max"), QM("GroupBy"), QM("WhereIn"), QM("Or"),
		QM("OrderBy"), QM("Offset"), QM("Distinct"), QM("Find"), QM("Iter"), QM("Cursor"), QM("Paginate"),
	} {
		if !seen[k] {
			t.Errorf("cobertura: falta el símbolo %s", k)
		}
	}
	t.Logf("crud cubrió %d símbolos en sqlite; statements capturados=%d", len(seen), r.Rec.Count())
}
