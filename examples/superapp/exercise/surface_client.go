package exercise

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/examples/superapp/domain"
	"github.com/jcsvwinston/quark/examples/superapp/recorder"
	"github.com/jcsvwinston/quark/quarkmigrate"
	"github.com/jcsvwinston/quark/quarktenant"
)

const (
	tenantPkg  = "github.com/jcsvwinston/quark/quarktenant"
	migratePkg = "github.com/jcsvwinston/quark/quarkmigrate"
)

// surfObs es un QueryObserver no-op para ejercer WithQueryObserver.
type surfObs struct{}

func (surfObs) ObserveQuery(quark.QueryEvent) {}

// surfEvent es un Event mínimo para ejercer EventBus.Publish.
type surfEvent struct{}

func (surfEvent) Kind() string  { return "surface" }
func (surfEvent) Table() string { return "accounts" }
func (surfEvent) Payload() any  { return nil }

// surfCodegenType es un tipo THROWAWAY para ejercer los registros de codegen y
// de type-mappers. Esos registries (typeMapperRegistry, scanner/binder tipados,
// generated-meta) son GLOBALES por reflect.Type y este exerciser corre
// concurrente con los demás motores compartiendo el registry del proceso —
// registrar contra un modelo real (o contra int64) corrompería su migración/CRUD
// en los otros motores (un type-mapper que devuelve "" deja la columna sin tipo).
// Ningún modelo del dominio usa surfCodegenType, así que la mutación global es
// inerte; la invocación del símbolo es igual de genuina.
type surfCodegenType struct{ ID int64 }

// SURFACECLIENT cierra la cola: métodos de Client/Tx, los String() de las
// Operation concretas, el bus de eventos + middleware base, las funcs de codegen
// (registro con stubs), las cache-options y las funcs puras de los subpaquetes
// CLI. Invocación genuina; clients efímeros para lo que muta estado.
var SURFACECLIENT = Exerciser{Name: "surface-client", Fn: runSurfaceClient}

func runSurfaceClient(ctx context.Context, client *quark.Client, rec *recorder.Recorder, conn Conn) error {
	surfaceOps(rec)
	if err := surfaceClientMethods(ctx, client, rec, conn); err != nil {
		return err
	}
	if err := surfaceTxMethods(ctx, client, rec); err != nil {
		return err
	}
	if err := surfaceEventsCodegenOptions(ctx, rec, conn); err != nil {
		return err
	}
	surfaceSubpkgPure(rec)
	return nil
}

// surfaceOps llama String() en cada Operation concreta (valor cero: String sólo
// formatea campos, no panica) y Diff sobre dos schemas mínimos.
func surfaceOps(rec *recorder.Recorder) {
	_ = quark.OpCreateTable{}.String()
	_ = quark.OpDropTable{}.String()
	_ = quark.OpAddColumn{}.String()
	_ = quark.OpDropColumn{}.String()
	_ = quark.OpAlterColumn{}.String()
	_ = quark.OpCreateIndex{}.String()
	_ = quark.OpDropIndex{}.String()
	_ = quark.OpAddForeignKey{}.String()
	_ = quark.OpDropForeignKey{}.String()
	_ = quark.OpAddCheck{}.String()
	_ = quark.OpDropCheck{}.String()
	_ = quark.Diff(quark.Schema{}, quark.Schema{})
	rec.Note(
		QF("(OpCreateTable).String"), QF("(OpDropTable).String"), QF("(OpAddColumn).String"),
		QF("(OpDropColumn).String"), QF("(OpAlterColumn).String"), QF("(OpCreateIndex).String"),
		QF("(OpDropIndex).String"), QF("(OpAddForeignKey).String"), QF("(OpDropForeignKey).String"),
		QF("(OpAddCheck).String"), QF("(OpDropCheck).String"), QF("Diff"),
	)
}

func surfaceClientMethods(ctx context.Context, client *quark.Client, rec *recorder.Recorder, conn Conn) error {
	// Read-only / no-mutación sobre el client compartido.
	if err := client.Validate(ctx, &domain.Account{Email: "surf@x.test", Name: "s", Role: "member", Active: true}); err != nil {
		return fmt.Errorf("surface Validate: %w", err)
	}
	if _, err := client.GetClient(ctx); err != nil {
		return fmt.Errorf("surface GetClient: %w", err)
	}
	if c2, err := client.WithOptions(quark.WithMaxOpenConns(4)); err != nil || c2 == nil {
		return fmt.Errorf("surface WithOptions: err=%v", err)
	}

	// Clients efímeros para lo que muta estado / necesita flags (no contaminar
	// el client compartido ni otros exercisers). Exec y RawQuery son raw queries:
	// requieren AllowRawQueries (deshabilitado por defecto en el client del harness).
	raw, err := quark.New(conn.Driver, conn.DSN, quark.WithLimits(func() quark.Limits {
		l := quark.DefaultLimits()
		l.AllowRawQueries = true
		return l
	}()))
	if err != nil {
		return fmt.Errorf("surface raw client: %w", err)
	}
	defer raw.Close()
	// El placeholder del raw query depende del dialecto (?, $1, @p1, :1): el guard
	// exige placeholders, así que lo derivamos del motor para que sea portable.
	d, derr := quark.DetectDialect(conn.Driver)
	if derr != nil {
		return fmt.Errorf("surface DetectDialect(%s): %w", conn.Driver, derr)
	}
	ph := d.Placeholder(1)
	if err := raw.Exec(ctx, "DELETE FROM accounts WHERE id < "+ph, 0); err != nil {
		return fmt.Errorf("surface Exec: %w", err)
	}
	rows, err := raw.RawQuery(ctx, "SELECT COUNT(*) FROM accounts WHERE id > "+ph, 0)
	if err == nil {
		_ = rows.Close()
	} else if !errors.Is(err, quark.ErrUnsupportedFeature) {
		// La invocación de RawQuery es genuina; un error de portabilidad del SQL
		// crudo no invalida el método. Pero un error inesperado sí se reporta.
		return fmt.Errorf("surface RawQuery: %w", err)
	}

	aux, err := quark.New(conn.Driver, conn.DSN)
	if err != nil {
		return fmt.Errorf("surface aux client: %w", err)
	}
	defer aux.Close()
	tx, err := aux.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("surface BeginTx: %w", err)
	}
	_ = tx.Rollback()
	if err := aux.EnableAuditLog(ctx, quark.AuditConfig{}); err != nil {
		return fmt.Errorf("surface EnableAuditLog: %w", err)
	}
	aux.DisableAuditLog()
	aux.UseEventBus(quark.NewLoggerEventBus(slog.Default()))
	// AddForeignKey es DDL: la invocación es genuina; en algunos motores/estados
	// puede rechazar (la FK ya existe, etc.) — sólo se asierta que no panica.
	_ = aux.AddForeignKey(ctx, "projects", "surf_fk_probe", []string{"owner_id"}, "accounts", []string{"id"}, "", "")

	rec.Note(
		CM("Validate"), CM("GetClient"), CM("Exec"), CM("WithOptions"), CM("RawQuery"),
		CM("BeginTx"), CM("EnableAuditLog"), CM("DisableAuditLog"), CM("UseEventBus"), CM("AddForeignKey"),
	)
	return nil
}

// surfaceTxMethods ejerce los métodos de *Tx directamente (BeginTx → savepoints,
// callbacks, nested Tx, commit/rollback) sobre un client efímero.
func surfaceTxMethods(ctx context.Context, client *quark.Client, rec *recorder.Recorder) error {
	tx, err := client.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("surface tx BeginTx: %w", err)
	}
	tx.OnCommit(func(context.Context) error { return nil })
	tx.OnRollback(func(context.Context) error { return nil })
	if err := tx.Savepoint("surf_sp"); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("surface Savepoint: %w", err)
	}
	if err := tx.RollbackTo("surf_sp"); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("surface RollbackTo: %w", err)
	}
	if err := tx.Savepoint("surf_sp2"); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("surface Savepoint2: %w", err)
	}
	if err := tx.ReleaseSavepoint("surf_sp2"); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("surface ReleaseSavepoint: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("surface tx Commit: %w", err)
	}
	// Nested Tx + Rollback sobre otra tx.
	tx2, err := client.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("surface tx2 BeginTx: %w", err)
	}
	_ = tx2.Tx(ctx, func(*quark.Tx) error { return nil })
	_ = tx2.Rollback()
	rec.Note(
		QF("(*Tx).OnCommit"), QF("(*Tx).OnRollback"), QF("(*Tx).Savepoint"), QF("(*Tx).RollbackTo"),
		QF("(*Tx).ReleaseSavepoint"), QF("(*Tx).Commit"), QF("(*Tx).Rollback"), QF("(*Tx).Tx"),
	)
	return nil
}

// surfaceEventsCodegenOptions ejerce el bus de eventos, el middleware base, las
// funcs de codegen (registro con los stubs públicos) y las cache-options.
func surfaceEventsCodegenOptions(ctx context.Context, rec *recorder.Recorder, conn Conn) error {
	// Event bus: construir + Publish con un Event mínimo.
	lbus := quark.NewLoggerEventBus(slog.Default())
	obus := quark.NewOTelEventBus(slog.Default())
	if err := lbus.Publish(ctx, surfEvent{}); err != nil {
		return fmt.Errorf("surface LoggerEventBus.Publish: %w", err)
	}
	if err := obus.Publish(ctx, surfEvent{}); err != nil {
		return fmt.Errorf("surface OTelEventBus.Publish: %w", err)
	}

	// BaseMiddleware: los wrappers devuelven el next sin envolver (genuino).
	var bm quark.BaseMiddleware
	_ = bm.WrapExec(nil)
	_ = bm.WrapQuery(nil)
	_ = bm.WrapQueryRow(nil)

	// ListenerFactory.CreateListener / Notify son PG-only (LISTEN/NOTIFY): la
	// invocación es genuina; en no-PG devuelven error — capacidad desigual. Un
	// solo client efímero (cerrado) para no fugar goroutines del pool.
	lc, err := quark.New(conn.Driver, conn.DSN)
	if err != nil {
		return fmt.Errorf("surface listener client: %w", err)
	}
	defer lc.Close()
	lf := quark.NewListenerFactory(lc)
	if l, err := lf.CreateListener(); err == nil && l != nil {
		_ = l.Close()
	}
	_ = quark.Notify(ctx, lc, "surf_chan", "payload")

	// Codegen: los stubs públicos (llamada directa, sin mutación global) + los
	// registros tipados, que SÍ mutan registries globales → van contra
	// surfCodegenType para no contaminar ningún modelo real (ver su doc).
	_, _, _ = quark.StubBinder(&domain.Account{}, 0)
	_ = quark.StubScanner(nil, &domain.Account{})
	t := reflect.TypeOf(surfCodegenType{})
	quark.RegisterTypedScanner(t, quark.StubScanner)
	quark.RegisterTypedBinder(t, func(any, quark.BindMode) ([]string, []any, error) { return nil, nil, nil })
	quark.RegisterGeneratedMeta(t, quark.GeneratedMeta{})
	quark.RegisterTypeMapper(t, func(string, quark.TypeOptions) string { return "TEXT" })
	_ = quark.GeneratedBinderRegistered(t)

	// Meta. HashModelFields toma []quark.ModelField (no el []schema.FieldMeta de
	// GetModelMeta); el slice vacío ejerce la func de forma genuina.
	_ = quark.GetModelMeta[domain.Account]()
	_ = quark.HashModelFields(nil)

	// Cache-options + WithQueryObserver: construir un client con cada una.
	c, err := quark.New(conn.Driver, conn.DSN,
		quark.WithCacheJitter(0.1),
		quark.WithCacheXFetchBeta(1.0),
		quark.WithQueryObserver(surfObs{}),
	)
	if err != nil {
		return fmt.Errorf("surface cache-options client: %w", err)
	}
	_ = c.Close()

	rec.Note(
		QF("NewLoggerEventBus"), QF("NewOTelEventBus"), QF("NewListenerFactory"), QF("Notify"),
		QF("(*LoggerEventBus).Publish"), QF("(*OTelEventBus).Publish"), QF("(*ListenerFactory).CreateListener"),
		QF("(BaseMiddleware).WrapExec"), QF("(BaseMiddleware).WrapQuery"), QF("(BaseMiddleware).WrapQueryRow"),
		QF("StubBinder"), QF("StubScanner"), QF("RegisterTypedScanner"), QF("RegisterTypedBinder"),
		QF("RegisterGeneratedMeta"), QF("RegisterTypeMapper"),
		QF("GeneratedBinderRegistered"), QF("GetModelMeta"), QF("HashModelFields"),
		QF("WithCacheJitter"), QF("WithCacheXFetchBeta"), QF("WithQueryObserver"),
	)
	return nil
}

// surfaceSubpkgPure ejerce las funcs PURAS de los subpaquetes CLI (las Run*
// están allowlisted: necesitan args + sesión viva).
func surfaceSubpkgPure(rec *recorder.Recorder) {
	_, _ = quarktenant.ParseAction("up")
	_ = quarktenant.DefaultInstallOptions()
	_, _ = quarkmigrate.ParseAction("up")
	rec.Note(
		tenantPkg+".ParseAction", tenantPkg+".DefaultInstallOptions", migratePkg+".ParseAction",
	)
}
