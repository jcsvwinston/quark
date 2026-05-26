package quark_test

import (
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
)

// assertLO verifies LimitOffset output (helper used by all dialect subtests below).
func assertLO(t *testing.T, d quark.Dialect, limit, offset int, want string) {
	t.Helper()
	if got := d.LimitOffset(limit, offset); got != want {
		t.Errorf("%s.LimitOffset(%d,%d)=%q want %q", d.Name(), limit, offset, got, want)
	}
}

// ---------------------------------------------------------------------------
// PostgreSQL dialect
// ---------------------------------------------------------------------------

func TestPostgresDialect_Full(t *testing.T) {
	d := quark.PostgreSQL()

	if d.Name() != "postgres" {
		t.Errorf("Name = %q", d.Name())
	}
	if d.Placeholder(1) != "$1" || d.Placeholder(3) != "$3" {
		t.Error("Placeholder")
	}
	if ps := d.Placeholders(3); len(ps) != 3 || ps[0] != "$1" || ps[2] != "$3" {
		t.Error("Placeholders")
	}
	if d.Quote("tbl") != `"tbl"` {
		t.Error("Quote")
	}
	assertLO(t, d, 10, 5, "LIMIT 10 OFFSET 5")
	assertLO(t, d, 10, 0, "LIMIT 10")
	assertLO(t, d, 0, 5, "OFFSET 5")
	assertLO(t, d, 0, 0, "")
	if !d.SupportsReturning() {
		t.Error("SupportsReturning")
	}
	if d.Returning() != "" {
		t.Error("Returning no cols")
	}
	if !strings.HasPrefix(d.Returning("id", "name"), "RETURNING") {
		t.Error("Returning with cols")
	}
	if d.SupportsLastInsertID() {
		t.Error("SupportsLastInsertID should be false")
	}
	if d.LastInsertIDQuery("t", "id") != "" {
		t.Error("LastInsertIDQuery")
	}
	if sql, args, err := d.JSONExtract("data", "key"); err != nil || !strings.Contains(sql, "jsonb") || len(args) == 0 {
		t.Errorf("JSONExtract: sql=%q args=%v err=%v", sql, args, err)
	}
	if d.CurrentTimestamp() == "" {
		t.Error("CurrentTimestamp")
	}
	if !strings.Contains(d.BuildRoutineQuery("fn", 2), "$1") {
		t.Error("BuildRoutineQuery")
	}
	if !strings.Contains(d.BuildProcedureCall("proc", 2), "CALL") {
		t.Error("BuildProcedureCall")
	}
	if !strings.Contains(d.AlterTableAddColumn("t", "col", "TEXT"), "ADD COLUMN") {
		t.Error("AlterTableAddColumn")
	}
	if !strings.Contains(d.AlterTableDropColumn("t", "col"), "DROP COLUMN") {
		t.Error("AlterTableDropColumn")
	}
	if !strings.Contains(d.AlterTableAlterColumn("t", "col", "INT"), "ALTER COLUMN") {
		t.Error("AlterTableAlterColumn")
	}
	if !strings.Contains(d.RenameColumn("t", "old", "new"), "RENAME COLUMN") {
		t.Error("RenameColumn")
	}
	if !strings.Contains(d.RenameTable("t", "t2"), "RENAME TO") {
		t.Error("RenameTable")
	}
	if !d.SupportsTransactionalDDL() {
		t.Error("SupportsTransactionalDDL")
	}
	// UpsertSQL variants
	if u := d.UpsertSQL(nil, nil, 1); !strings.Contains(u, "DO NOTHING") {
		t.Errorf("UpsertSQL empty conflict: %q", u)
	}
	if u := d.UpsertSQL([]string{"id"}, nil, 1); !strings.Contains(u, "DO NOTHING") {
		t.Errorf("UpsertSQL no updates: %q", u)
	}
	if u := d.UpsertSQL([]string{"id"}, []string{"name"}, 1); !strings.Contains(u, "DO UPDATE SET") {
		t.Errorf("UpsertSQL with updates: %q", u)
	}
}

// ---------------------------------------------------------------------------
// MySQL dialect
// ---------------------------------------------------------------------------

func TestMySQLDialect(t *testing.T) {
	d := quark.MySQL()

	if d.Name() != "mysql" {
		t.Errorf("Name = %q", d.Name())
	}
	if d.Placeholder(1) != "?" {
		t.Error("Placeholder")
	}
	if ps := d.Placeholders(2); ps[0] != "?" || ps[1] != "?" {
		t.Error("Placeholders")
	}
	if d.Quote("t") != "`t`" {
		t.Error("Quote")
	}
	assertLO(t, d, 5, 10, "LIMIT 10, 5") // MySQL uses LIMIT offset, count
	assertLO(t, d, 5, 0, "LIMIT 5")
	assertLO(t, d, 0, 5, "")
	assertLO(t, d, 0, 0, "")
	if d.SupportsReturning() {
		t.Error("SupportsReturning")
	}
	if d.Returning("id") != "" {
		t.Error("Returning")
	}
	if !d.SupportsLastInsertID() {
		t.Error("SupportsLastInsertID")
	}
	if d.LastInsertIDQuery("t", "id") == "" {
		t.Error("LastInsertIDQuery")
	}
	if sql, args, err := d.JSONExtract("data", "key"); err != nil || !strings.Contains(sql, "JSON_EXTRACT") || len(args) == 0 {
		t.Errorf("JSONExtract: sql=%q args=%v err=%v", sql, args, err)
	}
	if d.CurrentTimestamp() == "" {
		t.Error("CurrentTimestamp")
	}
	if !strings.Contains(d.BuildRoutineQuery("fn", 1), "CALL") {
		t.Error("BuildRoutineQuery")
	}
	if !strings.Contains(d.BuildProcedureCall("p", 1), "CALL") {
		t.Error("BuildProcedureCall")
	}
	if !strings.Contains(d.AlterTableAddColumn("t", "c", "INT"), "ADD COLUMN") {
		t.Error("AlterTableAddColumn")
	}
	if !strings.Contains(d.AlterTableDropColumn("t", "c"), "DROP COLUMN") {
		t.Error("AlterTableDropColumn")
	}
	if !strings.Contains(d.AlterTableAlterColumn("t", "c", "INT"), "MODIFY COLUMN") {
		t.Error("AlterTableAlterColumn")
	}
	if !strings.Contains(d.RenameColumn("t", "a", "b"), "RENAME COLUMN") {
		t.Error("RenameColumn")
	}
	if !strings.Contains(d.RenameTable("a", "b"), "RENAME TABLE") {
		t.Error("RenameTable")
	}
	if d.SupportsTransactionalDDL() {
		t.Error("SupportsTransactionalDDL should be false")
	}
	if u := d.UpsertSQL([]string{"id"}, nil, 1); !strings.Contains(u, "DUPLICATE KEY") {
		t.Errorf("UpsertSQL no updates: %q", u)
	}
	if u := d.UpsertSQL([]string{"id"}, []string{"name"}, 1); !strings.Contains(u, "VALUES") {
		t.Errorf("UpsertSQL with updates: %q", u)
	}
}

// ---------------------------------------------------------------------------
// SQLite dialect
// ---------------------------------------------------------------------------

func TestSQLiteDialect(t *testing.T) {
	d := quark.SQLite()

	if d.Name() != "sqlite" {
		t.Errorf("Name = %q", d.Name())
	}
	if d.Placeholder(2) != "?" {
		t.Error("Placeholder")
	}
	if ps := d.Placeholders(3); ps[2] != "?" {
		t.Error("Placeholders")
	}
	assertLO(t, d, 5, 10, "LIMIT 5 OFFSET 10")
	assertLO(t, d, 5, 0, "LIMIT 5")
	assertLO(t, d, 0, 5, "OFFSET 5")
	assertLO(t, d, 0, 0, "")
	if !d.SupportsReturning() {
		t.Error("SupportsReturning")
	}
	if d.Returning() != "" {
		t.Error("Returning empty")
	}
	if r := d.Returning("id"); !strings.HasPrefix(r, "RETURNING") {
		t.Error("Returning with col")
	}
	if !d.SupportsLastInsertID() {
		t.Error("SupportsLastInsertID")
	}
	if d.LastInsertIDQuery("t", "id") == "" {
		t.Error("LastInsertIDQuery")
	}
	if sql, args, err := d.JSONExtract("d", "k"); err != nil || !strings.Contains(sql, "JSON_EXTRACT") || len(args) == 0 {
		t.Errorf("JSONExtract: sql=%q args=%v err=%v", sql, args, err)
	}
	if d.CurrentTimestamp() == "" {
		t.Error("CurrentTimestamp")
	}
	if !strings.Contains(d.BuildRoutineQuery("fn", 2), "SELECT") {
		t.Error("BuildRoutineQuery")
	}
	if !strings.Contains(d.BuildProcedureCall("p", 1), "SELECT") {
		t.Error("BuildProcedureCall")
	}
	if !strings.Contains(d.AlterTableAddColumn("t", "c", "TEXT"), "ADD COLUMN") {
		t.Error("AlterTableAddColumn")
	}
	if !strings.Contains(d.AlterTableDropColumn("t", "c"), "DROP COLUMN") {
		t.Error("AlterTableDropColumn")
	}
	if s := d.AlterTableAlterColumn("t", "c", "INT"); !strings.Contains(s, "SQLite") {
		t.Error("AlterTableAlterColumn should have comment")
	}
	if !strings.Contains(d.RenameColumn("t", "a", "b"), "RENAME COLUMN") {
		t.Error("RenameColumn")
	}
	if !strings.Contains(d.RenameTable("a", "b"), "RENAME TO") {
		t.Error("RenameTable")
	}
	if !d.SupportsTransactionalDDL() {
		t.Error("SupportsTransactionalDDL")
	}
	if u := d.UpsertSQL(nil, nil, 1); !strings.Contains(u, "DO NOTHING") {
		t.Errorf("UpsertSQL empty: %q", u)
	}
	if u := d.UpsertSQL([]string{"id"}, nil, 1); !strings.Contains(u, "DO NOTHING") {
		t.Errorf("UpsertSQL no updates: %q", u)
	}
	if u := d.UpsertSQL([]string{"id"}, []string{"name"}, 1); !strings.Contains(u, "DO UPDATE SET") {
		t.Errorf("UpsertSQL with updates: %q", u)
	}
}

// ---------------------------------------------------------------------------
// MSSQL dialect
// ---------------------------------------------------------------------------

func TestMSSQLDialect_Full(t *testing.T) {
	d := quark.MSSQL()

	if d.Name() != "mssql" {
		t.Errorf("Name = %q", d.Name())
	}
	if d.Placeholder(1) != "@p1" || d.Placeholder(3) != "@p3" {
		t.Error("Placeholder")
	}
	if ps := d.Placeholders(2); ps[0] != "@p1" || ps[1] != "@p2" {
		t.Error("Placeholders")
	}
	if d.Quote("t") != "[t]" {
		t.Error("Quote")
	}
	assertLO(t, d, 10, 0, "OFFSET 0 ROWS FETCH NEXT 10 ROWS ONLY")
	assertLO(t, d, 10, 5, "OFFSET 5 ROWS FETCH NEXT 10 ROWS ONLY")
	assertLO(t, d, 0, 5, "OFFSET 5 ROWS")
	assertLO(t, d, 0, 0, "")
	if d.SupportsReturning() {
		t.Error("SupportsReturning")
	}
	if d.Returning("id") != "" {
		t.Error("Returning")
	}
	if !d.SupportsLastInsertID() {
		t.Error("SupportsLastInsertID")
	}
	if !strings.Contains(d.LastInsertIDQuery("t", "id"), "SCOPE_IDENTITY") {
		t.Error("LastInsertIDQuery")
	}
	if sql, args, err := d.JSONExtract("d", "k"); err != nil || !strings.Contains(sql, "JSON_VALUE") || len(args) == 0 {
		t.Errorf("JSONExtract: sql=%q args=%v err=%v", sql, args, err)
	}
	if !strings.Contains(d.CurrentTimestamp(), "GETDATE") {
		t.Error("CurrentTimestamp")
	}
	if !strings.Contains(d.BuildRoutineQuery("fn", 2), "@p1") {
		t.Error("BuildRoutineQuery")
	}
	if !strings.Contains(d.BuildProcedureCall("p", 2), "EXEC") {
		t.Error("BuildProcedureCall")
	}
	if !strings.Contains(d.AlterTableAddColumn("t", "c", "INT"), "ADD") {
		t.Error("AlterTableAddColumn")
	}
	if !strings.Contains(d.AlterTableDropColumn("t", "c"), "DROP COLUMN") {
		t.Error("AlterTableDropColumn")
	}
	if !strings.Contains(d.AlterTableAlterColumn("t", "c", "INT"), "ALTER COLUMN") {
		t.Error("AlterTableAlterColumn")
	}
	if !strings.Contains(d.RenameColumn("t", "a", "b"), "sp_rename") {
		t.Error("RenameColumn")
	}
	if !strings.Contains(d.RenameTable("a", "b"), "sp_rename") {
		t.Error("RenameTable")
	}
	if !d.SupportsTransactionalDDL() {
		t.Error("SupportsTransactionalDDL")
	}
	if u := d.UpsertSQL([]string{"id"}, []string{"name"}, 1); u != "" {
		t.Errorf("MSSQL UpsertSQL should return empty (uses MERGE): %q", u)
	}
}

// ---------------------------------------------------------------------------
// Oracle dialect
// ---------------------------------------------------------------------------

func TestOracleDialect_Full(t *testing.T) {
	d := quark.Oracle()

	if d.Name() != "oracle" {
		t.Errorf("Name = %q", d.Name())
	}
	if d.Placeholder(1) != ":1" || d.Placeholder(5) != ":5" {
		t.Error("Placeholder")
	}
	if ps := d.Placeholders(3); ps[0] != ":1" || ps[2] != ":3" {
		t.Error("Placeholders")
	}
	if d.Quote("tbl") != `"TBL"` {
		t.Error("Quote should uppercase")
	}
	assertLO(t, d, 10, 0, "OFFSET 0 ROWS FETCH NEXT 10 ROWS ONLY")
	assertLO(t, d, 10, 5, "OFFSET 5 ROWS FETCH NEXT 10 ROWS ONLY")
	assertLO(t, d, 0, 5, "OFFSET 5 ROWS")
	assertLO(t, d, 0, 0, "")
	if !d.SupportsReturning() {
		t.Error("SupportsReturning")
	}
	if d.Returning() != "" {
		t.Error("Returning empty")
	}
	if r := d.Returning("id"); !strings.HasPrefix(r, "RETURNING") {
		t.Error("Returning with col")
	}
	if d.SupportsLastInsertID() {
		t.Error("SupportsLastInsertID")
	}
	if d.LastInsertIDQuery("t", "id") != "" {
		t.Error("LastInsertIDQuery")
	}
	// Oracle's JSON_VALUE rejects a bound path (ORA-40454): the validated path
	// is inlined as the literal '$.k', the column is quoted (uppercased "D"),
	// and there is no bind arg.
	if sql, args, err := d.JSONExtract("d", "k"); err != nil || !strings.Contains(sql, "JSON_VALUE") || !strings.Contains(sql, `"D"`) || !strings.Contains(sql, "'$.k'") || len(args) != 0 {
		t.Errorf("JSONExtract: sql=%q args=%v err=%v", sql, args, err)
	}
	if d.CurrentTimestamp() != "SYSDATE" {
		t.Error("CurrentTimestamp")
	}
	if !strings.Contains(d.BuildRoutineQuery("fn", 2), ":1") {
		t.Error("BuildRoutineQuery")
	}
	if !strings.Contains(d.BuildProcedureCall("p", 2), "BEGIN") {
		t.Error("BuildProcedureCall")
	}
	if !strings.Contains(d.AlterTableAddColumn("t", "c", "NUMBER"), "ADD") {
		t.Error("AlterTableAddColumn")
	}
	if !strings.Contains(d.AlterTableDropColumn("t", "c"), "DROP COLUMN") {
		t.Error("AlterTableDropColumn")
	}
	if !strings.Contains(d.AlterTableAlterColumn("t", "c", "INT"), "MODIFY") {
		t.Error("AlterTableAlterColumn")
	}
	if !strings.Contains(d.RenameColumn("t", "a", "b"), "RENAME COLUMN") {
		t.Error("RenameColumn")
	}
	if !strings.Contains(d.RenameTable("a", "b"), "RENAME TO") {
		t.Error("RenameTable")
	}
	if !d.SupportsTransactionalDDL() {
		t.Error("SupportsTransactionalDDL")
	}
	if u := d.UpsertSQL([]string{"id"}, []string{"name"}, 1); u != "" {
		t.Errorf("Oracle UpsertSQL should return empty: %q", u)
	}
}

// ---------------------------------------------------------------------------
// MariaDB dialect
// ---------------------------------------------------------------------------

func TestMariaDBDialect_Full(t *testing.T) {
	d := quark.MariaDB()

	if d.Name() != "mariadb" {
		t.Errorf("Name = %q", d.Name())
	}
	// Inherited from MySQL
	if d.Placeholder(1) != "?" {
		t.Error("Placeholder")
	}
	if d.Quote("t") != "`t`" {
		t.Error("Quote")
	}
	// MariaDB overrides LimitOffset to LIMIT x OFFSET y
	assertLO(t, d, 5, 10, "LIMIT 5 OFFSET 10")
	assertLO(t, d, 5, 0, "LIMIT 5")
	assertLO(t, d, 0, 10, "OFFSET 10")
	assertLO(t, d, 0, 0, "")
	if !d.SupportsReturning() {
		t.Error("SupportsReturning")
	}
	if d.Returning() != "" {
		t.Error("Returning empty")
	}
	if r := d.Returning("id"); !strings.HasPrefix(r, "RETURNING") {
		t.Error("Returning with col")
	}
	if d.SupportsLastInsertID() {
		t.Error("SupportsLastInsertID should be false (uses RETURNING)")
	}
	if d.LastInsertIDQuery("t", "id") == "" {
		t.Error("LastInsertIDQuery fallback")
	}
	if sql, args, err := d.JSONExtract("d", "k"); err != nil || !strings.Contains(sql, "JSON_VALUE") || len(args) == 0 {
		t.Errorf("JSONExtract: sql=%q args=%v err=%v", sql, args, err)
	}
	if !d.SupportsTransactionalDDL() == false { // false means no tx DDL
		// SupportsTransactionalDDL should return false for MariaDB
	}
	if d.SupportsTransactionalDDL() {
		t.Error("MariaDB SupportsTransactionalDDL should be false")
	}
	if !strings.Contains(d.RenameColumn("t", "a", "b"), "RENAME COLUMN") {
		t.Error("RenameColumn")
	}
	if !strings.Contains(d.AlterTableAlterColumn("t", "c", "INT"), "MODIFY COLUMN") {
		t.Error("AlterTableAlterColumn")
	}
	if u := d.UpsertSQL([]string{"id"}, []string{"name"}, 1); !strings.Contains(u, "DUPLICATE KEY") {
		t.Errorf("UpsertSQL: %q", u)
	}

	// MariaDB-specific methods (type-assert to access them)
	mb := d.(*quark.MariaDBDialect)
	if !strings.Contains(mb.CreateSequence("seq", 1, 1), "CREATE SEQUENCE") {
		t.Error("CreateSequence")
	}
	if !strings.Contains(mb.NextVal("seq"), "NEXTVAL") {
		t.Error("NextVal")
	}
	if !strings.Contains(mb.CreateSystemVersionedTable("t", "id INT"), "SYSTEM VERSIONING") {
		t.Error("CreateSystemVersionedTable")
	}
	if !strings.Contains(mb.HistoryQuery("t"), "SYSTEM_TIME ALL") {
		t.Error("HistoryQuery")
	}
	if !strings.Contains(mb.HistoryBetween("t", "2020-01-01", "2021-01-01"), "SYSTEM_TIME BETWEEN") {
		t.Error("HistoryBetween")
	}
	if !strings.Contains(mb.JSONTable("data", "$[*]", "id INT PATH '$.id'"), "JSON_TABLE") {
		t.Error("JSONTable")
	}
}

// ---------------------------------------------------------------------------
// DetectDialect / DetectDialectByName / RegisterDialect
// ---------------------------------------------------------------------------

func TestDetectDialect_Full(t *testing.T) {
	cases := []struct{ driver, want string }{
		{"postgres", "postgres"},
		{"pgx", "postgres"},
		{"pgx/v5", "postgres"},
		{"pq", "postgres"},
		{"mysql", "mysql"},
		{"mariadb", "mariadb"},
		{"sqlite", "sqlite"},
		{"sqlite3", "sqlite"},
		{"modernc", "sqlite"},
		{"mssql", "mssql"},
		{"sqlserver", "mssql"},
		{"azuresql", "mssql"},
		{"oracle", "oracle"},
		{"godror", "oracle"},
		{"oci8", "oracle"},
	}
	for _, tc := range cases {
		d, err := quark.DetectDialect(tc.driver)
		if err != nil {
			t.Errorf("DetectDialect(%q): %v", tc.driver, err)
			continue
		}
		if d.Name() != tc.want {
			t.Errorf("DetectDialect(%q).Name() = %q, want %q", tc.driver, d.Name(), tc.want)
		}
	}

	// Unknown driver
	_, err := quark.DetectDialect("unknown_driver")
	if err == nil {
		t.Error("expected error for unknown driver")
	}
}

func TestDetectDialectByName(t *testing.T) {
	d, err := quark.DetectDialectByName("sqlite")
	if err != nil || d.Name() != "sqlite" {
		t.Errorf("DetectDialectByName(sqlite): %v %v", d, err)
	}
	_, err = quark.DetectDialectByName("nonexistent")
	if err == nil {
		t.Error("expected error for unknown name")
	}
}

func TestRegisterDialect(t *testing.T) {
	custom := quark.SQLite() // reuse sqlite as a custom "cockroach" dialect for testing
	quark.RegisterDialect("cockroach", custom)

	d, err := quark.DetectDialect("cockroach")
	if err != nil {
		t.Fatalf("DetectDialect after RegisterDialect: %v", err)
	}
	if d.Name() != "sqlite" {
		t.Errorf("got %q, want sqlite (reused)", d.Name())
	}

	d2, err := quark.DetectDialectByName("cockroach")
	if err != nil {
		t.Fatalf("DetectDialectByName after RegisterDialect: %v", err)
	}
	if d2.Name() != "sqlite" {
		t.Errorf("got %q", d2.Name())
	}
}
