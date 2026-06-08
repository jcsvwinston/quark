package engine

import (
	"fmt"
	"runtime"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/examples/superapp/control"
)

// Settle deja que las goroutines en cierre terminen antes de contar (GC + pausa).
func Settle() {
	for i := 0; i < 3; i++ {
		runtime.GC()
		time.Sleep(50 * time.Millisecond)
	}
}

// Goroutines cuenta las goroutines vivas tras estabilizar.
func Goroutines() int { Settle(); return runtime.NumGoroutine() }

// LeakReport compara goroutines antes/después de la corrida y el estado del pool
// DESPUÉS de cerrar el Client. Un motor limpio deja el pool a 0 y no acumula
// goroutines (más allá de la tolerancia, que cubre goroutines residuales de
// algunos drivers).
type LeakReport struct {
	Engine           control.Engine `json:"engine"`
	GoroutinesBefore int            `json:"goroutines_before"`
	GoroutinesAfter  int            `json:"goroutines_after"`
	Tolerance        int            `json:"tolerance"`
	PoolInUse        int            `json:"pool_in_use"` // conexiones en uso tras Close (debe ser 0)
	PoolOpen         int            `json:"pool_open"`   // conexiones abiertas tras Close (debe ser 0)
}

func (r LeakReport) GoroutinesLeaked() bool {
	return r.GoroutinesAfter > r.GoroutinesBefore+r.Tolerance
}
func (r LeakReport) PoolLeaked() bool { return r.PoolInUse != 0 || r.PoolOpen != 0 }
func (r LeakReport) OK() bool         { return !r.GoroutinesLeaked() && !r.PoolLeaked() }

func (r LeakReport) String() string {
	verdict := "OK"
	if !r.OK() {
		verdict = "FUGA"
	}
	return fmt.Sprintf("%s: goroutines %d→%d (tol %d), pool inUse=%d open=%d → %s",
		r.Engine, r.GoroutinesBefore, r.GoroutinesAfter, r.Tolerance, r.PoolInUse, r.PoolOpen, verdict)
}

// Result reúne el error funcional de fn y el report de fugas de un motor.
type Result struct {
	Err  error
	Leak LeakReport
}

// Run abre un Client por motor (vía newClient), corre fn, lo cierra, y verifica
// fugas: goroutines estables (dentro de tol) y pool a 0 tras Close. El pool se
// lee DESPUÉS de Close — ahí InUse/OpenConnections deben ser 0. Cada motor
// captura su propia línea base de goroutines, así que la acumulación entre
// motores no falsea el resultado.
func Run(conns map[control.Engine]Conn, tol int, newClient func(Conn) (*quark.Client, error), fn func(control.Engine, *quark.Client) error) map[control.Engine]Result {
	out := make(map[control.Engine]Result, len(conns))
	for e, c := range conns {
		before := Goroutines()
		res := Result{Leak: LeakReport{Engine: e, GoroutinesBefore: before, Tolerance: tol}}

		client, err := newClient(c)
		if err != nil {
			res.Err = fmt.Errorf("open %s: %w", e, err)
			res.Leak.GoroutinesAfter = Goroutines()
			out[e] = res
			continue
		}
		raw := client.Raw()
		res.Err = fn(e, client)
		_ = client.Close()

		// Settle UNA vez tras Close, ANTES de leer el pool: deja que las
		// conexiones en cierre asíncrono y las goroutines de fondo del driver
		// terminen. Sin esto, Stats() puede ver un InUse>0 transitorio (p.ej. si
		// fn devolvió error en mitad de una query) y dar un falso positivo —
		// asimétrico con el check de goroutines, que sí estabilizaba.
		Settle()
		st := raw.Stats() // tras Close + settle: InUse/OpenConnections deben ser 0
		res.Leak.PoolInUse = st.InUse
		res.Leak.PoolOpen = st.OpenConnections
		res.Leak.GoroutinesAfter = runtime.NumGoroutine() // ya estabilizado por Settle()
		out[e] = res
	}
	return out
}
