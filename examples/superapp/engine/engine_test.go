package engine

import (
	"context"
	"os"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/examples/superapp/control"
	"github.com/jcsvwinston/quark/examples/superapp/domain"

	_ "modernc.org/sqlite"
)

// newSQLiteClient abre un Client sin caché ni réplicas (para que la verificación
// de fugas mida sólo el pool base, sin goroutines de fondo opcionales).
func newTestClient(c Conn) (*quark.Client, error) {
	l := quark.DefaultLimits()
	l.SafeMigrations = false
	return quark.New(c.Driver, c.DSN, quark.WithLimits(l))
}

// exercise migra el dominio y hace una operación mínima de escritura+lectura.
func exercise(ctx context.Context) func(control.Engine, *quark.Client) error {
	return func(e control.Engine, client *quark.Client) error {
		if err := client.Migrate(ctx, domain.AllModels()...); err != nil {
			return err
		}
		a := &domain.Account{Email: "leak@superapp.test", Name: "leak", Role: "member", Active: true}
		if err := quark.For[domain.Account](ctx, client).Create(a); err != nil {
			return err
		}
		_, err := quark.For[domain.Account](ctx, client).Limit(10).List()
		return err
	}
}

// TestEngineSQLiteNoLeak ejerce el runner + el check de fugas contra SQLite
// in-process (sin Docker): tras cerrar el Client, el pool debe quedar a 0 y las
// goroutines volver a la línea base.
func TestEngineSQLiteNoLeak(t *testing.T) {
	ctx := context.Background()
	conns, err := Up(ctx, control.SQLite)
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	defer func() {
		Down(control.SQLite)
		_ = os.Remove(conns[control.SQLite].DSN)
	}()

	results := Run(conns, 2, newTestClient, exercise(ctx))
	r := results[control.SQLite]
	if r.Err != nil {
		t.Fatalf("exercise SQLite: %v", r.Err)
	}
	t.Logf("leak report — %s", r.Leak)
	if r.Leak.PoolLeaked() {
		t.Errorf("fuga de pool tras Close: inUse=%d open=%d", r.Leak.PoolInUse, r.Leak.PoolOpen)
	}
	if r.Leak.GoroutinesLeaked() {
		t.Errorf("fuga de goroutines: %d→%d (tol %d)", r.Leak.GoroutinesBefore, r.Leak.GoroutinesAfter, r.Leak.Tolerance)
	}
}
