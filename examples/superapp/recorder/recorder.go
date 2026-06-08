// Package recorder instrumenta un Client de Quark para la superapp de
// aceptación: observa cada statement SQL y lo atribuye al símbolo de la API
// pública que lo originó, alimentando dos mecanismos del arnés:
//
//  1. COBERTURA (control.Invoked) — qué símbolos públicos se ejercieron en este
//     motor. La alimentan Mark/Note en cada call-site del exerciser; NO se
//     deriva del SQL, porque la mayoría de símbolos (builders como Where/OrderBy,
//     opciones, tipos) no emiten SQL propio. Es el numerador del gate de
//     manifiesto (control.Manifest.Reconcile).
//
//  2. CAPTURA DE SQL (Statements) — el SQL parametrizado por símbolo y motor,
//     para los golden snapshots y el oráculo de paridad cross-engine, más el
//     conteo de statements que sostiene las aserciones "cache hit = 0 SQL" y
//     "N+1 acotado" de los exercisers.
//
// El Recorder se engancha por DOS vías complementarias de Quark, porque ninguna
// sola da la tupla completa (símbolo, motor, sql, duración, filas):
//
//   - quark.Middleware (WrapExec/WrapQuery/WrapQueryRow) — recibe context, así
//     que lee el símbolo estampado por Mark: es la fuente AUTORITATIVA del par
//     (símbolo → SQL). Mide la duración y, para EXEC/QUERY_ROW, las filas
//     exactas (RowsAffected / 1).
//   - quark.QueryObserver (ObserveQuery) — NO recibe context (no ve el símbolo),
//     pero Quark lo invoca tras escanear, así que reporta el conteo de filas
//     exacto también para los SELECT multi-fila, que el middleware no puede
//     contar sin consumir el *sql.Rows del caller. Mantiene un agregado por
//     motor y completa best-effort las filas del último statement multi-fila
//     pendiente con el mismo SQL.
//
// Un Recorder es POR MOTOR. El símbolo activo viaja por context (inmune a
// carreras entre goroutines); el estado mutable (invoked/stmts/tele) está
// protegido por mu, de modo que el Recorder es seguro bajo los exercisers
// concurrentes (ha.go: pool exhaustion, deadlock retry).
package recorder

import (
	"context"
	"database/sql"
	"sync"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/examples/superapp/control"
)

// Op clasifica un statement por la primitiva de Quark que lo ejecutó. No es el
// verbo SQL (eso lo da QueryEvent.Operation en la telemetría); es la vía de
// ejecución, que determina qué se sabe de las filas en el momento del wrap.
type Op string

const (
	OpExec     Op = "exec"      // ExecContext: INSERT/UPDATE/DELETE/DDL — filas = RowsAffected
	OpQuery    Op = "query"     // QueryContext: SELECT multi-fila — filas desconocidas en el wrap
	OpQueryRow Op = "query_row" // QueryRowContext: una fila lógica
)

// Statement es un statement SQL capturado, atribuido al símbolo que lo originó.
// Rows vale -1 cuando la vía no permite conocer el conteo en el wrap (SELECT
// multi-fila no enriquecido por el observer todavía).
type Statement struct {
	Symbol string         // Symbol.Key() del símbolo activo (vacío si no se estampó)
	Engine control.Engine // motor de este Recorder
	SQL    string         // SQL parametrizado (sin valores de bind — principio de redacción F4-2)
	Op     Op
	Dur    time.Duration
	Rows   int64
	Err    error
}

// Telemetry es el agregado por motor que alimenta el observer. Da el conteo de
// filas exacto incluso para SELECT multi-fila, que el middleware no ve.
type Telemetry struct {
	Queries int            // número de eventos de observer (≈ statements reales)
	Rows    int64          // suma de filas reportadas (>0)
	ByOp    map[string]int // QueryEvent.Operation -> conteo (SELECT/EXEC/QUERY_ROW/...)
}

// Recorder instrumenta un Client de un motor concreto. Implementa
// quark.Middleware y quark.QueryObserver; las aserciones de compilación de
// abajo garantizan que las firmas siguen casando con la API real de Quark.
type Recorder struct {
	engine control.Engine

	mu      sync.Mutex
	invoked map[string]bool // Symbol.Key() -> ejercido en este motor
	stmts   []Statement
	tele    Telemetry
}

var (
	_ quark.Middleware    = (*Recorder)(nil)
	_ quark.QueryObserver = (*Recorder)(nil)
)

// New crea un Recorder para el motor e.
func New(e control.Engine) *Recorder {
	return &Recorder{engine: e, invoked: map[string]bool{}}
}

// Engine devuelve el motor de este Recorder.
func (r *Recorder) Engine() control.Engine { return r.engine }

// Options devuelve las opciones que instalan este Recorder como middleware Y
// observer en un Client. Se pasan a quark.New tal cual:
//
//	rec := recorder.New(control.SQLite)
//	client, _ := quark.New("sqlite", dsn, rec.Options()...)
func (r *Recorder) Options() []any {
	return []any{
		quark.WithMiddleware(r),
		quark.WithQueryObserver(r),
	}
}

// --- Cobertura: símbolo activo por context ---

type symbolKey struct{}

// Mark estampa symbol en ctx Y lo marca como ejercido en este motor. El ctx
// devuelto debe pasarse a quark.For/ForTx en el call-site, para que el SQL que
// dispare quede atribuido a ese símbolo:
//
//	ctx = rec.Mark(ctx, "github.com/jcsvwinston/quark.(*Query[T]).List")
//	rows, err := quark.For[domain.Account](ctx, client).List()
func (r *Recorder) Mark(ctx context.Context, symbol string) context.Context {
	r.note(symbol)
	return context.WithValue(ctx, symbolKey{}, symbol)
}

// Note marca símbolos como ejercidos SIN tocar el context. Para los que no
// emiten SQL ni se enhebran por una llamada con ctx: constructores de opciones,
// tipos puros, helpers (p.ej. quark.DefaultLimits, quark.WithReplicas).
func (r *Recorder) Note(symbols ...string) {
	for _, s := range symbols {
		r.note(s)
	}
}

func (r *Recorder) note(symbol string) {
	if symbol == "" {
		return
	}
	r.mu.Lock()
	r.invoked[symbol] = true
	r.mu.Unlock()
}

func symbolFrom(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	s, _ := ctx.Value(symbolKey{}).(string)
	return s
}

// --- quark.Middleware: captura autoritativa símbolo → SQL ---

// WrapExec registra cada ExecContext (INSERT/UPDATE/DELETE/DDL) con su símbolo y
// las filas afectadas exactas.
func (r *Recorder) WrapExec(next quark.ExecFunc) quark.ExecFunc {
	return func(ctx context.Context, exec quark.Executor, s string, a []any) (sql.Result, error) {
		sym := symbolFrom(ctx)
		start := time.Now()
		res, err := next(ctx, exec, s, a)
		dur := time.Since(start)
		rows := int64(-1)
		if err == nil && res != nil {
			if n, raErr := res.RowsAffected(); raErr == nil {
				rows = n
			}
		}
		r.record(Statement{Symbol: sym, Engine: r.engine, SQL: s, Op: OpExec, Dur: dur, Rows: rows, Err: err})
		return res, err
	}
}

// WrapQuery registra cada QueryContext (SELECT multi-fila). Las filas no se
// pueden contar aquí sin consumir el *sql.Rows que el caller necesita escanear,
// así que se dejan en -1 y ObserveQuery las completa tras el escaneo.
func (r *Recorder) WrapQuery(next quark.QueryFunc) quark.QueryFunc {
	return func(ctx context.Context, exec quark.Executor, s string, a []any) (*sql.Rows, error) {
		sym := symbolFrom(ctx)
		start := time.Now()
		rows, err := next(ctx, exec, s, a)
		dur := time.Since(start)
		r.record(Statement{Symbol: sym, Engine: r.engine, SQL: s, Op: OpQuery, Dur: dur, Rows: -1, Err: err})
		return rows, err
	}
}

// WrapQueryRow registra cada QueryRowContext (una fila lógica). *sql.Row difiere
// su error a Scan, así que Err queda nil aquí — igual que el evento de observer
// QUERY_ROW de Quark.
func (r *Recorder) WrapQueryRow(next quark.QueryRowFunc) quark.QueryRowFunc {
	return func(ctx context.Context, exec quark.Executor, s string, a []any) *sql.Row {
		sym := symbolFrom(ctx)
		start := time.Now()
		row := next(ctx, exec, s, a)
		dur := time.Since(start)
		r.record(Statement{Symbol: sym, Engine: r.engine, SQL: s, Op: OpQueryRow, Dur: dur, Rows: 1})
		return row
	}
}

func (r *Recorder) record(s Statement) {
	r.mu.Lock()
	r.stmts = append(r.stmts, s)
	r.mu.Unlock()
}

// --- quark.QueryObserver: conteo de filas exacto + agregado ---

// ObserveQuery recibe el evento que Quark emite tras escanear: aporta el conteo
// de filas exacto (incluido el de los SELECT multi-fila) al agregado y completa
// best-effort las filas del último statement multi-fila pendiente con el mismo
// SQL. La completitud es EXACTA bajo ejecución secuencial en una goroutine (el
// observer del SELECT dispara justo tras el middleware que lo registró); sólo
// aproximada si dos goroutines corren el MISMO SQL en paralelo — y aun así nunca
// atribuye mal el SÍMBOLO, que proviene del context autoritativo del middleware.
func (r *Recorder) ObserveQuery(ev quark.QueryEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.tele.Queries++
	if r.tele.ByOp == nil {
		r.tele.ByOp = map[string]int{}
	}
	r.tele.ByOp[ev.Operation]++
	if ev.Rows <= 0 {
		// ev.Rows == 0: lista vacía, SELECT (stream)/cursor o error — ninguno
		// aporta un conteo real, así que no enriquecemos (un statement de lista
		// vacía se queda en Rows: -1, inocuo para los exercisers).
		return
	}
	r.tele.Rows += ev.Rows
	for i := len(r.stmts) - 1; i >= 0; i-- {
		if r.stmts[i].Rows < 0 && r.stmts[i].SQL == ev.SQL {
			r.stmts[i].Rows = ev.Rows
			break
		}
	}
}

// --- Accesores ---

// Invoked devuelve una copia del set de símbolos ejercidos en este motor.
func (r *Recorder) Invoked() map[string]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]bool, len(r.invoked))
	for k, v := range r.invoked {
		out[k] = v
	}
	return out
}

// Statements devuelve una copia de los statements capturados, en orden.
func (r *Recorder) Statements() []Statement {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Statement, len(r.stmts))
	copy(out, r.stmts)
	return out
}

// Telemetry devuelve una copia del agregado del observer.
func (r *Recorder) Telemetry() Telemetry {
	r.mu.Lock()
	defer r.mu.Unlock()
	t := r.tele
	t.ByOp = make(map[string]int, len(r.tele.ByOp))
	for k, v := range r.tele.ByOp {
		t.ByOp[k] = v
	}
	return t
}

// Count es cuántos statements SQL se han registrado. Para las aserciones de
// conteo: tómalo antes y después de una operación de servicio y diffea (p.ej.
// un cache hit no debe incrementarlo; un Preload de N hijos debe sumar 1, no N).
func (r *Recorder) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.stmts)
}

// Reset limpia statements y telemetría CONSERVANDO la cobertura acumulada. Para
// acotar conteos entre fases de un mismo motor sin perder lo ya invocado.
func (r *Recorder) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stmts = nil
	r.tele = Telemetry{}
}

// --- Plegado a control.Invoked ---

// ContributeTo vuelca la cobertura de este motor en inv (lo crea si falta).
func (r *Recorder) ContributeTo(inv control.Invoked) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := inv[r.engine]
	if m == nil {
		m = map[string]bool{}
		inv[r.engine] = m
	}
	for k := range r.invoked {
		m[k] = true
	}
}

// Collect pliega varios recorders por-motor en un único control.Invoked listo
// para Manifest.Reconcile.
func Collect(recs ...*Recorder) control.Invoked {
	inv := control.Invoked{}
	for _, r := range recs {
		if r != nil {
			r.ContributeTo(inv)
		}
	}
	return inv
}
