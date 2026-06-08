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
)

// supported mapea cada feature limitada a los motores que la soportan. Un motor
// ausente para una feature DEBE devolver quark.ErrUnsupportedFeature; el
// exerciser lo afirma y la celda se marca SkippedExpected (no Failed).
//
// Fuentes: rls_native.go (PG-only), docs/ROADMAP.md F3-1 (lock: SQLite/Oracle
// devuelven ErrUnsupportedFeature), ADR-0019 (LISTEN/NOTIFY PG-only).
var supported = map[Feature]map[Engine]bool{
	FeatRLSNative:     {Postgres: true},
	FeatListenNotify:  {Postgres: true},
	FeatMigrationLock: {Postgres: true, MySQL: true, MariaDB: true, MSSQL: true},
	FeatSkipLocked:    {Postgres: true, MySQL: true, MariaDB: true, MSSQL: true, Oracle: true},
}

// Supports indica si el motor e soporta la feature f. Si devuelve false, el
// exerciser debe esperar quark.ErrUnsupportedFeature en ese motor.
func Supports(f Feature, e Engine) bool {
	return supported[f][e]
}
