package exercise

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/examples/superapp/control"
	"github.com/jcsvwinston/quark/examples/superapp/recorder"
)

// dbtDoc vive en la base de datos PROPIA de cada tenant — no lleva tenant_id:
// bajo DatabasePerTenant el aislamiento es físico (pools/DSNs distintos), no un
// predicado ni un schema.
type dbtDoc struct {
	ID    int64  `db:"id" pk:"true"`
	Title string `db:"title"`
}

func (dbtDoc) TableName() string { return "dbt_docs" }

// DBPERTENANT ejerce la estrategia DatabasePerTenant (ADR-0007): el router abre
// un *Client por tenant vía factory y los cachea en un LRU. Se asertan las dos
// garantías del contrato:
//
//  1. Aislamiento físico — cada tenant ve sólo su base (counts exactos, sin
//     columna tenant_id de por medio), y los datos sobreviven al ciclo del pool
//     (evicción → re-open → siguen ahí).
//  2. El LRU evicta — con MaxCachedPools=1 y 2 tenants alternados, el factory se
//     invoca en cada cambio de tenant (4 veces, no 2) y ActiveTenants() refleja
//     sólo el pool vivo. La evicción cierra el client evictado (async); el
//     exerciser cierra además todos los que abrió antes del leak-check.
//
// Aprovisionamiento por motor (FeatDBPerTenantProvision): SQLite = un fichero
// por tenant; PG/MySQL/MariaDB/MSSQL = CREATE DATABASE vía un admin client
// (client.Exec va directo a db.ExecContext, sin tx — PG exige CREATE DATABASE
// fuera de tx) + rewrite del DSN (tenant_dsn.go). Oracle se salta documentado:
// una database por tenant ahí es un PDB, fuera del alcance del harness.
var DBPERTENANT = Exerciser{Name: "tenant-db-per", Fn: runDBPerTenant}

func runDBPerTenant(ctx context.Context, client *quark.Client, rec *recorder.Recorder, conn Conn) error {
	rec.Note(QF("DatabasePerTenant"), QF("NewTenantRouter"), QF("DefaultTenantConfig"),
		QF("For"), TRM("GetClient"), TRM("ActiveTenants"))

	if !control.Supports(control.FeatDBPerTenantProvision, conn.Engine) {
		return nil // Oracle: skip documentado en capability.go (PDB fuera de alcance)
	}

	const ta, tb = "ta", "tb"
	dbName := func(tid string) string { return "superapp_dbt_" + tid }

	// --- Aprovisionamiento + cleanup re-ejecutable (nombres fijos) ---
	if conn.Engine == control.SQLite {
		// Un fichero por tenant, derivado del fichero base del harness.
		for _, tid := range []string{ta, tb} {
			dsn, _ := tenantDBDSN(conn.Engine, conn.DSN, dbName(tid))
			_ = os.Remove(dsn) // limpia una corrida previa
			defer os.Remove(dsn)
		}
	} else {
		admin, err := quark.New(conn.Driver, conn.DSN, quark.WithLimits(quark.Limits{
			AllowRawQueries: true,
			MaxResults:      1000,
			QueryTimeout:    30 * time.Second,
		}))
		if err != nil {
			return fmt.Errorf("admin client: %w", err)
		}
		defer admin.Close() // LIFO: el último — el cleanup necesita el admin abierto

		// PG/MSSQL rechazan DROP DATABASE con sesiones vivas: los clients de
		// tenant se cierran ANTES (su defer se registra después; LIFO).
		cleanup := func() {
			cctx := context.Background()
			for _, tid := range []string{ta, tb} {
				if stmt, err := dropTenantDBSQL(conn.Engine, dbName(tid)); err == nil {
					_ = admin.Exec(cctx, stmt)
				}
			}
		}
		cleanup()
		defer cleanup()

		for _, tid := range []string{ta, tb} {
			stmt, err := createTenantDBSQL(conn.Engine, dbName(tid))
			if err != nil {
				return err
			}
			if err := admin.Exec(ctx, stmt); err != nil {
				return fmt.Errorf("create database %s: %w", dbName(tid), err)
			}
		}
	}

	// --- Factory instrumentado + tracking para el leak-check ---
	// El router NO tiene Close(): el exerciser rastrea cada client que el factory
	// abre y los cierra todos al salir (doble-Close con el de la evicción del LRU
	// es inocuo). Registrado tras el cleanup → corre antes (LIFO).
	var opened []*quark.Client
	defer func() {
		for _, c := range opened {
			_ = c.Close()
		}
	}()
	factoryCalls := 0
	factory := func(tid string) (*quark.Client, error) {
		factoryCalls++
		dsn, err := tenantDBDSN(conn.Engine, conn.DSN, dbName(tid))
		if err != nil {
			return nil, err
		}
		l := quark.DefaultLimits()
		l.SafeMigrations = false
		c, err := quark.New(conn.Driver, dsn, append(rec.Options(), quark.WithLimits(l))...)
		if err != nil {
			return nil, err
		}
		opened = append(opened, c)
		// Idempotente: en un re-open tras evicción la tabla ya existe.
		if err := c.Migrate(ctx, &dbtDoc{}); err != nil {
			return nil, fmt.Errorf("migrate db de %s: %w", tid, err)
		}
		return c, nil
	}

	cfg := quark.DefaultTenantConfig()
	cfg.Strategy = quark.DatabasePerTenant
	cfg.MaxCachedPools = 1 // fuerza la evicción LRU con 2 tenants alternados
	router := quark.NewTenantRouter(cfg, tenantResolver, factory)

	ctxA, ctxB := tenantCtx(ta), tenantCtx(tb)

	// 1) Seed ta (factory #1) y tb (factory #2 — evicta el pool de ta).
	for _, title := range []string{"dbt-a-1", "dbt-a-2"} {
		if err := quark.For[dbtDoc](rec.Mark(ctxA, QM("Create")), router).Create(&dbtDoc{Title: title}); err != nil {
			return fmt.Errorf("create ta %q: %w", title, err)
		}
	}
	if err := quark.For[dbtDoc](rec.Mark(ctxB, QM("Create")), router).Create(&dbtDoc{Title: "dbt-b-1"}); err != nil {
		return fmt.Errorf("create tb: %w", err)
	}

	// 2) ActiveTenants refleja sólo el pool vivo (MaxCachedPools=1 → el de tb).
	if act := router.ActiveTenants(); len(act) != 1 || act[0] != tb {
		return fmt.Errorf("ActiveTenants = %v, esperaba [%s] (LRU de tamaño 1)", act, tb)
	}

	// 3) Volver a ta re-invoca el factory (evicción real, no cache) y los datos
	// persisten en SU base: aislamiento físico que sobrevive al ciclo del pool.
	aDocs, err := quark.For[dbtDoc](rec.Mark(ctxA, QM("List")), router).List()
	if err != nil {
		return fmt.Errorf("list ta: %w", err)
	}
	if len(aDocs) != 2 {
		return fmt.Errorf("ta: esperaba 2 filas en su base, obtuve %d", len(aDocs))
	}
	for _, d := range aDocs {
		if d.Title == "dbt-b-1" {
			return fmt.Errorf("FUGA física: la base de ta contiene la fila de tb (%+v)", d)
		}
	}
	bDocs, err := quark.For[dbtDoc](rec.Mark(ctxB, QM("List")), router).List()
	if err != nil {
		return fmt.Errorf("list tb: %w", err)
	}
	if len(bDocs) != 1 || bDocs[0].Title != "dbt-b-1" {
		return fmt.Errorf("tb: esperaba exactamente su fila dbt-b-1, obtuve %+v", bDocs)
	}

	// 4) El factory corrió una vez por cambio de tenant (ta,tb,ta,tb = 4): con un
	// LRU mayor habrían sido 2 — esto demuestra que la evicción ocurrió.
	if factoryCalls != 4 {
		return fmt.Errorf("factory invocado %d veces, esperaba 4 (evicción LRU con MaxCachedPools=1)", factoryCalls)
	}
	return nil
}
