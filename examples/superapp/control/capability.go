// Package control implementa la maquinaria de cobertura, paridad y gating de la
// superapp de aceptación: el manifiesto de superficie pública, el reconciliador
// invocado-vs-manifiesto, la matriz de capacidad por motor y el reporte/gate.
//
// Compila solo con la stdlib: no depende de la API de Quark, para poder
// razonarse y testearse de forma aislada.
package control

// Engine identifica cada motor soportado por Quark.
type Engine string

const (
	SQLite   Engine = "sqlite"
	Postgres Engine = "postgres"
	MySQL    Engine = "mysql"
	MariaDB  Engine = "mariadb"
	MSSQL    Engine = "mssql"
	Oracle   Engine = "oracle"
)

// AllEngines devuelve los 6 motores en orden estable.
func AllEngines() []Engine {
	return []Engine{SQLite, Postgres, MySQL, MariaDB, MSSQL, Oracle}
}

// Feature nombra una capacidad que NO está disponible en todos los motores.
// Solo se listan las features con cobertura desigual; lo no listado se asume
// soportado en los 6.
type Feature string

const (
	FeatRLSNative     Feature = "rls_native"     // RLS forzada por el motor (set_config + CREATE POLICY)
	FeatListenNotify  Feature = "listen_notify"  // listener inbound LISTEN/NOTIFY
	FeatMigrationLock Feature = "migration_lock" // lock de migración distribuido
	FeatSkipLocked    Feature = "skip_locked"    // SELECT ... FOR UPDATE SKIP LOCKED

	// FeatSchemaPerTenant marca los motores con schemas reales dentro de una
	// base (CREATE SCHEMA + qualificación schema.table). A diferencia de las
	// features de arriba, Quark NO gatea esta estrategia con
	// ErrUnsupportedFeature (el builder emite el SQL cualificado y es el motor
	// quien lo acepta o no): en un motor no soportado el exerciser SALTA el
	// path funcional (SkippedExpected), no aserta un error. Fuente:
	// docs/playbooks/tenant.md ("sólo PG y MSSQL real, MySQL no tiene schemas").
	FeatSchemaPerTenant Feature = "schema_per_tenant"

	// FeatDBPerTenantProvision marca los motores donde el ARNÉS puede
	// aprovisionar una base de datos por tenant (la estrategia DatabasePerTenant
	// en sí es agnóstica del motor — sólo necesita DSNs distintos). SQLite usa
	// ficheros; PG/MySQL/MariaDB/MSSQL un CREATE DATABASE vía admin. Oracle
	// queda fuera: una "database" por tenant ahí es un PDB (operación
	// administrativa pesada, fuera del alcance del harness) y el equivalente
	// ligero (CREATE USER = schema) es la otra estrategia. Los exercisers de
	// HA (réplicas/sharding) reusan esta misma capacidad: sus réplicas y
	// shards son DSNs aprovisionados con idéntico mecanismo.
	FeatDBPerTenantProvision Feature = "db_per_tenant_provision"

	// FeatIntersectExcept marca los motores cuyo dialecto renderiza INTERSECT
	// y EXCEPT (Oracle vía MINUS). MySQL/MariaDB no los modelan en quark
	// (setop.go devuelve ErrUnsupportedFeature) — feature gateada por Quark:
	// el exerciser ASERTA el sentinel donde falta, como con el migration lock.
	FeatIntersectExcept Feature = "intersect_except"

	// FeatDeadlock marca los motores donde un deadlock por orden de locks
	// invertido puede MANIFESTARSE (los 5 con servidor). SQLite serializa
	// escrituras (SQLITE_BUSY no es un código de deadlock), así que el
	// exerciser invoca igualmente WithDeadlockRetry (el símbolo se ejerce en
	// los 6) pero la RECUPERACIÓN sólo se asierta donde el deadlock existe.
	// Semántica de capacidad (como SchemaPerTenant): skip del path funcional,
	// no error sentinel. Fuente: bugbash F12 + tx_deadlock_integration_test.go.
	FeatDeadlock Feature = "deadlock"
)

// supported mapea cada feature limitada a los motores que la soportan. Para las
// features gateadas por Quark (RLSNative, ListenNotify, MigrationLock,
// SkipLocked) un motor ausente DEBE devolver quark.ErrUnsupportedFeature; el
// exerciser lo afirma y la celda se marca SkippedExpected (no Failed). Para las
// features de capacidad del motor/arnés (SchemaPerTenant, DBPerTenantProvision)
// no hay error sentinel: el exerciser salta el path funcional.
//
// Fuentes: rls_native.go (PG-only), migration_lock.go + dialect_migration_lock.go
// (lock: PG/MySQL/MariaDB/MSSQL/Oracle lo implementan — Oracle vía DBMS_LOCK,
// ADR-0018; sólo SQLite devuelve ErrUnsupportedFeature), ADR-0019
// (LISTEN/NOTIFY PG-only), docs/playbooks/tenant.md (schemas: PG/MSSQL).
var supported = map[Feature]map[Engine]bool{
	FeatRLSNative:            {Postgres: true},
	FeatListenNotify:         {Postgres: true},
	FeatMigrationLock:        {Postgres: true, MySQL: true, MariaDB: true, MSSQL: true, Oracle: true},
	FeatSkipLocked:           {Postgres: true, MySQL: true, MariaDB: true, MSSQL: true, Oracle: true},
	FeatSchemaPerTenant:      {Postgres: true, MSSQL: true},
	FeatDBPerTenantProvision: {SQLite: true, Postgres: true, MySQL: true, MariaDB: true, MSSQL: true},
	FeatDeadlock:             {Postgres: true, MySQL: true, MariaDB: true, MSSQL: true, Oracle: true},
	FeatIntersectExcept:      {SQLite: true, Postgres: true, MSSQL: true, Oracle: true},
}

// Supports indica si el motor e soporta la feature f. Si devuelve false, el
// exerciser debe esperar quark.ErrUnsupportedFeature en ese motor.
func Supports(f Feature, e Engine) bool {
	return supported[f][e]
}
