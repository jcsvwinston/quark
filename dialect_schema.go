// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"fmt"
	"strings"

	"github.com/jcsvwinston/quark/internal/migrate"
	"github.com/jcsvwinston/quark/quarkdriver"
)

// This file is the built-in dialects' answers to the schema questions of
// quarkdriver (ADR-0026, A11 Q2): their column types, their auto-increment
// key, and how each creates a table or an index only when it is missing. The
// schema path asks these questions through type assertions and never reads a
// dialect's Name() to decide what to write, so a dialect that wraps a
// built-in under another name writes the same DDL as long as it forwards the
// interfaces.
//
// Each answer passes the dialect's own ENGINE constant to the tables in
// internal/migrate — the concrete type knows what it is; nothing reads the
// name it was registered under.

// Compile-time checks: what each built-in answers.
var (
	_ quarkdriver.ColumnTyper     = (*PostgresDialect)(nil)
	_ quarkdriver.AutoIncrementer = (*PostgresDialect)(nil)
	_ quarkdriver.ColumnTyper     = (*MySQLDialect)(nil)
	_ quarkdriver.AutoIncrementer = (*MySQLDialect)(nil)
	_ quarkdriver.IdempotentDDL   = (*MySQLDialect)(nil)
	_ quarkdriver.ColumnTyper     = (*MariaDBDialect)(nil)
	_ quarkdriver.AutoIncrementer = (*MariaDBDialect)(nil)
	_ quarkdriver.IdempotentDDL   = (*MariaDBDialect)(nil)
	_ quarkdriver.ColumnTyper     = (*SQLiteDialect)(nil)
	_ quarkdriver.AutoIncrementer = (*SQLiteDialect)(nil)
	_ quarkdriver.ColumnTyper     = (*MSSQLDialect)(nil)
	_ quarkdriver.AutoIncrementer = (*MSSQLDialect)(nil)
	_ quarkdriver.IdempotentDDL   = (*MSSQLDialect)(nil)
	_ quarkdriver.ColumnTyper     = (*OracleDialect)(nil)
	_ quarkdriver.AutoIncrementer = (*OracleDialect)(nil)
	_ quarkdriver.IdempotentDDL   = (*OracleDialect)(nil)
)

// --- column types and the boolean literal (quarkdriver.ColumnTyper) ---------

// ColumnType is PostgreSQL's type for a column of the given kind.
func (*PostgresDialect) ColumnType(s quarkdriver.ColumnSpec) string {
	return migrate.EngineColumnType(migrate.EnginePostgres, s)
}

// BoolLiteral is TRUE or FALSE: PostgreSQL's BOOLEAN rejects 1 and 0.
func (*PostgresDialect) BoolLiteral(v bool) string {
	return migrate.EngineBoolLiteral(migrate.EnginePostgres, v)
}

// ColumnType is MySQL's type for a column of the given kind.
func (*MySQLDialect) ColumnType(s quarkdriver.ColumnSpec) string {
	return migrate.EngineColumnType(migrate.EngineMySQL, s)
}

// BoolLiteral is 1 or 0.
func (*MySQLDialect) BoolLiteral(v bool) string {
	return migrate.EngineBoolLiteral(migrate.EngineMySQL, v)
}

// ColumnType is MariaDB's type for a column of the given kind.
func (*MariaDBDialect) ColumnType(s quarkdriver.ColumnSpec) string {
	return migrate.EngineColumnType(migrate.EngineMariaDB, s)
}

// BoolLiteral is 1 or 0.
func (*MariaDBDialect) BoolLiteral(v bool) string {
	return migrate.EngineBoolLiteral(migrate.EngineMariaDB, v)
}

// ColumnType is SQLite's type for a column of the given kind.
func (*SQLiteDialect) ColumnType(s quarkdriver.ColumnSpec) string {
	return migrate.EngineColumnType(migrate.EngineSQLite, s)
}

// BoolLiteral is 1 or 0.
func (*SQLiteDialect) BoolLiteral(v bool) string {
	return migrate.EngineBoolLiteral(migrate.EngineSQLite, v)
}

// ColumnType is SQL Server's type for a column of the given kind.
func (*MSSQLDialect) ColumnType(s quarkdriver.ColumnSpec) string {
	return migrate.EngineColumnType(migrate.EngineMSSQL, s)
}

// BoolLiteral is 1 or 0: SQL Server's BIT rejects TRUE and FALSE.
func (*MSSQLDialect) BoolLiteral(v bool) string {
	return migrate.EngineBoolLiteral(migrate.EngineMSSQL, v)
}

// ColumnType is Oracle's type for a column of the given kind.
func (*OracleDialect) ColumnType(s quarkdriver.ColumnSpec) string {
	return migrate.EngineColumnType(migrate.EngineOracle, s)
}

// BoolLiteral is 1 or 0: Oracle's NUMBER(1) rejects TRUE and FALSE.
func (*OracleDialect) BoolLiteral(v bool) string {
	return migrate.EngineBoolLiteral(migrate.EngineOracle, v)
}

// --- the auto-increment key (quarkdriver.AutoIncrementer) -------------------

// AutoIncrementColumn is BIGSERIAL PRIMARY KEY.
func (*PostgresDialect) AutoIncrementColumn() (string, string) {
	return migrate.EngineAutoIncrement(migrate.EnginePostgres)
}

// AutoIncrementColumn is BIGINT AUTO_INCREMENT PRIMARY KEY.
func (*MySQLDialect) AutoIncrementColumn() (string, string) {
	return migrate.EngineAutoIncrement(migrate.EngineMySQL)
}

// AutoIncrementColumn is BIGINT AUTO_INCREMENT PRIMARY KEY.
func (*MariaDBDialect) AutoIncrementColumn() (string, string) {
	return migrate.EngineAutoIncrement(migrate.EngineMariaDB)
}

// AutoIncrementColumn is INTEGER PRIMARY KEY AUTOINCREMENT, whose catalog
// type is INTEGER: only INTEGER PRIMARY KEY aliases SQLite's rowid.
func (*SQLiteDialect) AutoIncrementColumn() (string, string) {
	return migrate.EngineAutoIncrement(migrate.EngineSQLite)
}

// AutoIncrementColumn is BIGINT IDENTITY(1,1) PRIMARY KEY.
func (*MSSQLDialect) AutoIncrementColumn() (string, string) {
	return migrate.EngineAutoIncrement(migrate.EngineMSSQL)
}

// AutoIncrementColumn is NUMBER GENERATED ALWAYS AS IDENTITY PRIMARY KEY.
func (*OracleDialect) AutoIncrementColumn() (string, string) {
	return migrate.EngineAutoIncrement(migrate.EngineOracle)
}

// --- creating only what is missing (quarkdriver.IdempotentDDL) --------------
//
// PostgreSQL and SQLite take the default — CREATE TABLE IF NOT EXISTS and
// CREATE INDEX IF NOT EXISTS — and do not implement the interface.

// CreateTableIfNotExists is CREATE TABLE IF NOT EXISTS.
func (m *MySQLDialect) CreateTableIfNotExists(table, body string) string {
	return migrate.PortableCreateTable(m, "IF NOT EXISTS ", table, body)
}

// CreateIndexIfNotExists is a plain CREATE INDEX: MySQL and MariaDB have no
// IF NOT EXISTS on it. The "Duplicate key name" error (1061) it raises when
// the index exists counts as success.
func (m *MySQLDialect) CreateIndexIfNotExists(table, index string, columns []string, unique bool) string {
	return migrate.PortableCreateIndex(m, "", table, index, columns, unique)
}

// IsAlreadyExists recognises error 1061, "Duplicate key name", on a CREATE
// INDEX.
func (m *MySQLDialect) IsAlreadyExists(object quarkdriver.SchemaObject, err error) bool {
	return object == quarkdriver.ObjectIndex && err != nil && strings.Contains(err.Error(), "1061")
}

// CreateTableIfNotExists guards a plain CREATE TABLE with a lookup in
// sys.tables: SQL Server has no CREATE TABLE IF NOT EXISTS.
func (m *MSSQLDialect) CreateTableIfNotExists(table, body string) string {
	return fmt.Sprintf("IF NOT EXISTS (SELECT * FROM sys.tables WHERE name = '%s') %s",
		sqlStringContent(table), migrate.PortableCreateTable(m, "", table, body))
}

// CreateIndexIfNotExists guards a plain CREATE INDEX with a lookup in
// sys.indexes.
func (m *MSSQLDialect) CreateIndexIfNotExists(table, index string, columns []string, unique bool) string {
	return fmt.Sprintf("IF NOT EXISTS (SELECT name FROM sys.indexes WHERE name = '%s') %s",
		sqlStringContent(index), migrate.PortableCreateIndex(m, "", table, index, columns, unique))
}

// IsAlreadyExists recognises nothing: the guards make the statements
// conditional.
func (m *MSSQLDialect) IsAlreadyExists(quarkdriver.SchemaObject, error) bool { return false }

// CreateTableIfNotExists is a plain CREATE TABLE: Oracle has no IF NOT EXISTS
// on it. ORA-00955 ("name is already used by an existing object") counts as
// success.
func (o *OracleDialect) CreateTableIfNotExists(table, body string) string {
	return migrate.PortableCreateTable(o, "", table, body)
}

// CreateIndexIfNotExists is CREATE INDEX IF NOT EXISTS, which Oracle accepts
// since 23ai. ORA-01408 ("such column list already indexed") counts as
// success.
func (o *OracleDialect) CreateIndexIfNotExists(table, index string, columns []string, unique bool) string {
	return migrate.PortableCreateIndex(o, "IF NOT EXISTS ", table, index, columns, unique)
}

// IsAlreadyExists recognises ORA-00955 on a CREATE TABLE, ORA-01408 on a
// CREATE INDEX and ORA-02264 ("name already used by an existing constraint")
// on an ADD CONSTRAINT.
func (o *OracleDialect) IsAlreadyExists(object quarkdriver.SchemaObject, err error) bool {
	if err == nil {
		return false
	}
	switch object {
	case quarkdriver.ObjectTable:
		return strings.Contains(err.Error(), "ORA-00955")
	case quarkdriver.ObjectIndex:
		return strings.Contains(err.Error(), "ORA-01408")
	case quarkdriver.ObjectConstraint:
		return strings.Contains(err.Error(), "ORA-02264")
	}
	return false
}

// sqlStringContent escapes s for the inside of a single-quoted SQL string.
func sqlStringContent(s string) string { return strings.ReplaceAll(s, "'", "''") }
