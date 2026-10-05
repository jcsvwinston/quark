// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quark

import (
	"fmt"
	"strings"

	"github.com/jcsvwinston/quark/internal/guard"
	"github.com/jcsvwinston/quark/quarkdriver"
)

// Dialect is the interface a database dialect implements: placeholder
// syntax, identifier quoting, pagination, upsert, locking and DDL. Each
// supported database (PostgreSQL, MySQL, SQLite, etc.) implements it.
//
// It is declared in quarkdriver, the leaf package a driver module imports
// (ADR-0026), where its methods are documented; this name is an alias of
// the same type. A dialect written against quark.Dialect is a
// quarkdriver.Dialect and the reverse, so an application keeps writing
// quark.WithDialect(quark.PostgreSQL()) and a driver module implements the
// interface without importing package quark.
type Dialect = quarkdriver.Dialect

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
	// Defense in depth (H-Q7): identifiers reach Quote already vetted by
	// guard.ValidateIdentifier on the main paths, but Quote itself must not
	// depend on that — doubling the closing quote makes an embedded `"`
	// inert instead of an identifier-injection point for any future
	// call-site that forgets to validate.
	return fmt.Sprintf(`"%s"`, strings.ReplaceAll(identifier, `"`, `""`))
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
	// Self-escaping quote — see PostgresDialect.Quote (H-Q7).
	return fmt.Sprintf("`%s`", strings.ReplaceAll(identifier, "`", "``"))
}

func (m *MySQLDialect) LimitOffset(limit, offset int) string {
	// MySQL uses LIMIT offset, count
	if limit > 0 && offset > 0 {
		return fmt.Sprintf("LIMIT %d, %d", offset, limit)
	}
	if limit > 0 {
		return fmt.Sprintf("LIMIT %d", limit)
	}
	if offset > 0 {
		// MySQL has no OFFSET-without-LIMIT form; the manual's documented
		// idiom for "skip n, read to the end" is LIMIT with the max
		// unsigned-64 row count. Without this the offset was silently
		// dropped and the query returned rows the caller asked to skip.
		return fmt.Sprintf("LIMIT 18446744073709551615 OFFSET %d", offset)
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
		if len(conflictCols) == 0 {
			// Defensive: Upsert/UpsertBatch reject empty conflictCols before
			// reaching any dialect, but indexing conflictCols[0] here must
			// never be a panic path for a future caller that doesn't.
			return ""
		}
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
	// Self-escaping quote — see PostgresDialect.Quote (H-Q7).
	return fmt.Sprintf(`"%s"`, strings.ReplaceAll(identifier, `"`, `""`))
}

func (s *SQLiteDialect) LimitOffset(limit, offset int) string {
	if limit > 0 && offset > 0 {
		return fmt.Sprintf("LIMIT %d OFFSET %d", limit, offset)
	}
	if limit > 0 {
		return fmt.Sprintf("LIMIT %d", limit)
	}
	if offset > 0 {
		// SQLite's grammar has no bare OFFSET — it must hang off a LIMIT,
		// and a negative LIMIT means "no limit". The previous bare
		// `OFFSET n` was a syntax error at exec time.
		return fmt.Sprintf("LIMIT -1 OFFSET %d", offset)
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
	// Self-escaping quote — see PostgresDialect.Quote (H-Q7). Only the
	// closing bracket terminates a T-SQL bracketed identifier, so `[`
	// needs no escaping.
	return fmt.Sprintf("[%s]", strings.ReplaceAll(identifier, "]", "]]"))
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
	// Self-escaping quote — see PostgresDialect.Quote (H-Q7).
	return fmt.Sprintf(`"%s"`, strings.ReplaceAll(strings.ToUpper(identifier), `"`, `""`))
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

// SupportsTransactionalDDL returns false: Oracle commits implicitly before
// and after every DDL statement, so a ROLLBACK does not undo one. It said
// true until A11 Q2, and nothing acted on the answer — ApplyPlan decided by
// the dialect's name, which put Oracle on the resumable path; since it asks
// this method, the answer has to be the engine's.
func (o *OracleDialect) SupportsTransactionalDDL() bool {
	return false
}

// MapColumnType translates a neutral column-type string into Oracle's
// native form. Oracle has no TEXT type (`ORA-00902: invalid datatype`),
// so the generic TEXT that other engines accept becomes CLOB — the
// semantic equivalent for unbounded text. Already-native types pass
// through unchanged, so this is safe to apply to every column type.
func (o *OracleDialect) MapColumnType(t string) string {
	switch strings.ToUpper(strings.TrimSpace(t)) {
	case "TEXT":
		return "CLOB"
	}
	return t
}

// UpsertSQL for Oracle: MERGE syntax — same as MSSQL, built separately.
func (o *OracleDialect) UpsertSQL(conflictCols, updateCols []string, _ int) string {
	return ""
}

func (o *OracleDialect) JSONExtract(column, path string) (string, []any, error) {
	if err := guard.ValidateJSONPath(path); err != nil {
		return "", nil, err
	}
	// Oracle's JSON_VALUE requires the path as a string literal; binding it
	// raises ORA-40454 ("path expression not a literal"). The path cannot be
	// parameterised, so it is concatenated like an identifier. This stays
	// injection-safe because ValidateJSONPath restricts path to [A-Za-z0-9_.]
	// — the same guarantee that makes Quote(validatedIdentifier) safe.
	return fmt.Sprintf("JSON_VALUE(%s, '$.%s')", o.Quote(column), path), nil, nil
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
		// Like MySQL, MariaDB's OFFSET only exists as part of LIMIT; the
		// bare `OFFSET n` this used to emit is a syntax error. Same
		// max-uint64 sentinel idiom as MySQLDialect.LimitOffset.
		return fmt.Sprintf("LIMIT 18446744073709551615 OFFSET %d", offset)
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

// RegisterDialect allows developers to register custom database dialects.
// This enables support for proprietary or non-standard databases.
//
// Example:
//
//	quark.RegisterDialect("cockroach", myCockroachDialect)
//
// A client opened with that driver name resolves it through the registry:
//
//	client, err := quark.New("cockroach", dsn)
//
// and a pool opened under another driver name takes it explicitly:
//
//	d, _ := quark.DetectDialectByName("cockroach")
//	client, err := quark.NewWithDB("pgx", db, quark.WithDialect(d))
//
// The registry is quarkdriver's (ADR-0026): RegisterDialect calls
// [quarkdriver.RegisterDialect], so a dialect a driver module registers
// there without importing this package and one registered here are found
// the same way. RegisterDialect is safe to call concurrently with itself,
// [DetectDialect] and [DetectDialectByName]. Registering a name again
// replaces the dialect registered under it.
func RegisterDialect(name string, d Dialect) {
	quarkdriver.RegisterDialect(name, d)
}

// DetectDialect attempts to auto-detect the dialect from a driver name.
func DetectDialect(driverName string) (Dialect, error) {
	// First check the registry: a registered name wins over a built-in one.
	if d, ok := quarkdriver.LookupDialect(driverName); ok {
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
	// First check the registry
	if d, ok := quarkdriver.LookupDialect(name); ok {
		return d, nil
	}

	// Fall back to standard detection
	return DetectDialect(name)
}

// SavepointDialect is the optional [Dialect] extension for engines whose
// savepoint statements diverge from the ANSI form (SAVEPOINT /
// ROLLBACK TO SAVEPOINT / RELEASE SAVEPOINT); the transaction layer
// ([Tx.Savepoint], [Tx.RollbackTo], [Tx.ReleaseSavepoint]) asserts it on
// the dialect. It is declared, and documented, in quarkdriver (ADR-0026);
// this name is an alias of the same type.
type SavepointDialect = quarkdriver.SavepointDialect

// --- SQL Server: SAVE TRANSACTION / ROLLBACK TRANSACTION, no release ---

func (m *MSSQLDialect) SavepointStmt(name string) string { return "SAVE TRANSACTION " + name }
func (m *MSSQLDialect) RollbackToSavepointStmt(name string) string {
	return "ROLLBACK TRANSACTION " + name
}
func (m *MSSQLDialect) ReleaseSavepointStmt(name string) string { return "" }

// --- Oracle: ANSI SAVEPOINT / ROLLBACK TO SAVEPOINT, but no RELEASE ---

func (o *OracleDialect) SavepointStmt(name string) string { return "SAVEPOINT " + o.Quote(name) }
func (o *OracleDialect) RollbackToSavepointStmt(name string) string {
	return "ROLLBACK TO SAVEPOINT " + o.Quote(name)
}
func (o *OracleDialect) ReleaseSavepointStmt(name string) string { return "" }
