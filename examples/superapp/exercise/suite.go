// Package exercise ejerce la superficie pública de Quark contra cada motor,
// marcando los símbolos que invoca (para el gate de cobertura del manifiesto) y
// asertando el resultado funcional. Reusa engine.Run (S4) para el lifecycle por
// motor + el chequeo de fugas; añade el recorder por motor y la cobertura.
//
// Un Exerciser cubre un área (crud, builder, relations, tx, cache, tenant,
// migrate, security, ha, observability). Cada uno es un `fn` que engine.Run
// corre por motor; los asserts funcionales viven dentro de cada Exerciser.
package exercise

import (
	"context"
	"fmt"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/cache/memory"
	"github.com/jcsvwinston/quark/examples/superapp/control"
	"github.com/jcsvwinston/quark/examples/superapp/domain"
	"github.com/jcsvwinston/quark/examples/superapp/engine"
	"github.com/jcsvwinston/quark/examples/superapp/recorder"
)

// qpkg es el import path del paquete raíz de Quark; las keys deben casar EXACTO
// con las que emite cmd/gen-apisurface (ver apisurface.json).
const qpkg = "github.com/jcsvwinston/quark"

// QM es la key de manifiesto de un método de *Query[T]: QM("Create") →
// "github.com/jcsvwinston/quark.(*Query[T]).Create".
func QM(method string) string { return qpkg + ".(*Query[T])." + method }

// CM es la key de un método de *Client.
func CM(method string) string { return qpkg + ".(*Client)." + method }

// QF es la key de una func/tipo/const a nivel paquete (For, ForTx, New,
// NewTenantRouter, RowLevelSecurityClient…).
func QF(name string) string { return qpkg + "." + name }

// TRM es la key de un método de *TenantRouter: TRM("Tx") →
// "github.com/jcsvwinston/quark.(*TenantRouter).Tx".
func TRM(method string) string { return qpkg + ".(*TenantRouter)." + method }

// MIG es la key de un símbolo del paquete de migraciones versionadas:
// MIG("(*Migrator).Up") → "github.com/jcsvwinston/quark/migrate.(*Migrator).Up".
func MIG(name string) string { return qpkg + "/migrate." + name }

// SRM es la key de un método de *ShardRouter: SRM("GetClient") →
// "github.com/jcsvwinston/quark.(*ShardRouter).GetClient".
func SRM(method string) string { return qpkg + ".(*ShardRouter)." + method }

// Conn re-exporta engine.Conn para que los exercisers que necesitan el driver/DSN
// del motor lo reciban sin importar el paquete engine. Lo usan los que abren
// clientes propios además del client del harness: RLSNative deriva un rol
// no-superuser y aplica CREATE POLICY; DBPerTenant abre un *Client por tenant. El
// resto lo ignora (_ Conn).
type Conn = engine.Conn

// Exerciser ejerce un área de la API: marca los símbolos que toca (rec.Mark /
// rec.Note) y aserta el resultado funcional, devolviendo error al primer fallo. El
// Conn da acceso al driver/DSN del motor a los exercisers que abren sus propios
// clientes; los que sólo usan el client del harness lo ignoran.
type Exerciser struct {
	Name string
	Fn   func(ctx context.Context, client *quark.Client, rec *recorder.Recorder, conn Conn) error
}

// EngineResult reúne el resultado por motor.
type EngineResult struct {
	Err  error              // primer error funcional (o de open/migrate)
	Leak engine.LeakReport  // del chequeo de fugas de engine.Run
	Rec  *recorder.Recorder // recorder del motor (cobertura + SQL capturado)
}

// Run corre los exercisers contra cada motor vía engine.Run (lifecycle +
// anti-fugas), instalando un recorder por motor y migrando el dominio primero.
func Run(conns map[control.Engine]engine.Conn, tol int, exercisers []Exerciser) map[control.Engine]EngineResult {
	recs := map[control.Engine]*recorder.Recorder{}
	// Caché L2 in-memory por motor: el CacheExerciser la usa vía .Cache(); para
	// el resto dormita (sólo .Cache() la consulta). memory.New() arranca una
	// goroutine cleanupLoop que Client.Close() NO cierra — la cerramos en fn,
	// antes de que engine.Run haga el leak-check.
	stores := map[control.Engine]*memory.Store{}

	newClient := func(c engine.Conn) (*quark.Client, error) {
		rec := recorder.New(c.Engine)
		recs[c.Engine] = rec
		l := quark.DefaultLimits()
		l.SafeMigrations = false
		l.MaxResults = 1_000_000
		store := memory.New()
		client, err := quark.New(c.Driver, c.DSN, append(rec.Options(), quark.WithCacheStore(store), quark.WithLimits(l))...)
		if err != nil {
			store.Close() // no leak la goroutine si New falla
			return nil, err
		}
		stores[c.Engine] = store
		return client, nil
	}

	fn := func(e control.Engine, client *quark.Client) error {
		// Cierra la goroutine de la caché antes de devolver el control a
		// engine.Run (que hace Close(client) + leak-check). defer cubre también
		// el path de error de un exerciser.
		if s := stores[e]; s != nil {
			defer s.Close()
		}
		ctx := context.Background()
		rec := recs[e]
		rec.Note(QF("New"), QF("DefaultLimits"), CM("Close")) // los usa el harness
		if err := client.Migrate(rec.Mark(ctx, CM("Migrate")), domain.AllModels()...); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
		for _, ex := range exercisers {
			if err := ex.Fn(ctx, client, rec, conns[e]); err != nil {
				return fmt.Errorf("%s: %w", ex.Name, err)
			}
		}
		return nil
	}

	leak := engine.Run(conns, tol, newClient, fn)
	out := make(map[control.Engine]EngineResult, len(leak))
	for e, lr := range leak {
		out[e] = EngineResult{Err: lr.Err, Leak: lr.Leak, Rec: recs[e]}
	}
	return out
}

// Coverage pliega la cobertura de todos los motores en un control.Invoked, listo
// para control.Manifest.Reconcile.
func Coverage(results map[control.Engine]EngineResult) control.Invoked {
	recs := make([]*recorder.Recorder, 0, len(results))
	for _, r := range results {
		if r.Rec != nil {
			recs = append(recs, r.Rec)
		}
	}
	return recorder.Collect(recs...)
}
