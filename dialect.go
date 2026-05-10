// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"fmt"
	"strings"

	"github.com/jcsvwinston/quark/internal/guard"
)

// Dialect defines the interface for database-specific SQL generation.
// Each supported database (PostgreSQL, MySQL, SQLite, etc.) implements this interface.
type Dialect interface {
	// Name returns the dialect name (e.g., "postgres", "mysql", "sqlite").
	Name() string

	// Placeholder returns the placeholder for the given parameter index.
	// PostgreSQL: $1, $2, etc.
	// MySQL/SQLite: ?
	// MSSQL: @p1, @p2, etc.
	// Oracle: :1, :2, etc.
	Placeholder(index int) string

	// Quote returns a quoted identifier (table/column name).
	// PostgreSQL: "identifier"
	// MySQL: `identifier`
	// MSSQL: [identifier]
	// SQLite/Oracle: "identifier"
	Quote(identifier string) string

	// Placeholders returns a slice of placeholders for n parameters.
	Placeholders(n int) []string

	// LimitOffset returns the LIMIT/OFFSET clause for the given parameters.
	LimitOffset(limit, offset int) string

	// SupportsReturning indicates if the dialect supports RETURNING clause.
	SupportsReturning() bool

	// Returning returns the RETURNING clause for the given columns.
	// Returns empty string if not supported.
	Returning(columns ...string) string

	// SupportsLastInsertID indicates if the dialect supports LastInsertId().
	SupportsLastInsertID() bool

	// LastInsertIDQuery returns the query to get the last insert ID.
	// Used for dialects that don't support RETURNING.
	LastInsertIDQuery(table, pkColumn string) string

	// CurrentTimestamp returns the SQL function for current timestamp.
	CurrentTimestamp() string

	// BuildRoutineQuery returns the SQL for a table-valued function or routine returning rows.
	// E.g., Postgres: SELECT * FROM func($1, $2)
	BuildRoutineQuery(routine string, argCount int) string

	// BuildProcedureCall returns the SQL for calling a procedure (pure logic / OUT params).
	// E.g., MySQL: CALL proc(?, ?)
	BuildProcedureCall(procedure string, argCount int) string

	// JSONExtract returns the SQL expression to extract a value from a JSON column,
	// the bind args required by that expression, or an error if the path is
	// malformed.
	//
	// The returned SQL fragment uses literal '?' as a neutral bind marker; the
	// caller (typically buildWhereClause) substitutes each '?' for the dialect's
	// placeholder syntax (`$N`, `?`, `@pN`, `:N`) at the appropriate arg index.
	//
	// The path is validated and passed as a bind parameter — never interpolated
	// into the SQL surface. This closes the SQL-injection vector that existed
	// while the path was concatenated with fmt.Sprintf.
	//
	// Example outputs (with column "data" and path "user.name"):
	//   Postgres: jsonb_extract_path_text(("data")::jsonb, ?, ?) / args=["user","name"]
	//   MySQL:    JSON_EXTRACT(`data`, ?) / args=["$.user.name"]
	//   SQLite:   JSON_EXTRACT("data", ?) / args=["$.user.name"]
	//   MSSQL:    JSON_VALUE([data], ?) / args=["$.user.name"]
	//   Oracle:   JSON_VALUE("DATA", ?) / args=["$.user.name"]
	JSONExtract(column, path string) (sql string, args []any, err error)

	// AlterTableAddColumn returns SQL to add a column to a table.
	// E.g., PostgreSQL: ALTER TABLE "users" ADD COLUMN "email" VARCHAR(255)
	AlterTableAddColumn(table, column, dataType string) string

	// AlterTableDropColumn returns SQL to drop a column from a table.
	// E.g., PostgreSQL: ALTER TABLE "users" DROP COLUMN "email"
	AlterTableDropColumn(table, column string) string

	// AlterTableAlterColumn returns SQL to alter a column's type.
	// E.g., PostgreSQL: ALTER TABLE "users" ALTER COLUMN "email" TYPE VARCHAR(255)
	AlterTableAlterColumn(table, column, newDataType string) string

	// RenameColumn returns SQL to rename a column.
	// E.g., PostgreSQL: ALTER TABLE "users" RENAME COLUMN "old_name" TO "new_name"
	RenameColumn(table, oldName, newName string) string

	// RenameTable returns SQL to rename a table.
	// E.g., PostgreSQL: ALTER TABLE "users" RENAME TO "accounts"
	RenameTable(oldName, newName string) string

	// SupportsTransactionalDDL indicates if the dialect supports DDL in transactions.
	SupportsTransactionalDDL() bool

	// UpsertSQL returns the dialect-specific upsert (INSERT … ON CONFLICT … DO UPDATE)
	// fragment that is appended after the VALUES clause.
	// conflictCols: columns that define the conflict target (e.g. primary key or unique index).
	// updateCols:   columns to update on conflict; if empty defaults to all non-conflict columns.
	// argOffset:    current placeholder index (1-based) so positional dialects stay in sync.
	// Returns the SQL fragment and the additional argument list (for the SET clause values).
	UpsertSQL(conflictCols, updateCols []string, argOffset int) string
}

// baseDialect provides common functionality for all dialects.
type baseDialect struct {
	name string
}

func (d *baseDialect) Name() string {
	return d.name
}

// PostgresDialect implements the PostgreSQL dialect.
type PostgresDialect struct {
	baseDialect
}

// PostgreSQL returns the PostgreSQL dialect instance.
func PostgreSQL() Dialect {
	return &PostgresDialect{
		baseDialect{name: "postgres"},
	}
}

func (p *PostgresDialect) Placeholder(index int) string {
	return fmt.Sprintf("$%d", index)
}

func (p *PostgresDialect) Placeholders(n int) []string {
	placeholders := make([]string, n)
	for i := 0; i < n; i++ {
		placeholders[i] = p.Placeholder(i + 1)
	}
	return placeholders
}

func (p *PostgresDialect) Quote(identifier string) string {
	return fmt.Sprintf(`"%s"`, identifier)
}

func (p *PostgresDialect) LimitOffset(limit, offset int) string {
	if limit > 0 && offset > 0 {
		return fmt.Sprintf("LIMIT %d OFFSET %d", limit, offset)
	}
	if limit > 0 {
		return fmt.Sprintf("LIMIT %d", limit)
	}
	if offset > 0 {
		return fmt.Sprintf("OFFSET %d", offset)
	}
	return ""
}

func (p *PostgresDialect) SupportsReturning() bool {
	return true
}

func (p *PostgresDialect) Returning(columns ...string) string {
	if len(columns) == 0 {
		return ""
	}
	quoted := make([]string, len(columns))
	for i, col := range columns {
		quoted[i] = p.Quote(col)
	}
	return "RETURNING " + strings.Join(quoted, ", ")
}

func (p *PostgresDialect) SupportsLastInsertID() bool {
	return false
}

func (p *PostgresDialect) LastInsertIDQuery(table, pkColumn string) string {
	return "" // Uses RETURNING
}

func (p *PostgresDialect) JSONExtract(column, path string) (string, []any, error) {
	if err := guard.ValidateJSONPath(path); err != nil {
		return "", nil, err
	}
	// Bind each path component as a separate text arg to the variadic
	// jsonb_extract_path_text(jsonb, VARIADIC text[]). The cast lets it work
	// on TEXT-typed columns too.
	parts := strings.Split(path, ".")
	markers := make([]string, len(parts))
	args := make([]any, len(parts))
	for i, seg := range parts {
		markers[i] = "?"
		args[i] = seg
	}
	return fmt.Sprintf("jsonb_extract_path_text((%s)::jsonb, %s)", p.Quote(column), strings.Join(markers, ", ")), args, nil
}

func (p *PostgresDialect) CurrentTimestamp() string {
	return "CURRENT_TIMESTAMP"
}

func (p *PostgresDialect) BuildRoutineQuery(routine string, argCount int) string {
	placeholders := strings.Join(p.Placeholders(argCount), ", ")
	return fmt.Sprintf("SELECT * FROM %s(%s)", p.Quote(routine), placeholders)
}

func (p *PostgresDialect) BuildProcedureCall(procedure string, argCount int) string {
	placeholders := strings.Join(p.Placeholders(argCount), ", ")
	return fmt.Sprintf("CALL %s(%s)", p.Quote(procedure), placeholders)
}

func (p *PostgresDialect) AlterTableAddColumn(table, column, dataType string) string {
	return fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", p.Quote(table), p.Quote(column), dataType)
}

func (p *PostgresDialect) AlterTableDropColumn(table, column string) string {
	return fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", p.Quote(table), p.Quote(column))
}

func (p *PostgresDialect) AlterTableAlterColumn(table, column, newDataType string) string {
	return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s TYPE %s", p.Quote(table), p.Quote(column), newDataType)
}

func (p *PostgresDialect) RenameColumn(table, oldName, newName string) string {
	return fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s", p.Quote(table), p.Quote(oldName), p.Quote(newName))
}

func (p *PostgresDialect) RenameTable(oldName, newName string) string {
	return fmt.Sprintf("ALTER TABLE %s RENAME TO %s", p.Quote(oldName), p.Quote(newName))
}

func (p *PostgresDialect) SupportsTransactionalDDL() bool {
	return true
}

// UpsertSQL for PostgreSQL: INSERT … ON CONFLICT (cols) DO UPDATE SET col = EXCLUDED.col
func (p *PostgresDialect) UpsertSQL(conflictCols, updateCols []string, _ int) string {
	if len(conflictCols) == 0 {
		return " ON CONFLICT DO NOTHING"
	}
	quoted := make([]string, len(conflictCols))
	for i, c := range conflictCols {
		quoted[i] = p.Quote(c)
	}
	conflict := strings.Join(quoted, ", ")
	if len(updateCols) == 0 {
		return fmt.Sprintf(" ON CONFLICT (%s) DO NOTHING", conflict)
	}
	sets := make([]string, len(updateCols))
	for i, c := range updateCols {
		sets[i] = fmt.Sprintf("%s = EXCLUDED.%s", p.Quote(c), p.Quote(c))
	}
	return fmt.Sprintf(" ON CONFLICT (%s) DO UPDATE SET %s", conflict, strings.Join(sets, ", "))
}

// MySQLDialect implements the MySQL dialect.
type MySQLDialect struct {
	baseDialect
}

// MySQL returns the MySQL dialect instance.
func MySQL() Dialect {
	return &MySQLDialect{
		baseDialect{name: "mysql"},
	}
}

func (m *MySQLDialect) Placeholder(index int) string {
	return "?"
}

func (m *MySQLDialect) JSONExtract(column, path string) (string, []any, error) {
	if err := guard.ValidateJSONPath(path); err != nil {
		return "", nil, err
	}
	return fmt.Sprintf("JSON_EXTRACT(%s, ?)", m.Quote(column)), []any{"$." + path}, nil
}

func (m *MySQLDialect) Placeholders(n int) []string {
	placeholders := make([]string, n)
	for i := 0; i < n; i++ {
		placeholders[i] = "?"
	}
	return placeholders
}

func (m *MySQLDialect) Quote(identifier string) string {
	return fmt.Sprintf("`%s`", identifier)
}

func (m *MySQLDialect) LimitOffset(limit, offset int) string {
	// MySQL uses LIMIT offset, count
	if limit > 0 && offset > 0 {
		return fmt.Sprintf("LIMIT %d, %d", offset, limit)
	}
	if limit > 0 {
		return fmt.Sprintf("LIMIT %d", limit)
	}
	return ""
}

func (m *MySQLDialect) SupportsReturning() bool {
	// MySQL 8.0.19+ supports RETURNING, but we'll use LastInsertId for compatibility
	return false
}

func (m *MySQLDialect) Returning(columns ...string) string {
	return ""
}

func (m *MySQLDialect) SupportsLastInsertID() bool {
	return true
}

func (m *MySQLDialect) LastInsertIDQuery(table, pkColumn string) string {
	return "SELECT LAST_INSERT_ID()"
}

func (m *MySQLDialect) CurrentTimestamp() string {
	return "CURRENT_TIMESTAMP"
}

func (m *MySQLDialect) BuildRoutineQuery(routine string, argCount int) string {
	placeholders := strings.Join(m.Placeholders(argCount), ", ")
	// MySQL uses CALL for everything, even if returning result sets
	return fmt.Sprintf("CALL %s(%s)", m.Quote(routine), placeholders)
}

func (m *MySQLDialect) BuildProcedureCall(procedure string, argCount int) string {
	placeholders := strings.Join(m.Placeholders(argCount), ", ")
	return fmt.Sprintf("CALL %s(%s)", m.Quote(procedure), placeholders)
}

func (m *MySQLDialect) AlterTableAddColumn(table, column, dataType string) string {
	return fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", m.Quote(table), m.Quote(column), dataType)
}

func (m *MySQLDialect) AlterTableDropColumn(table, column string) string {
	return fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", m.Quote(table), m.Quote(column))
}

func (m *MySQLDialect) AlterTableAlterColumn(table, column, newDataType string) string {
	// MySQL uses MODIFY for changing column types
	return fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s %s", m.Quote(table), m.Quote(column), newDataType)
}

func (m *MySQLDialect) RenameColumn(table, oldName, newName string) string {
	// MySQL 8.0+ supports RENAME COLUMN
	return fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s", m.Quote(table), m.Quote(oldName), m.Quote(newName))
}

func (m *MySQLDialect) RenameTable(oldName, newName string) string {
	return fmt.Sprintf("RENAME TABLE %s TO %s", m.Quote(oldName), m.Quote(newName))
}

func (m *MySQLDialect) SupportsTransactionalDDL() bool {
	// MySQL does not support transactional DDL
	return false
}

// UpsertSQL for MySQL: INSERT … ON DUPLICATE KEY UPDATE col = VALUES(col)
func (m *MySQLDialect) UpsertSQL(conflictCols, updateCols []string, _ int) string {
	if len(updateCols) == 0 {
		return " ON DUPLICATE KEY UPDATE " + m.Quote(conflictCols[0]) + " = VALUES(" + m.Quote(conflictCols[0]) + ")"
	}
	sets := make([]string, len(updateCols))
	for i, c := range updateCols {
		sets[i] = fmt.Sprintf("%s = VALUES(%s)", m.Quote(c), m.Quote(c))
	}
	return " ON DUPLICATE KEY UPDATE " + strings.Join(sets, ", ")
}

// SQLiteDialect implements the SQLite dialect.
type SQLiteDialect struct {
	baseDialect
}

// SQLite returns the SQLite dialect instance.
func SQLite() Dialect {
	return &SQLiteDialect{
		baseDialect{name: "sqlite"},
	}
}

func (s *SQLiteDialect) Placeholder(index int) string {
	return "?"
}

func (s *SQLiteDialect) Placeholders(n int) []string {
	placeholders := make([]string, n)
	for i := 0; i < n; i++ {
		placeholders[i] = "?"
	}
	return placeholders
}

func (s *SQLiteDialect) Quote(identifier string) string {
	return fmt.Sprintf(`"%s"`, identifier)
}

func (s *SQLiteDialect) LimitOffset(limit, offset int) string {
	if limit > 0 && offset > 0 {
		return fmt.Sprintf("LIMIT %d OFFSET %d", limit, offset)
	}
	if limit > 0 {
		return fmt.Sprintf("LIMIT %d", limit)
	}
	if offset > 0 {
		return fmt.Sprintf("OFFSET %d", offset)
	}
	return ""
}

func (s *SQLiteDialect) SupportsReturning() bool {
	// SQLite 3.35.0+ supports RETURNING
	return true
}

func (s *SQLiteDialect) Returning(columns ...string) string {
	if len(columns) == 0 {
		return ""
	}
	quoted := make([]string, len(columns))
	for i, col := range columns {
		quoted[i] = s.Quote(col)
	}
	return "RETURNING " + strings.Join(quoted, ", ")
}

func (s *SQLiteDialect) SupportsLastInsertID() bool {
	return true
}

func (s *SQLiteDialect) LastInsertIDQuery(table, pkColumn string) string {
	return "SELECT last_insert_rowid()"
}

func (s *SQLiteDialect) JSONExtract(column, path string) (string, []any, error) {
	if err := guard.ValidateJSONPath(path); err != nil {
		return "", nil, err
	}
	return fmt.Sprintf("JSON_EXTRACT(%s, ?)", s.Quote(column)), []any{"$." + path}, nil
}

func (s *SQLiteDialect) CurrentTimestamp() string {
	return "CURRENT_TIMESTAMP"
}

func (s *SQLiteDialect) BuildRoutineQuery(routine string, argCount int) string {
	placeholders := strings.Join(s.Placeholders(argCount), ", ")
	// SQLite has User-Defined Functions but not procedures, so we select it
	return fmt.Sprintf("SELECT * FROM %s(%s)", s.Quote(routine), placeholders)
}

func (s *SQLiteDialect) BuildProcedureCall(procedure string, argCount int) string {
	placeholders := strings.Join(s.Placeholders(argCount), ", ")
	// SQLite has no CALL, map to SELECT
	return fmt.Sprintf("SELECT %s(%s)", s.Quote(procedure), placeholders)
}

func (s *SQLiteDialect) AlterTableAddColumn(table, column, dataType string) string {
	return fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", s.Quote(table), s.Quote(column), dataType)
}

func (s *SQLiteDialect) AlterTableDropColumn(table, column string) string {
	// SQLite only supports DROP COLUMN since version 3.35.0
	return fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", s.Quote(table), s.Quote(column))
}

func (s *SQLiteDialect) AlterTableAlterColumn(table, column, newDataType string) string {
	// SQLite does not support ALTER COLUMN directly
	// Would require table recreation in practice
	return fmt.Sprintf("-- SQLite does not support ALTER COLUMN: ALTER TABLE %s ALTER COLUMN %s TYPE %s", s.Quote(table), s.Quote(column), newDataType)
}

func (s *SQLiteDialect) RenameColumn(table, oldName, newName string) string {
	return fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s", s.Quote(table), s.Quote(oldName), s.Quote(newName))
}

func (s *SQLiteDialect) RenameTable(oldName, newName string) string {
	return fmt.Sprintf("ALTER TABLE %s RENAME TO %s", s.Quote(oldName), s.Quote(newName))
}

func (s *SQLiteDialect) SupportsTransactionalDDL() bool {
	// SQLite supports transactional DDL
	return true
}

// UpsertSQL for SQLite: ON CONFLICT (cols) DO UPDATE SET col = excluded.col
func (s *SQLiteDialect) UpsertSQL(conflictCols, updateCols []string, _ int) string {
	if len(conflictCols) == 0 {
		return " ON CONFLICT DO NOTHING"
	}
	quoted := make([]string, len(conflictCols))
	for i, c := range conflictCols {
		quoted[i] = s.Quote(c)
	}
	conflict := strings.Join(quoted, ", ")
	if len(updateCols) == 0 {
		return fmt.Sprintf(" ON CONFLICT (%s) DO NOTHING", conflict)
	}
	sets := make([]string, len(updateCols))
	for i, c := range updateCols {
		sets[i] = fmt.Sprintf("%s = excluded.%s", s.Quote(c), s.Quote(c))
	}
	return fmt.Sprintf(" ON CONFLICT (%s) DO UPDATE SET %s", conflict, strings.Join(sets, ", "))
}

// MSSQLDialect implements the Microsoft SQL Server dialect.
type MSSQLDialect struct {
	baseDialect
}

// MSSQL returns the Microsoft SQL Server dialect instance.
func MSSQL() Dialect {
	return &MSSQLDialect{
		baseDialect{name: "mssql"},
	}
}

func (m *MSSQLDialect) Placeholder(index int) string {
	return fmt.Sprintf("@p%d", index)
}

func (m *MSSQLDialect) Placeholders(n int) []string {
	placeholders := make([]string, n)
	for i := 0; i < n; i++ {
		placeholders[i] = m.Placeholder(i + 1)
	}
	return placeholders
}

func (m *MSSQLDialect) Quote(identifier string) string {
	return fmt.Sprintf("[%s]", identifier)
}

func (m *MSSQLDialect) LimitOffset(limit, offset int) string {
	// MSSQL 2012+ uses OFFSET x ROWS FETCH NEXT y ROWS ONLY
	// Note: This REQUIRES an ORDER BY clause in the query.
	if limit > 0 && offset >= 0 {
		return fmt.Sprintf("OFFSET %d ROWS FETCH NEXT %d ROWS ONLY", offset, limit)
	}
	if offset > 0 {
		return fmt.Sprintf("OFFSET %d ROWS", offset)
	}
	return ""
}

func (m *MSSQLDialect) SupportsReturning() bool {
	// MSSQL supports OUTPUT clause, but it has different syntax (middle of query)
	// We'll use LastInsertId() which is supported by most drivers via SCOPE_IDENTITY()
	return false
}

func (m *MSSQLDialect) Returning(columns ...string) string {
	return ""
}

func (m *MSSQLDialect) SupportsLastInsertID() bool {
	return true
}

func (m *MSSQLDialect) LastInsertIDQuery(table, pkColumn string) string {
	return "SELECT SCOPE_IDENTITY()"
}

func (m *MSSQLDialect) JSONExtract(column, path string) (string, []any, error) {
	if err := guard.ValidateJSONPath(path); err != nil {
		return "", nil, err
	}
	return fmt.Sprintf("JSON_VALUE(%s, ?)", m.Quote(column)), []any{"$." + path}, nil
}

func (m *MSSQLDialect) CurrentTimestamp() string {
	return "GETDATE()"
}

func (m *MSSQLDialect) BuildRoutineQuery(routine string, argCount int) string {
	placeholders := strings.Join(m.Placeholders(argCount), ", ")
	return fmt.Sprintf("SELECT * FROM %s(%s)", m.Quote(routine), placeholders)
}

func (m *MSSQLDialect) BuildProcedureCall(procedure string, argCount int) string {
	placeholders := strings.Join(m.Placeholders(argCount), ", ")
	return fmt.Sprintf("EXEC %s %s", m.Quote(procedure), placeholders)
}

func (m *MSSQLDialect) AlterTableAddColumn(table, column, dataType string) string {
	return fmt.Sprintf("ALTER TABLE %s ADD %s %s", m.Quote(table), m.Quote(column), dataType)
}

func (m *MSSQLDialect) AlterTableDropColumn(table, column string) string {
	return fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", m.Quote(table), m.Quote(column))
}

func (m *MSSQLDialect) AlterTableAlterColumn(table, column, newDataType string) string {
	// MSSQL uses ALTER COLUMN
	return fmt.Sprintf("ALTER TABLE %s ALTER COLUMN %s %s", m.Quote(table), m.Quote(column), newDataType)
}

func (m *MSSQLDialect) RenameColumn(table, oldName, newName string) string {
	// MSSQL requires sp_rename stored procedure for renaming columns
	return fmt.Sprintf("EXEC sp_rename '%s.%s', '%s', 'COLUMN'", table, oldName, newName)
}

func (m *MSSQLDialect) RenameTable(oldName, newName string) string {
	return fmt.Sprintf("EXEC sp_rename '%s', '%s'", oldName, newName)
}

func (m *MSSQLDialect) SupportsTransactionalDDL() bool {
	// MSSQL supports transactional DDL
	return true
}

// UpsertSQL for MSSQL: uses MERGE statement appended as a WITH-style hint.
// MSSQL requires MERGE syntax which cannot be appended to a plain INSERT,
// so we return a marker that buildUpsert handles specially.
func (m *MSSQLDialect) UpsertSQL(conflictCols, updateCols []string, _ int) string {
	// MSSQL MERGE is built separately in buildUpsert — return empty to signal that.
	return ""
}

// OracleDialect implements the Oracle Database dialect.
type OracleDialect struct {
	baseDialect
}

// Oracle returns the Oracle Database dialect instance.
func Oracle() Dialect {
	return &OracleDialect{
		baseDialect{name: "oracle"},
	}
}

func (o *OracleDialect) Placeholder(index int) string {
	return fmt.Sprintf(":%d", index)
}

func (o *OracleDialect) Placeholders(n int) []string {
	placeholders := make([]string, n)
	for i := 0; i < n; i++ {
		placeholders[i] = o.Placeholder(i + 1)
	}
	return placeholders
}

func (o *OracleDialect) Quote(identifier string) string {
	return fmt.Sprintf(`"%s"`, strings.ToUpper(identifier))
}

func (o *OracleDialect) LimitOffset(limit, offset int) string {
	// Oracle 12c+ supports OFFSET/FETCH
	if limit > 0 && offset >= 0 {
		return fmt.Sprintf("OFFSET %d ROWS FETCH NEXT %d ROWS ONLY", offset, limit)
	}
	if offset > 0 {
		return fmt.Sprintf("OFFSET %d ROWS", offset)
	}
	return ""
}

func (o *OracleDialect) SupportsReturning() bool {
	return true
}

func (o *OracleDialect) Returning(columns ...string) string {
	if len(columns) == 0 {
		return ""
	}
	quoted := make([]string, len(columns))
	for i, col := range columns {
		quoted[i] = o.Quote(col)
	}
	return "RETURNING " + strings.Join(quoted, ", ")
}

func (o *OracleDialect) SupportsLastInsertID() bool {
	return false
}

func (o *OracleDialect) LastInsertIDQuery(table, pkColumn string) string {
	return ""
}

func (o *OracleDialect) CurrentTimestamp() string {
	return "SYSDATE"
}

func (o *OracleDialect) BuildRoutineQuery(routine string, argCount int) string {
	placeholders := strings.Join(o.Placeholders(argCount), ", ")
	return fmt.Sprintf("SELECT * FROM TABLE(%s(%s))", o.Quote(routine), placeholders)
}

func (o *OracleDialect) BuildProcedureCall(procedure string, argCount int) string {
	placeholders := strings.Join(o.Placeholders(argCount), ", ")
	return fmt.Sprintf("BEGIN %s(%s); END;", o.Quote(procedure), placeholders)
}

func (o *OracleDialect) AlterTableAddColumn(table, column, dataType string) string {
	return fmt.Sprintf("ALTER TABLE %s ADD %s %s", o.Quote(table), o.Quote(column), dataType)
}

func (o *OracleDialect) AlterTableDropColumn(table, column string) string {
	return fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", o.Quote(table), o.Quote(column))
}

func (o *OracleDialect) AlterTableAlterColumn(table, column, newDataType string) string {
	// Oracle uses MODIFY for changing column types
	return fmt.Sprintf("ALTER TABLE %s MODIFY %s %s", o.Quote(table), o.Quote(column), newDataType)
}

func (o *OracleDialect) RenameColumn(table, oldName, newName string) string {
	return fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s", o.Quote(table), o.Quote(oldName), o.Quote(newName))
}

func (o *OracleDialect) RenameTable(oldName, newName string) string {
	return fmt.Sprintf("ALTER TABLE %s RENAME TO %s", o.Quote(oldName), o.Quote(newName))
}

func (o *OracleDialect) SupportsTransactionalDDL() bool {
	// Oracle supports transactional DDL
	return true
}

// UpsertSQL for Oracle: MERGE syntax — same as MSSQL, built separately.
func (o *OracleDialect) UpsertSQL(conflictCols, updateCols []string, _ int) string {
	return ""
}

func (o *OracleDialect) JSONExtract(column, path string) (string, []any, error) {
	if err := guard.ValidateJSONPath(path); err != nil {
		return "", nil, err
	}
	return fmt.Sprintf("JSON_VALUE(%s, ?)", o.Quote(column)), []any{"$." + path}, nil
}

// MariaDBDialect implements the MariaDB dialect.
// MariaDB is a fork of MySQL with significant additions:
//   - RETURNING clause in INSERT/DELETE/UPDATE (10.5+)
//   - Native sequences via CREATE SEQUENCE (10.3+)
//   - Temporal tables / system-versioned tables (10.3.4+)
//   - JSON_TABLE support (10.6+)
//   - INTERSECT / EXCEPT set operations (10.3+)
//   - Descending indexes (10.6+)
//   - UUID() and UUID_SHORT() built-ins
//   - IGNORE INDEX / USE INDEX hints identical to MySQL
type MariaDBDialect struct {
	MySQLDialect // embed MySQL — identical wire protocol and driver
}

// MariaDB returns a MariaDB dialect instance.
func MariaDB() Dialect {
	return &MariaDBDialect{
		MySQLDialect: MySQLDialect{
			baseDialect: baseDialect{name: "mariadb"},
		},
	}
}

// SupportsReturning returns true: MariaDB 10.5+ supports RETURNING in
// INSERT … RETURNING, DELETE … RETURNING and UPDATE … RETURNING.
func (m *MariaDBDialect) SupportsReturning() bool {
	return true
}

// Returning generates a RETURNING clause compatible with MariaDB 10.5+.
func (m *MariaDBDialect) Returning(columns ...string) string {
	if len(columns) == 0 {
		return ""
	}
	quoted := make([]string, len(columns))
	for i, col := range columns {
		quoted[i] = m.Quote(col)
	}
	return "RETURNING " + strings.Join(quoted, ", ")
}

// SupportsLastInsertID returns false when RETURNING is used.
// The ORM prefers RETURNING over LAST_INSERT_ID() for MariaDB.
func (m *MariaDBDialect) SupportsLastInsertID() bool {
	return false
}

// LastInsertIDQuery is kept as fallback for engines older than 10.5.
func (m *MariaDBDialect) LastInsertIDQuery(table, pkColumn string) string {
	return "SELECT LAST_INSERT_ID()"
}

// JSONExtract uses the MariaDB / MySQL JSON_VALUE syntax (10.2.3+).
// MariaDB also accepts the arrow operator col->>'$.key' from 10.4.3+.
func (m *MariaDBDialect) JSONExtract(column, path string) (string, []any, error) {
	if err := guard.ValidateJSONPath(path); err != nil {
		return "", nil, err
	}
	return fmt.Sprintf("JSON_VALUE(%s, ?)", m.Quote(column)), []any{"$." + path}, nil
}

// CreateSequence returns the DDL to create a named sequence (MariaDB 10.3+).
func (m *MariaDBDialect) CreateSequence(name string, start, increment int64) string {
	return fmt.Sprintf("CREATE SEQUENCE IF NOT EXISTS %s START WITH %d INCREMENT BY %d",
		m.Quote(name), start, increment)
}

// NextVal returns the SQL expression that reads the next value from a sequence.
func (m *MariaDBDialect) NextVal(sequenceName string) string {
	return fmt.Sprintf("NEXTVAL(%s)", m.Quote(sequenceName))
}

// CreateSystemVersionedTable returns the DDL for a system-versioned (temporal) table.
// Requires MariaDB 10.3.4+.
func (m *MariaDBDialect) CreateSystemVersionedTable(table string, columnDefs string) string {
	return fmt.Sprintf(
		"CREATE TABLE IF NOT EXISTS %s (\n%s\n) WITH SYSTEM VERSIONING",
		m.Quote(table), columnDefs,
	)
}

// HistoryQuery returns SELECT … FOR SYSTEM_TIME ALL to query full row history.
func (m *MariaDBDialect) HistoryQuery(table string) string {
	return fmt.Sprintf("SELECT * FROM %s FOR SYSTEM_TIME ALL", m.Quote(table))
}

// HistoryBetween returns SELECT … FOR SYSTEM_TIME BETWEEN for a time range.
func (m *MariaDBDialect) HistoryBetween(table, from, to string) string {
	return fmt.Sprintf(
		"SELECT * FROM %s FOR SYSTEM_TIME BETWEEN '%s' AND '%s'",
		m.Quote(table), from, to,
	)
}

// JSONTable returns a JSON_TABLE expression (MariaDB 10.6+).
// source: SQL expression producing JSON; path: root path e.g. '$[*]';
// columns: column definitions e.g. "id INT PATH '$.id'".
//
// The path is validated against guard.ValidateJSONTablePath (JSONPath grammar
// rooted at "$"). source and columns must be trusted strings — the JSON_TABLE
// row syntax intermixes column types and PATH literals, so binding it as a
// parameter is not possible. Callers MUST NOT pass user-controlled values for
// source or columns. If invalid, the returned SQL embeds an obvious sentinel
// that fails parsing at execution time, surfacing the misuse rather than
// silently producing executable injection.
//
// TODO(public-api): when JSONTable graduates from internal-only to a public
// builder, change the signature to return (string, error) so callers can
// detect validation failure with errors.Is rather than scanning the SQL for
// JSON_TABLE_PATH_INVALID.
func (m *MariaDBDialect) JSONTable(source, path string, columns ...string) string {
	if err := guard.ValidateJSONTablePath(path); err != nil {
		return fmt.Sprintf("/* %s */ JSON_TABLE_PATH_INVALID", err.Error())
	}
	cols := strings.Join(columns, ",\n  ")
	return fmt.Sprintf("JSON_TABLE(%s, '%s' COLUMNS (\n  %s\n))", source, path, cols)
}

// LimitOffset for MariaDB uses standard LIMIT … OFFSET … syntax
// (unlike MySQL which uses LIMIT offset, count).
func (m *MariaDBDialect) LimitOffset(limit, offset int) string {
	if limit > 0 && offset > 0 {
		return fmt.Sprintf("LIMIT %d OFFSET %d", limit, offset)
	}
	if limit > 0 {
		return fmt.Sprintf("LIMIT %d", limit)
	}
	if offset > 0 {
		return fmt.Sprintf("OFFSET %d", offset)
	}
	return ""
}

// RenameColumn uses the standard SQL syntax supported since MariaDB 10.4.2.
func (m *MariaDBDialect) RenameColumn(table, oldName, newName string) string {
	return fmt.Sprintf("ALTER TABLE %s RENAME COLUMN %s TO %s",
		m.Quote(table), m.Quote(oldName), m.Quote(newName))
}

// AlterTableAlterColumn uses MODIFY COLUMN (same as MySQL).
func (m *MariaDBDialect) AlterTableAlterColumn(table, column, newDataType string) string {
	return fmt.Sprintf("ALTER TABLE %s MODIFY COLUMN %s %s",
		m.Quote(table), m.Quote(column), newDataType)
}

// SupportsTransactionalDDL returns false — MariaDB (like MySQL) performs
// implicit commits around DDL statements.
func (m *MariaDBDialect) SupportsTransactionalDDL() bool {
	return false
}

// UpsertSQL for MariaDB: INSERT … ON DUPLICATE KEY UPDATE (same as MySQL)
func (m *MariaDBDialect) UpsertSQL(conflictCols, updateCols []string, argOffset int) string {
	return m.MySQLDialect.UpsertSQL(conflictCols, updateCols, argOffset)
}

// customDialectRegistry holds user-registered dialects
var customDialectRegistry = make(map[string]Dialect)

// RegisterDialect allows developers to register custom database dialects.
// This enables support for proprietary or non-standard databases.
//
// Example:
//
//	quark.RegisterDialect("cockroach", myCockroachDialect)
//
// The registered dialect can then be used with:
//
//	client, err := quark.New(db, quark.WithDialect(quark.DetectDialectByName("cockroach")))
func RegisterDialect(name string, d Dialect) {
	customDialectRegistry[name] = d
}

// DetectDialect attempts to auto-detect the dialect from a driver name.
func DetectDialect(driverName string) (Dialect, error) {
	// First check custom registry
	if d, ok := customDialectRegistry[driverName]; ok {
		return d, nil
	}

	switch driverName {
	case "postgres", "pgx", "pgx/v5", "pq":
		return PostgreSQL(), nil
	case "mysql":
		return MySQL(), nil
	case "mariadb":
		return MariaDB(), nil
	case "sqlite", "sqlite3", "modernc":
		return SQLite(), nil
	case "mssql", "sqlserver", "azuresql":
		return MSSQL(), nil
	case "oracle", "godror", "oci8":
		return Oracle(), nil
	default:
		return nil, fmt.Errorf("%w: %s", ErrDialectNotSupported, driverName)
	}
}

// DetectDialectByName attempts to get a dialect by name from all registered dialects
// including custom ones. This is useful when you know the exact dialect name.
func DetectDialectByName(name string) (Dialect, error) {
	// First check custom registry
	if d, ok := customDialectRegistry[name]; ok {
		return d, nil
	}

	// Fall back to standard detection
	return DetectDialect(name)
}
