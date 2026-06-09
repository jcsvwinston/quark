package exercise

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/examples/superapp/control"
	"github.com/jcsvwinston/quark/examples/superapp/recorder"
)

// rlsNativeOrder es la tabla tenant-scoped del exerciser RLSNative. La policy de
// PostgreSQL filtra sus filas comparando tenant_id contra
// current_setting('app.tenant_id'). Local al exerciser (los modelos del dominio no
// son tenant-scoped), igual que rlsNativeOrder en rls_native_postgres_test.go.
type rlsNativeOrder struct {
	ID       int64  `db:"id" pk:"true"`
	TenantID string `db:"tenant_id"`
	Status   string `db:"status"`
}

func (rlsNativeOrder) TableName() string { return "rls_native_orders" }

// El rol no-superuser y la tabla se limpian al entrar Y al salir, así que los
// nombres fijos son re-ejecutables contra un contenedor persistente
// (SUPERAPP_DSN_POSTGRES). La password en claro es intencional: rol efímero de
// test (NOSUPERUSER NOBYPASSRLS, grants mínimos) sobre un contenedor local que el
// harness crea y destruye — no es una credencial real.
const (
	rlsNativeRole = "quark_superapp_rls"
	rlsNativePass = "quark_superapp_rls_pw"
)

// RLSNATIVE ejerce la estrategia RowLevelSecurityNative (ADR-0012, F5-2): el
// aislamiento lo FUERZA el motor vía CREATE POLICY + set_config('app.tenant_id'),
// no la WHERE-injection del builder (esa es RowLevelSecurityClient, exerciser
// TENANT). La distinción es observable: bajo Native el builder NO inyecta
// `WHERE tenant_id = ?` — un `SELECT * FROM rls_native_orders` plano devuelve sólo
// las filas del tenant porque la policy del motor las filtra. Es PG-only:
//
//   - En Postgres instala un rol no-superuser + policy y aserta, vía router.Tx
//     (el camino recomendado bajo Native), que cada tenant ve sólo sus filas y que
//     un INSERT respeta el WITH CHECK de la policy.
//   - En los otros 5 motores aserta que la estrategia se rechaza con
//     quark.ErrUnsupportedFeature (capacidad desigual ≠ fallo, premisa #4 del
//     HANDOFF) — mirror de rls_native_test.go.
//
// A diferencia del exerciser RLSClient (builder-only sobre el client del harness),
// éste necesita el DSN del motor (conn): el client del harness corre como
// superuser y los superusers se saltan RLS incondicionalmente, así que el sujeto
// del aislamiento debe ser un rol no-superuser distinto, y el DDL de policy exige
// un admin client con AllowRawQueries. Por eso S5 cambió la firma de Exerciser.Fn
// para recibir el Conn.
//
// Usa SÓLO router.Tx (no el path implicit-tx de For[T] bajo Native): router.Tx
// commitea de forma síncrona y libera la conexión y la goroutine awaitDone de la
// tx, así que es determinista y no deja fugas para el leak-check del harness. El
// path implicit-tx de For[T] (nativeRLSExecutor con context.AfterFunc) queda
// cubierto por rls_native_postgres_test.go; aquí seguimos el camino que el propio
// rls_native.go marca como recomendado para cualquier operación no trivial.
var RLSNATIVE = Exerciser{Name: "tenant-rls-native", Fn: runRLSNative}

func runRLSNative(ctx context.Context, client *quark.Client, rec *recorder.Recorder, conn Conn) error {
	// Cobertura: los símbolos del path Native corren sobre clientes propios (no
	// instrumentados por el recorder del harness), así que se marcan con Note.
	rec.Note(QF("NewTenantRouter"), QF("DefaultTenantConfig"), QF("TenantConfig"),
		QF("RowLevelSecurityNative"), TRM("Tx"), TRM("ResolveTenant"))

	cfg := quark.DefaultTenantConfig()
	cfg.Strategy = quark.RowLevelSecurityNative

	// --- Motores sin RLS nativo: la estrategia se rechaza, no falla el run. ---
	// El rechazo ocurre antes de abrir tx o tocar la tabla, así que no hay fuga ni
	// hace falta tabla migrada.
	if !control.Supports(control.FeatRLSNative, conn.Engine) {
		rec.Note(QF("For"))
		cfg.BaseClient = client
		router := quark.NewTenantRouter(cfg, tenantResolver, nil)
		// ctx cancelable por consistencia con txAs; el rechazo ocurre antes de abrir
		// ninguna tx (no hay awaitDone que liberar), pero no dejamos ctx colgantes.
		ctxA, cancel := context.WithCancel(tenantCtx("ta"))
		defer cancel()
		if _, err := quark.For[rlsNativeOrder](ctxA, router).List(); !errors.Is(err, quark.ErrUnsupportedFeature) {
			return fmt.Errorf("For[T] bajo RLSNative en %s debía devolver ErrUnsupportedFeature, obtuve %v", conn.Engine, err)
		}
		if err := router.Tx(ctxA, func(*quark.Tx) error { return nil }); !errors.Is(err, quark.ErrUnsupportedFeature) {
			return fmt.Errorf("router.Tx bajo RLSNative en %s debía devolver ErrUnsupportedFeature, obtuve %v", conn.Engine, err)
		}
		return nil
	}
	rec.Note(QF("ForTx"))

	// --- Postgres: aislamiento forzado por el motor. ---
	// El rol del harness es superuser (postgres) con AllowRawQueries:false;
	// RLSNative exige lo contrario en dos clientes: un admin con raw queries para
	// el DDL de policy, y un sujeto no-superuser (derivado del DSN URL-form) bajo
	// el que la policy aplica de verdad.
	nonSuperDSN, ok := swapDSNUser(conn.DSN, rlsNativeRole, rlsNativePass)
	if !ok {
		return fmt.Errorf("RLSNative necesita un DSN Postgres URL-form para derivar un rol no-superuser; obtuve %q", conn.DSN)
	}

	admin, err := quark.New(conn.Driver, conn.DSN, quark.WithLimits(quark.Limits{
		AllowRawQueries: true,
		MaxResults:      1000,
		QueryTimeout:    30 * time.Second,
	}))
	if err != nil {
		return fmt.Errorf("admin client: %w", err)
	}
	defer admin.Close() // LIFO: corre el último — el cleanup necesita el admin abierto

	// Limpia rol+tabla de una corrida previa (incluida una que crasheó). DROP OWNED
	// y DROP ROLE exigen que el rol no tenga sesiones vivas → el non-super se cierra
	// ANTES (su defer se registra después; LIFO).
	// ctx propio (Background): el cleanup debe correr aunque el ctx del harness se
	// cancelara (hoy es Background, pero no dependemos de ello).
	cleanup := func() {
		cctx := context.Background()
		_ = admin.Exec(cctx, `DROP TABLE IF EXISTS rls_native_orders CASCADE`)
		_ = admin.Exec(cctx, `DROP OWNED BY `+rlsNativeRole+` CASCADE`)
		_ = admin.Exec(cctx, `DROP ROLE IF EXISTS `+rlsNativeRole)
	}
	cleanup()
	defer cleanup() // LIFO: corre en medio — non-super ya cerrado, admin aún abierto

	if err := admin.Migrate(ctx, &rlsNativeOrder{}); err != nil {
		return fmt.Errorf("migrate rls_native_orders: %w", err)
	}

	// Rol no-superuser + policy. FORCE ROW LEVEL SECURITY impide que el OWNER de la
	// tabla se salte la policy. La policy referencia el setting que router.Tx emite
	// vía set_config (lo instala el CLI `quark tenant install-rls-policies` en
	// producción; aquí inline para ser autocontenido).
	for _, stmt := range []string{
		`CREATE ROLE ` + rlsNativeRole + ` WITH LOGIN NOSUPERUSER NOBYPASSRLS PASSWORD '` + rlsNativePass + `'`,
		`GRANT USAGE ON SCHEMA public TO ` + rlsNativeRole,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE rls_native_orders TO ` + rlsNativeRole,
		`GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA public TO ` + rlsNativeRole,
		`ALTER TABLE rls_native_orders ENABLE ROW LEVEL SECURITY`,
		`ALTER TABLE rls_native_orders FORCE ROW LEVEL SECURITY`,
		`CREATE POLICY rls_native_orders_tenant_isolation ON rls_native_orders
			USING (tenant_id = current_setting('app.tenant_id', true)::text)
			WITH CHECK (tenant_id = current_setting('app.tenant_id', true)::text)`,
	} {
		if err := admin.Exec(ctx, stmt); err != nil {
			return fmt.Errorf("policy DDL %q: %w", firstLine(stmt), err)
		}
	}

	nonSuper, err := quark.New(conn.Driver, nonSuperDSN, quark.WithLimits(quark.Limits{
		AllowRawQueries: false,
		MaxResults:      1000,
		QueryTimeout:    30 * time.Second,
	}))
	if err != nil {
		return fmt.Errorf("non-super client: %w", err)
	}
	defer nonSuper.Close() // LIFO: corre el primero — libera las sesiones del rol antes del DROP

	cfg.BaseClient = nonSuper
	router := quark.NewTenantRouter(cfg, tenantResolver, nil)

	// Seed: cada INSERT corre dentro de router.Tx bajo su set_config y satisface el
	// WITH CHECK de la policy.
	if err := txAs(router, "ta", func(c context.Context, tx *quark.Tx) error {
		return createRLSNativeRows(c, tx, "ta", "pending", "paid", "shipped")
	}); err != nil {
		return fmt.Errorf("seed ta: %w", err)
	}
	if err := txAs(router, "tb", func(c context.Context, tx *quark.Tx) error {
		return createRLSNativeRows(c, tx, "tb", "pending", "paid")
	}); err != nil {
		return fmt.Errorf("seed tb: %w", err)
	}

	// Aislamiento: el motor filtra. ta ve 3, tb ve 2, y nunca filas ajenas — aunque
	// el SELECT de ForTx sea `… FROM rls_native_orders` sin predicado de tenant (la
	// diferencia clave con RLSClient: aquí el aislamiento NO viene del builder).
	if err := assertRLSNativeRows(router, "ta", 3); err != nil {
		return fmt.Errorf("aislamiento ta: %w", err)
	}
	if err := assertRLSNativeRows(router, "tb", 2); err != nil {
		return fmt.Errorf("aislamiento tb: %w", err)
	}

	// Write isolation: un INSERT de ta no afecta a tb, y la PK del RETURNING se
	// rellena (cubre el path de escritura bajo set_config).
	if err := txAs(router, "ta", func(c context.Context, tx *quark.Tx) error {
		row := rlsNativeOrder{TenantID: "ta", Status: "delivered"}
		if err := quark.ForTx[rlsNativeOrder](c, tx).Create(&row); err != nil {
			return err
		}
		if row.ID == 0 {
			return errors.New("Create bajo Native no rellenó la PK del RETURNING")
		}
		return nil
	}); err != nil {
		return fmt.Errorf("create ta: %w", err)
	}
	if err := assertRLSNativeRows(router, "ta", 4); err != nil {
		return fmt.Errorf("tras create ta: %w", err)
	}
	if err := assertRLSNativeRows(router, "tb", 2); err != nil {
		return fmt.Errorf("tb no debe verse afectado por el insert de ta: %w", err)
	}
	return nil
}

// txAs corre fn dentro de router.Tx para el tenant tid, con un ctx CANCELABLE que
// se cancela al volver. Bajo RowLevelSecurityNative, router.Tx abre una tx en el
// BaseClient y emite set_config('app.tenant_id', tid) como primer statement; el
// commit es síncrono. El cancel libera la goroutine awaitDone que database/sql
// arranca por tx (espera en ctx.Done()): sin un ctx cancelable se quedaría parada
// para siempre y el leak-check del harness la contaría como fuga.
func txAs(router *quark.TenantRouter, tid string, fn func(ctx context.Context, tx *quark.Tx) error) error {
	ctx, cancel := context.WithCancel(tenantCtx(tid))
	defer cancel()
	return router.Tx(ctx, func(tx *quark.Tx) error { return fn(ctx, tx) })
}

func createRLSNativeRows(ctx context.Context, tx *quark.Tx, tid string, statuses ...string) error {
	for _, s := range statuses {
		row := rlsNativeOrder{TenantID: tid, Status: s}
		if err := quark.ForTx[rlsNativeOrder](ctx, tx).Create(&row); err != nil {
			return err
		}
	}
	return nil
}

// assertRLSNativeRows lista dentro de router.Tx y verifica el conteo exacto + que
// ninguna fila pertenezca a otro tenant (la policy del motor debe haber filtrado).
func assertRLSNativeRows(router *quark.TenantRouter, tid string, want int) error {
	var got []rlsNativeOrder
	if err := txAs(router, tid, func(c context.Context, tx *quark.Tx) error {
		var inner error
		got, inner = quark.ForTx[rlsNativeOrder](c, tx).List()
		return inner
	}); err != nil {
		return err
	}
	if len(got) != want {
		return fmt.Errorf("esperaba %d filas para %s, obtuve %d", want, tid, len(got))
	}
	for _, d := range got {
		if d.TenantID != tid {
			return fmt.Errorf("FUGA: %s vio una fila del tenant %q", tid, d.TenantID)
		}
	}
	return nil
}

// swapDSNUser reescribe el user/password de un DSN Postgres URL-form. Mirror de
// swapPGUser (rls_native_postgres_test.go): el rol del harness es superuser y los
// superusers se saltan RLS, así que el sujeto del aislamiento debe correr bajo un
// rol no-superuser. Devuelve (dsn, false) si el DSN no es URL-form.
func swapDSNUser(dsn, user, password string) (string, bool) {
	if !strings.HasPrefix(dsn, "postgres://") && !strings.HasPrefix(dsn, "postgresql://") {
		return dsn, false
	}
	u, err := url.Parse(dsn)
	if err != nil {
		return dsn, false
	}
	u.User = url.UserPassword(user, password)
	return u.String(), true
}

// firstLine recorta un statement multilínea para los mensajes de error.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return strings.TrimSpace(s[:i]) + " …"
	}
	return s
}
