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

	results := Run(conns, 2, AllExercisers())
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
		QM("Preload"), QM("Cache"),
		QF("NewTenantRouter"), QF("DefaultTenantConfig"), QF("RowLevelSecurityClient"),
		TRM("ResolveTenant"), TRM("GetClient"),
		QF("RowLevelSecurityNative"), TRM("Tx"), // RLSNATIVE (rechazo en sqlite vía Note)
		QF("SchemaPerTenant"),                         // SCHEMAPERTENANT (skip en sqlite vía Note)
		QF("DatabasePerTenant"), TRM("ActiveTenants"), // DBPERTENANT (full en sqlite: ficheros)
		// MIGRATE: schema-as-code + registry + sync + backfill + lock (rechazo
		// en sqlite) + migraciones versionadas (paquete migrate).
		CM("PlanMigration"), CM("ApplyPlan"), CM("CreateIndex"), CM("IntrospectSchema"),
		CM("RegisterModel"), CM("RegisteredModels"), CM("MigrateRegistered"), CM("PlanMigrationRegistered"),
		CM("Sync"), CM("Backfill"), CM("AcquireMigrationLock"), QM("CreateBatch"),
		QF("(Plan).IsEmpty"), QF("(Plan).String"), QF("(Plan).Hash"),
		QF("ErrUnsupportedFeature"),
		MIG("Register"), MIG("Reset"), MIG("NewMigrator"),
		MIG("(*Migrator).Init"), MIG("(*Migrator).UpDryRun"), MIG("(*Migrator).Up"),
		MIG("(*Migrator).GetApplied"), MIG("(*Migrator).Down"),
		// HA: réplicas (full en sqlite vía ficheros), sharding (full en sqlite),
		// deadlock (camino feliz en sqlite; recuperación en servidores).
		QF("WithReplicas"), QF("WithReplicaStrategy"), QF("Sticky"), QF("ReplicaRoundRobin"),
		QF("NewShardRouter"), QF("HashShardFunc"), QF("WithShardKey"), QF("ShardKeyFromContext"),
		SRM("GetClient"), SRM("ShardNames"),
		QF("WithDeadlockRetry"), QM("UpdateMap"),
		// OBSERVABILITY: otel in-memory + redacción + logger (los 6 motores).
		OTL("New"), OTL("WithDBSystem"), OTL("WithSpanRedaction"),
		OTL("IncludeArgs"), OTL("RedactArgs"),
		OTL("(*Middleware).WrapExec"), OTL("(*Middleware).WrapQuery"), OTL("(*Middleware).WrapQueryRow"),
		QF("WithMiddleware"), QF("WithLogger"), QF("WithSlowQueryThreshold"),
		// STRICTREADS: modo estricto de lecturas (#247) — asserts funcionales
		// sobre el WARN/reject/N+1 en el propio exerciser.
		QF("WithStrictReads"), QF("TrackReads"), QM("AllowUnbounded"),
		// BUILDERADV: los ~35 métodos avanzados de Query[T].
		QM("WhereBetween"), QM("WhereNot"), QM("WhereP"), QM("WhereExpr"), QM("Apply"),
		QM("UpdateFields"), QM("Track"), QM("LeftJoin"), QM("RightJoin"),
		QM("HavingAggregate"), QM("HavingExpr"), QM("SelectExpr"),
		QM("AsSubquery"), QM("MustAsSubquery"), QM("With"), QM("WithRecursive"), QM("WhereSubquery"),
		QM("Union"), QM("UnionAll"), QM("Intersect"), QM("Except"),
		QM("ForUpdate"), QM("ForShare"), QM("SkipLocked"), QM("NoWait"),
		QM("WithTrashed"), QM("OnlyTrashed"), QM("Unscoped"), QM("Restore"), QM("HardDelete"),
		QM("Upsert"), QM("UpsertBatch"), QM("UpdateBatch"), QM("DeleteBatch"), QM("DeleteBy"),
		QF("NewTypedColumn"), QF("Col"), QF("Lit"), QF("Eq"), QF("NewWindow"), QF("Over"), QF("RowNumber"),
		QF("(*JoinBuilder[T]).On"), QF("(*JoinBuilder[T]).OnRaw"),
		QF("(*TrackedQuery[T]).Find"), QF("(*Tracked[T]).Save"),
	} {
		if !seen[k] {
			t.Errorf("cobertura: falta el símbolo %s", k)
		}
	}
	t.Logf("crud cubrió %d símbolos en sqlite; statements capturados=%d", len(seen), r.Rec.Count())
}
