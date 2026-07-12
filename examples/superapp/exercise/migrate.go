package exercise

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/examples/superapp/control"
	"github.com/jcsvwinston/quark/examples/superapp/domain"
	"github.com/jcsvwinston/quark/examples/superapp/recorder"
	"github.com/jcsvwinston/quark/migrate"
)

// migrateLedger es el modelo EXTRA del exerciser — no está en domain.AllModels,
// así que su tabla aparece/desaparece vía PlanMigration/ApplyPlan y eso es
// exactamente lo que prueba el diff (OpCreateTable al añadirlo al desired,
// OpDropTable al quitarlo).
type migrateLedger struct {
	ID     int64  `db:"id" pk:"true"`
	Ref    string `db:"ref" quark:"not_null"`
	Amount int64  `db:"amount" default:"0"`
}

func (migrateLedger) TableName() string { return "migrate_ledgers" }

// migrateLedgerV2 es la misma tabla con una columna más: el delta que Sync debe
// añadir (V1→V2) y luego dropear al volver a V1 (SafeMigrations=false en el
// suite).
type migrateLedgerV2 struct {
	ID     int64  `db:"id" pk:"true"`
	Ref    string `db:"ref" quark:"not_null"`
	Amount int64  `db:"amount" default:"0"`
	// Nullable: las filas existentes quedan NULL tras el ADD COLUMN (Sync no
	// rellena), y un string plano rompería el Scan del First.
	Note quark.Nullable[string] `db:"note"`
}

func (migrateLedgerV2) TableName() string { return "migrate_ledgers" }

// migrateVNote es la tabla que crea/destruye el ciclo de migraciones
// VERSIONADAS (paquete migrate, registry + quark_migrations).
type migrateVNote struct {
	ID   int64  `db:"id" pk:"true"`
	Body string `db:"body" quark:"not_null"`
}

func (migrateVNote) TableName() string { return "migrate_v_notes" }

// IDs de las migraciones versionadas y nombre del backfill. Constantes para que
// el pre-clean (converge) pueda borrar el estado que un run anterior abortado
// hubiera dejado en quark_migrations / quark_backfill_state.
const (
	migVNotesID   = "0001_superapp_v_notes"
	migVSeedID    = "0002_superapp_v_notes_seed"
	backfillName  = "superapp_migrate_ledger_backfill"
	ledgerTable   = "migrate_ledgers"
	vNotesTable   = "migrate_v_notes"
	migLockName   = "superapp_migrate_lock"
	syncedNoteVal = "synced"
)

// MIGRATE ejerce el área de migraciones: el round-trip Migrate→PlanMigration
// vacío (el invariante que BB-11 rompía), el ciclo schema-as-code completo
// (diff detecta la tabla faltante → ApplyPlan la CREA con su PK [regresión
// F3-2-pk] → ops de columna → drop de tabla), el contrato "índice manual no
// genera drops" de mergeNonColumnSurface, el registry per-Client (F3-7),
// Sync (dry-run, add y drop de columna), Backfill con resume tras fallo
// (F3-6), el lock de migración distribuido por capability (F3-1/ADR-0018),
// y el ciclo completo de migraciones versionadas (paquete migrate:
// Init/UpDryRun/Up/GetApplied/Down).
// Deja la BD como la encontró (cleanup vía OpDropTable real) y es idempotente
// entre runs (converge + limpieza de estado al entrar).
var MIGRATE = Exerciser{Name: "migrate", Fn: func(ctx context.Context, client *quark.Client, rec *recorder.Recorder, conn Conn) error {
	eng := rec.Engine()

	// --- 0. Converge: sana restos de un run anterior abortado. -------------
	// ApplyPlan(plan-de-drift) devuelve la BD al estado canónico (dropea un
	// migrate_ledgers/migrate_v_notes huérfano, columnas sobrantes…); las dos
	// DELETE limpian el estado persistente que NO sale en planes (las tablas
	// quark_* están filtradas de la introspección). Sus errores se toleran:
	// en una BD fresca esas tablas aún no existen. El plan se aplica CRUDO:
	// con los findings A (task_20d5f912) y B (task_b03f2155) cerrados, un
	// plan post-Migrate limpio es vacío de verdad — el diff ya no propone
	// drops de join tables m2m ni alters cosméticos.
	p0, err := client.PlanMigration(rec.Mark(ctx, CM("PlanMigration")), domain.AllModels()...)
	if err != nil {
		return fmt.Errorf("plan converge: %w", err)
	}
	if !p0.IsEmpty() {
		if err := client.ApplyPlan(rec.Mark(ctx, CM("ApplyPlan")), p0); err != nil {
			return fmt.Errorf("converge (restos de un run anterior): %w", err)
		}
	}
	rec.Note(CM("Dialect"), CM("Raw"))
	_, _ = client.Raw().ExecContext(ctx,
		"DELETE FROM quark_backfill_state WHERE name = "+client.Dialect().Placeholder(1), backfillName)
	_, _ = client.Raw().ExecContext(ctx, fmt.Sprintf(
		"DELETE FROM quark_migrations WHERE id IN (%s, %s)",
		client.Dialect().Placeholder(1), client.Dialect().Placeholder(2)), migVNotesID, migVSeedID)

	// --- 1. Round-trip: tras Migrate (el suite ya migró), plan VACÍO. ------
	// IsEmpty() a secas — el invariante documentado ("did anything drift?").
	// Era módulo-drift-conocido mientras los findings A/B estuvieron vivos;
	// con sus fixes en core el assert quedó estricto.
	p1, err := client.PlanMigration(ctx, domain.AllModels()...)
	if err != nil {
		return fmt.Errorf("plan round-trip: %w", err)
	}
	rec.Note(QF("(Plan).IsEmpty"), QF("(Plan).String"), QF("Plan"))
	if !p1.IsEmpty() {
		return fmt.Errorf("round-trip roto: el plan post-Migrate no es vacío:\n%s", p1.String())
	}
	// El render del plan vacío es el literal documentado.
	if got := p1.String(); got != "(no changes)" {
		return fmt.Errorf("Plan.String() de plan vacío = %q, esperaba \"(no changes)\"", got)
	}

	// --- 2. Plan→ApplyPlan crea la tabla — CON su PK (regresión F3-2-pk). --
	// El finding A (task_20d5f912, cerrado) era exactamente esto: la tabla
	// creada por ApplyPlan salía sin constraint de PK ni autoincrement y el
	// primer INSERT con id autogenerado reventaba. Desde el fix,
	// applyCreateTable renderiza el PK con los mismos fragmentos por dialecto
	// que Migrate, así que el create va por el plan y el INSERT de abajo es
	// el assert end-to-end.
	models2 := append(domain.AllModels(), &migrateLedger{})
	p2, err := client.PlanMigration(ctx, models2...)
	if err != nil {
		return fmt.Errorf("plan con ledger: %w", err)
	}
	if p2.IsEmpty() {
		return fmt.Errorf("el plan con el modelo nuevo debería contener su CREATE TABLE")
	}
	if !strings.Contains(p2.String(), ledgerTable) {
		return fmt.Errorf("el plan no menciona %s:\n%s", ledgerTable, p2.String())
	}
	rec.Note(QF("(Plan).Hash"))
	if h := p2.Hash(); h == "" || h != p2.Hash() {
		return fmt.Errorf("Plan.Hash() debe ser determinista y no-vacío (got %q)", h)
	}
	if err := client.ApplyPlan(rec.Mark(ctx, CM("ApplyPlan")), p2); err != nil {
		return fmt.Errorf("apply ledger: %w", err)
	}
	p3, err := client.PlanMigration(ctx, models2...)
	if err != nil {
		return fmt.Errorf("plan post-apply: %w", err)
	}
	if !p3.IsEmpty() {
		return fmt.Errorf("round-trip post-ApplyPlan del ledger roto:\n%s", p3.String())
	}
	// La tabla es real Y tiene PK: el INSERT confía en el id autogenerado
	// (con el finding A vivo esto reventaba con NOT NULL constraint).
	if err := quark.For[migrateLedger](rec.Mark(ctx, QM("Create")), client).Create(&migrateLedger{Ref: "r-1", Amount: 10}); err != nil {
		return fmt.Errorf("create en tabla creada por plan: %w", err)
	}
	if n, err := quark.For[migrateLedger](ctx, client).Count(); err != nil || n != 1 {
		return fmt.Errorf("count en ledger: n=%d err=%v", n, err)
	}

	// --- 2b. Un índice manual NO rompe el round-trip. ----------------------
	// Contrato de mergeNonColumnSurface: los objetos de catálogo que el modelo
	// no declara (índices/FKs/checks) se conservan, no generan drops.
	if err := client.CreateIndex(rec.Mark(ctx, CM("CreateIndex")), ledgerTable, "idx_superapp_migrate_ref", []string{"ref"}, false); err != nil {
		return fmt.Errorf("create index: %w", err)
	}
	p4, err := client.PlanMigration(ctx, models2...)
	if err != nil {
		return fmt.Errorf("plan post-índice: %w", err)
	}
	if !p4.IsEmpty() {
		return fmt.Errorf("el índice manual generó drift en el plan (mergeNonColumnSurface roto):\n%s", p4.String())
	}

	// --- 3. Registry per-Client (F3-7). -------------------------------------
	rec.Note(CM("RegisteredModels"))
	if len(client.RegisteredModels()) == 0 {
		// Registry vacío → no-op nil documentado ("letting the caller
		// initialise the Client in stages"), no un error.
		if err := client.MigrateRegistered(rec.Mark(ctx, CM("MigrateRegistered"))); err != nil {
			return fmt.Errorf("MigrateRegistered sin modelos debía ser no-op nil, got %v", err)
		}
	}
	rec.Note(CM("RegisterModel"))
	if err := client.RegisterModel(models2...); err != nil {
		return fmt.Errorf("RegisterModel: %w", err)
	}
	if got := len(client.RegisteredModels()); got != len(models2) {
		return fmt.Errorf("RegisteredModels()=%d, esperaba %d", got, len(models2))
	}
	if err := client.MigrateRegistered(rec.Mark(ctx, CM("MigrateRegistered"))); err != nil {
		return fmt.Errorf("MigrateRegistered (todo existente): %w", err)
	}
	pr, err := client.PlanMigrationRegistered(rec.Mark(ctx, CM("PlanMigrationRegistered")))
	if err != nil {
		return fmt.Errorf("PlanMigrationRegistered: %w", err)
	}
	if !pr.IsEmpty() {
		return fmt.Errorf("PlanMigrationRegistered no-vacío con todo migrado:\n%s", pr.String())
	}

	// --- 4. Sync: dry-run no toca, add real añade, vuelta a V1 dropea. -----
	rec.Note(QF("SyncOptions"))
	if has, err := hasColumn(rec.Mark(ctx, CM("IntrospectSchema")), client, ledgerTable, "note"); err != nil {
		return fmt.Errorf("introspect pre-sync: %w", err)
	} else if has {
		return fmt.Errorf("la columna note no debería existir antes del Sync")
	}
	if err := client.Sync(rec.Mark(ctx, CM("Sync")), quark.SyncOptions{DryRun: true}, &migrateLedgerV2{}); err != nil {
		return fmt.Errorf("sync dry-run: %w", err)
	}
	if has, err := hasColumn(ctx, client, ledgerTable, "note"); err != nil {
		return fmt.Errorf("introspect post-dry-run: %w", err)
	} else if has {
		return fmt.Errorf("Sync con DryRun ejecutó DDL (la columna note existe)")
	}
	if err := client.Sync(ctx, quark.SyncOptions{}, &migrateLedgerV2{}); err != nil {
		return fmt.Errorf("sync add column: %w", err)
	}
	if has, err := hasColumn(ctx, client, ledgerTable, "note"); err != nil {
		return fmt.Errorf("introspect post-sync: %w", err)
	} else if !has {
		return fmt.Errorf("Sync no añadió la columna note")
	}
	// La columna nueva es usable end-to-end (no sólo está en el catálogo).
	got, err := quark.For[migrateLedgerV2](ctx, client).Where("ref", "=", "r-1").First()
	if err != nil {
		return fmt.Errorf("first V2: %w", err)
	}
	got.Note = quark.Nullable[string]{V: syncedNoteVal, Valid: true}
	if rows, err := quark.For[migrateLedgerV2](rec.Mark(ctx, QM("Update")), client).Update(&got); err != nil || rows != 1 {
		return fmt.Errorf("update V2: rows=%d err=%v", rows, err)
	}
	if re, err := quark.For[migrateLedgerV2](ctx, client).Where("ref", "=", "r-1").First(); err != nil || !re.Note.Valid || re.Note.V != syncedNoteVal {
		return fmt.Errorf("la columna añadida por Sync no hizo round-trip: note=%+v err=%v", re.Note, err)
	}
	// Vuelta a V1 (NoTransaction cubre la otra opción): con SafeMigrations
	// desactivado en el suite, Sync dropea la columna sobrante.
	if err := client.Sync(ctx, quark.SyncOptions{NoTransaction: true}, &migrateLedger{}); err != nil {
		return fmt.Errorf("sync drop column: %w", err)
	}
	if has, err := hasColumn(ctx, client, ledgerTable, "note"); err != nil {
		return fmt.Errorf("introspect post-drop: %w", err)
	} else if has {
		return fmt.Errorf("Sync de vuelta a V1 no dropeó la columna note (SafeMigrations=false)")
	}

	// --- 4b. ApplyPlan ejecuta ops de columna (el path probado por F6). ----
	// El mismo delta V1↔V2, ahora conducido por plan: OpAddColumn al planear
	// con V2, OpDropColumn al volver a V1. ApplyPlan ejecuta ambos.
	modelsV2 := append(domain.AllModels(), &migrateLedgerV2{})
	pAdd, err := client.PlanMigration(ctx, modelsV2...)
	if err != nil {
		return fmt.Errorf("plan add-column: %w", err)
	}
	if pAdd.IsEmpty() || !strings.Contains(pAdd.String(), "note") {
		return fmt.Errorf("el plan V2 debía proponer la columna note:\n%s", pAdd.String())
	}
	if err := client.ApplyPlan(rec.Mark(ctx, CM("ApplyPlan")), pAdd); err != nil {
		return fmt.Errorf("apply add-column: %w", err)
	}
	if has, err := hasColumn(ctx, client, ledgerTable, "note"); err != nil {
		return fmt.Errorf("introspect post-apply-add: %w", err)
	} else if !has {
		return fmt.Errorf("ApplyPlan no añadió la columna note")
	}
	pDropCol, err := client.PlanMigration(ctx, models2...)
	if err != nil {
		return fmt.Errorf("plan drop-column: %w", err)
	}
	if pDropCol.IsEmpty() {
		return fmt.Errorf("el plan de vuelta a V1 debía proponer el drop de note")
	}
	if err := client.ApplyPlan(rec.Mark(ctx, CM("ApplyPlan")), pDropCol); err != nil {
		return fmt.Errorf("apply drop-column: %w", err)
	}
	if has, err := hasColumn(ctx, client, ledgerTable, "note"); err != nil {
		return fmt.Errorf("introspect post-apply-drop: %w", err)
	} else if has {
		return fmt.Errorf("ApplyPlan no dropeó la columna note")
	}

	// --- 5. Backfill: resume tras fallo + idempotencia (F3-6). -------------
	seed := make([]*migrateLedger, 0, 24)
	for i := 0; i < 24; i++ {
		seed = append(seed, &migrateLedger{Ref: fmt.Sprintf("bf-%02d", i), Amount: int64(i)})
	}
	if err := quark.For[migrateLedger](rec.Mark(ctx, QM("CreateBatch")), client).CreateBatch(seed); err != nil {
		return fmt.Errorf("seed backfill: %w", err)
	}
	// 25 filas (r-1 + 24), BatchSize 10 → lotes de 10/10/5.
	rec.Note(QF("BackfillSpec"))
	sentinel := errors.New("fallo inyectado en el lote 2")
	var run1 [][]int64
	err = client.Backfill(rec.Mark(ctx, CM("Backfill")), quark.BackfillSpec{
		Name: backfillName, Table: ledgerTable, BatchSize: 10,
		Process: func(_ context.Context, pks []int64) error {
			if len(run1) == 1 {
				return sentinel // el lote 1 ya se procesó y su estado quedó persistido
			}
			run1 = append(run1, pks)
			return nil
		},
	})
	if !errors.Is(err, sentinel) {
		return fmt.Errorf("backfill run1: esperaba el sentinel del lote 2, got %v", err)
	}
	if len(run1) != 1 {
		return fmt.Errorf("backfill run1 procesó %d lotes antes del fallo, esperaba exactamente 1", len(run1))
	}
	if len(run1[0]) != 10 {
		return fmt.Errorf("backfill run1: el lote 1 trajo %d PKs, esperaba 10", len(run1[0]))
	}
	maxSeen := run1[0][len(run1[0])-1]
	// Re-invocación con el mismo Name: resume DESPUÉS del último PK persistido,
	// sin reprocesar el lote 1.
	var run2 []int64
	err = client.Backfill(rec.Mark(ctx, CM("Backfill")), quark.BackfillSpec{
		Name: backfillName, Table: ledgerTable, BatchSize: 10,
		Process: func(_ context.Context, pks []int64) error {
			run2 = append(run2, pks...)
			return nil
		},
	})
	if err != nil {
		return fmt.Errorf("backfill run2 (resume): %w", err)
	}
	if len(run2) != 15 {
		return fmt.Errorf("backfill run2 procesó %d PKs, esperaba los 15 restantes", len(run2))
	}
	// Invariante: Backfill itera por PK ASC global, así que el primer PK del
	// resume DEBE superar el último persistido — no hay solape posible.
	if run2[0] <= maxSeen {
		return fmt.Errorf("backfill run2 reprocesó PKs del lote 1 (primero=%d, estado=%d)", run2[0], maxSeen)
	}
	// Tercera invocación: completo → 0 lotes, nil (idempotente).
	calls := 0
	err = client.Backfill(rec.Mark(ctx, CM("Backfill")), quark.BackfillSpec{
		Name: backfillName, Table: ledgerTable, BatchSize: 10,
		Process: func(_ context.Context, _ []int64) error { calls++; return nil },
	})
	if err != nil || calls != 0 {
		return fmt.Errorf("backfill run3 (completo): calls=%d err=%v, esperaba 0 y nil", calls, err)
	}

	// --- 6. Lock de migración distribuido (F3-1 + ADR-0018). ----------------
	rec.Note(QF("MigrationLock"))
	if control.Supports(control.FeatMigrationLock, eng) {
		lock1, err := client.AcquireMigrationLock(rec.Mark(ctx, CM("AcquireMigrationLock")), migLockName, 5*time.Second)
		if err != nil {
			return fmt.Errorf("acquire lock: %w", err)
		}
		// Contención: el 2º acquire del mismo nombre agota su timeout (1s — el
		// mínimo entero de GET_LOCK/DBMS_LOCK) y devuelve ErrLockTimeout.
		// SUPUESTO: pool sin acotar (cada acquire toma su propia conexión
		// dedicada). Si S7 acota el pool de Oracle (ORA-12516), que no sea a 1
		// durante este exerciser o el 2º acquire esperaría conexión, no lock.
		if _, err := client.AcquireMigrationLock(ctx, migLockName, time.Second); !errors.Is(err, quark.ErrLockTimeout) {
			_ = lock1.Release(ctx)
			return fmt.Errorf("acquire concurrente: esperaba ErrLockTimeout, got %v", err)
		}
		rec.Note(QF("ErrLockTimeout"))
		if err := lock1.Release(ctx); err != nil {
			return fmt.Errorf("release: %w", err)
		}
		rec.Note(QF("(MigrationLock).Release"))
		// Liberado el primero, el nombre vuelve a estar disponible.
		lock2, err := client.AcquireMigrationLock(ctx, migLockName, 5*time.Second)
		if err != nil {
			return fmt.Errorf("re-acquire tras release: %w", err)
		}
		if err := lock2.Release(ctx); err != nil {
			return fmt.Errorf("release 2: %w", err)
		}
	} else {
		// Capacidad desigual ≠ fallo: SQLite no modela locks distribuidos.
		_, err := client.AcquireMigrationLock(rec.Mark(ctx, CM("AcquireMigrationLock")), migLockName, time.Second)
		if !errors.Is(err, quark.ErrUnsupportedFeature) {
			return fmt.Errorf("lock en %s: esperaba ErrUnsupportedFeature, got %v", eng, err)
		}
		rec.Note(QF("ErrUnsupportedFeature"))
	}

	// --- 7. Migraciones versionadas (paquete migrate). ----------------------
	// El Migrator usa client.Exec para su bookkeeping y eso exige
	// AllowRawQueries — requisito DOCUMENTADO ("Raw SQL Requirement",
	// website/docs/reference/api/migrations.mdx): el ciclo corre sobre un
	// client de migración dedicado, exactamente como manda la doc (este
	// exerciser es su regresión end-to-end). Instrumentado con el mismo
	// recorder y SIN caché: si las mutaciones fueran por otro client, la caché
	// del harness serviría Counts stale.
	migLimits := quark.DefaultLimits()
	migLimits.AllowRawQueries = true
	admin, err := quark.New(conn.Driver, conn.DSN, append(rec.Options(), quark.WithLimits(migLimits))...)
	if err != nil {
		return fmt.Errorf("client de migración (AllowRawQueries): %w", err)
	}
	defer admin.Close()
	// El registry es GLOBAL y mutable (deuda documentada en el playbook):
	// Reset al entrar y al salir para no contaminar otros usos del proceso.
	migrate.Reset()
	defer migrate.Reset()
	rec.Note(MIG("Reset"), MIG("Register"), MIG("Migration"), MIG("Migrator"), MIG("NewMigrator"))
	migrate.Register(&migrate.Migration{
		ID: migVNotesID, Name: "create v_notes",
		Up:   func(ctx context.Context, c *quark.Client) error { return c.Migrate(ctx, &migrateVNote{}) },
		Down: func(ctx context.Context, c *quark.Client) error { return execRaw(ctx, c, "DROP TABLE "+vNotesTable) },
	})
	migrate.Register(&migrate.Migration{
		ID: migVSeedID, Name: "seed v_notes",
		Up: func(ctx context.Context, c *quark.Client) error {
			return quark.For[migrateVNote](ctx, c).Create(&migrateVNote{Body: "seeded"})
		},
		Down: func(ctx context.Context, c *quark.Client) error { return execRaw(ctx, c, "DELETE FROM "+vNotesTable) },
	})
	// RegisteredCount es el guard con el que la CLI rechaza el no-op de
	// 'migrate up' con registro vacío (QK-P1-1): tras registrar dos, cuenta 2.
	if n := migrate.RegisteredCount(); n != 2 {
		return fmt.Errorf("RegisteredCount: %d, esperaba 2", n)
	}
	rec.Note(MIG("RegisteredCount"))
	m := migrate.NewMigrator(admin)
	if err := m.Init(rec.Mark(ctx, MIG("(*Migrator).Init"))); err != nil {
		return fmt.Errorf("migrator init: %w", err)
	}
	// Dry-run: lista pendientes sin ejecutar — la tabla no debe aparecer.
	if err := m.UpDryRun(rec.Mark(ctx, MIG("(*Migrator).UpDryRun")), 0); err != nil {
		return fmt.Errorf("up dry-run: %w", err)
	}
	if has, err := hasTable(ctx, client, vNotesTable); err != nil {
		return fmt.Errorf("introspect post-dry-run versionado: %w", err)
	} else if has {
		return fmt.Errorf("UpDryRun ejecutó la migración (existe %s)", vNotesTable)
	}
	// Up aplica las dos en orden de ID.
	if err := m.Up(rec.Mark(ctx, MIG("(*Migrator).Up")), 0); err != nil {
		return fmt.Errorf("up: %w", err)
	}
	if n, err := quark.For[migrateVNote](ctx, admin).Count(); err != nil || n != 1 {
		return fmt.Errorf("v_notes tras Up: n=%d err=%v, esperaba la fila seeded", n, err)
	}
	applied, err := m.GetApplied(rec.Mark(ctx, MIG("(*Migrator).GetApplied")))
	if err != nil {
		return fmt.Errorf("get applied: %w", err)
	}
	if !applied[migVNotesID] || !applied[migVSeedID] {
		return fmt.Errorf("GetApplied no registra las dos migraciones: %v", applied)
	}
	// Down(1) revierte SÓLO la última (ID más alto): el seed se va, la tabla queda.
	if err := m.Down(rec.Mark(ctx, MIG("(*Migrator).Down")), 1); err != nil {
		return fmt.Errorf("down(1): %w", err)
	}
	if n, err := quark.For[migrateVNote](ctx, admin).Count(); err != nil || n != 0 {
		return fmt.Errorf("v_notes tras Down(1): n=%d err=%v, esperaba 0 (seed revertido)", n, err)
	}
	applied, err = m.GetApplied(ctx)
	if err != nil {
		return fmt.Errorf("get applied post-down: %w", err)
	}
	if !applied[migVNotesID] || applied[migVSeedID] {
		return fmt.Errorf("Down(1) debía revertir sólo %s: %v", migVSeedID, applied)
	}
	// Up re-aplica el pendiente; Down(0) revierte todo y deja cero rastro.
	if err := m.Up(ctx, 1); err != nil {
		return fmt.Errorf("re-up: %w", err)
	}
	if n, err := quark.For[migrateVNote](ctx, admin).Count(); err != nil || n != 1 {
		return fmt.Errorf("v_notes tras re-Up: n=%d err=%v", n, err)
	}
	if err := m.Down(ctx, 0); err != nil {
		return fmt.Errorf("down(0): %w", err)
	}
	if has, err := hasTable(ctx, client, vNotesTable); err != nil {
		return fmt.Errorf("introspect post-down-all: %w", err)
	} else if has {
		return fmt.Errorf("Down(0) no dropeó %s", vNotesTable)
	}

	// --- 8. Cleanup: dropear el ledger vía plan (OpDropTable real). ---------
	// PlanMigration sin el ledger en el desired DEBE proponer su drop; tras
	// aplicarlo, la BD vuelve al estado canónico (el run es re-entrante y los
	// exercisers que vengan detrás ven el mundo que esperaban).
	pDrop, err := client.PlanMigration(ctx, domain.AllModels()...)
	if err != nil {
		return fmt.Errorf("plan drop: %w", err)
	}
	if pDrop.IsEmpty() || !strings.Contains(pDrop.String(), ledgerTable) {
		return fmt.Errorf("el plan de cleanup debía proponer el drop de %s:\n%s", ledgerTable, pDrop.String())
	}
	if err := client.ApplyPlan(ctx, pDrop); err != nil {
		return fmt.Errorf("apply drop: %w", err)
	}
	pFinal, err := client.PlanMigration(ctx, domain.AllModels()...)
	if err != nil {
		return fmt.Errorf("plan final: %w", err)
	}
	if !pFinal.IsEmpty() {
		return fmt.Errorf("la BD no quedó canónica tras el cleanup:\n%s", pFinal.String())
	}
	// El estado del backfill ya no apunta a nada (la tabla se dropeó): fuera,
	// para que el próximo run arranque de cero.
	_, _ = client.Raw().ExecContext(ctx,
		"DELETE FROM quark_backfill_state WHERE name = "+client.Dialect().Placeholder(1), backfillName)
	return nil
}}

// execRaw ejecuta DDL/DML crudo dentro de una migración versionada (los cuerpos
// Up/Down son código del usuario; Raw() es la vía documentada para DDL que el
// builder no modela).
func execRaw(ctx context.Context, c *quark.Client, sql string) error {
	_, err := c.Raw().ExecContext(ctx, sql)
	return err
}

// hasTable introspecciona el schema y dice si la tabla existe.
func hasTable(ctx context.Context, client *quark.Client, table string) (bool, error) {
	sch, err := client.IntrospectSchema(ctx)
	if err != nil {
		return false, err
	}
	for _, t := range sch.Tables {
		if t.Name == table {
			return true, nil
		}
	}
	return false, nil
}

// hasColumn introspecciona el schema y dice si la columna existe en la tabla.
func hasColumn(ctx context.Context, client *quark.Client, table, column string) (bool, error) {
	sch, err := client.IntrospectSchema(ctx)
	if err != nil {
		return false, err
	}
	for _, t := range sch.Tables {
		if t.Name != table {
			continue
		}
		for _, c := range t.Columns {
			if c.Name == column {
				return true, nil
			}
		}
	}
	return false, nil
}
