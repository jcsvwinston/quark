package quark_test

import (
	"testing"

	"github.com/jcsvwinston/quark"
)

func TestMSSQLDialect(t *testing.T) {
	d := quark.MSSQL()

	if d.Name() != "mssql" {
		t.Errorf("expected mssql, got %s", d.Name())
	}

	if d.Placeholder(1) != "@p1" {
		t.Errorf("expected @p1, got %s", d.Placeholder(1))
	}

	if d.Quote("user") != "[user]" {
		t.Errorf("expected [user], got %s", d.Quote("user"))
	}

	limitOffset := d.LimitOffset(10, 20)
	expected := "OFFSET 20 ROWS FETCH NEXT 10 ROWS ONLY"
	if limitOffset != expected {
		t.Errorf("expected %s, got %s", expected, limitOffset)
	}

	if d.SupportsReturning() {
		t.Error("mssql should not support returning in current implementation")
	}
}

func TestOracleDialect(t *testing.T) {
	d := quark.Oracle()

	if d.Name() != "oracle" {
		t.Errorf("expected oracle, got %s", d.Name())
	}

	if d.Placeholder(1) != ":1" {
		t.Errorf("expected :1, got %s", d.Placeholder(1))
	}

	if d.Quote("user") != "\"USER\"" {
		t.Errorf("expected \"USER\" (Oracle uppercases identifiers), got %s", d.Quote("user"))
	}

	limitOffset := d.LimitOffset(10, 20)
	expected := "OFFSET 20 ROWS FETCH NEXT 10 ROWS ONLY"
	if limitOffset != expected {
		t.Errorf("expected %s, got %s", expected, limitOffset)
	}

	if !d.SupportsReturning() {
		t.Error("oracle should support returning")
	}

	returning := d.Returning("id")
	expectedReturning := "RETURNING \"ID\""
	if returning != expectedReturning {
		t.Errorf("expected %s, got %s", expectedReturning, returning)
	}
}

func TestMariaDBDialect(t *testing.T) {
	d := quark.MariaDB()

	if d.Name() != "mariadb" {
		t.Errorf("expected mariadb, got %s", d.Name())
	}

	// Wire protocol identical to MySQL — backtick quoting, ? placeholders
	if d.Placeholder(1) != "?" {
		t.Errorf("expected ?, got %s", d.Placeholder(1))
	}
	if d.Quote("user") != "`user`" {
		t.Errorf("expected `user`, got %s", d.Quote("user"))
	}

	// MariaDB uses standard LIMIT n OFFSET m (not MySQL's LIMIT m, n)
	if got := d.LimitOffset(10, 20); got != "LIMIT 10 OFFSET 20" {
		t.Errorf("LimitOffset: expected 'LIMIT 10 OFFSET 20', got %q", got)
	}
	if got := d.LimitOffset(5, 0); got != "LIMIT 5" {
		t.Errorf("LimitOffset(5,0): expected 'LIMIT 5', got %q", got)
	}
	if got := d.LimitOffset(0, 10); got != "OFFSET 10" {
		t.Errorf("LimitOffset(0,10): expected 'OFFSET 10', got %q", got)
	}

	// RETURNING is supported (10.5+)
	if !d.SupportsReturning() {
		t.Error("mariadb should support RETURNING")
	}
	if got := d.Returning("id"); got != "RETURNING `id`" {
		t.Errorf("Returning: expected 'RETURNING `id`', got %q", got)
	}
	if got := d.Returning("id", "created_at"); got != "RETURNING `id`, `created_at`" {
		t.Errorf("Returning multi: got %q", got)
	}

	// ORM prefers RETURNING over LAST_INSERT_ID for MariaDB
	if d.SupportsLastInsertID() {
		t.Error("mariadb dialect should prefer RETURNING over LastInsertID")
	}

	// JSON_VALUE (10.2.3+)
	if got := d.JSONExtract("meta", "key"); got != "JSON_VALUE(`meta`, '$.key')" {
		t.Errorf("JSONExtract: got %q", got)
	}

	// No transactional DDL (implicit commits like MySQL)
	if d.SupportsTransactionalDDL() {
		t.Error("mariadb should not support transactional DDL")
	}

	// DDL statements
	if got := d.AlterTableAddColumn("users", "email", "VARCHAR(255)"); got != "ALTER TABLE `users` ADD COLUMN `email` VARCHAR(255)" {
		t.Errorf("AlterTableAddColumn: got %q", got)
	}
	if got := d.AlterTableDropColumn("users", "email"); got != "ALTER TABLE `users` DROP COLUMN `email`" {
		t.Errorf("AlterTableDropColumn: got %q", got)
	}
	if got := d.AlterTableAlterColumn("users", "email", "TEXT"); got != "ALTER TABLE `users` MODIFY COLUMN `email` TEXT" {
		t.Errorf("AlterTableAlterColumn: got %q", got)
	}
	if got := d.RenameColumn("users", "mail", "email"); got != "ALTER TABLE `users` RENAME COLUMN `mail` TO `email`" {
		t.Errorf("RenameColumn: got %q", got)
	}
	if got := d.RenameTable("users", "accounts"); got != "RENAME TABLE `users` TO `accounts`" {
		t.Errorf("RenameTable: got %q", got)
	}

	// MariaDB-specific: cast to MariaDBDialect for exclusive methods
	mdb, ok := d.(*quark.MariaDBDialect)
	if !ok {
		t.Fatal("MariaDB() should return *quark.MariaDBDialect")
	}

	// Sequences (10.3+)
	if got := mdb.CreateSequence("order_seq", 1, 1); got != "CREATE SEQUENCE IF NOT EXISTS `order_seq` START WITH 1 INCREMENT BY 1" {
		t.Errorf("CreateSequence: got %q", got)
	}
	if got := mdb.NextVal("order_seq"); got != "NEXTVAL(`order_seq`)" {
		t.Errorf("NextVal: got %q", got)
	}

	// System-versioned / temporal tables (10.3.4+)
	cols := "`id` INT NOT NULL AUTO_INCREMENT PRIMARY KEY,\n`name` VARCHAR(100)"
	if got := mdb.CreateSystemVersionedTable("audit_log", cols); !containsAll(got, "WITH SYSTEM VERSIONING", "`audit_log`") {
		t.Errorf("CreateSystemVersionedTable: got %q", got)
	}
	if got := mdb.HistoryQuery("audit_log"); got != "SELECT * FROM `audit_log` FOR SYSTEM_TIME ALL" {
		t.Errorf("HistoryQuery: got %q", got)
	}
	if got := mdb.HistoryBetween("audit_log", "2024-01-01", "2025-01-01"); !containsAll(got, "FOR SYSTEM_TIME BETWEEN", "2024-01-01", "2025-01-01") {
		t.Errorf("HistoryBetween: got %q", got)
	}

	// JSON_TABLE (10.6+)
	jt := mdb.JSONTable("doc_col", "$[*]", "id INT PATH '$.id'", "name TEXT PATH '$.name'")
	if !containsAll(jt, "JSON_TABLE", "$[*]", "COLUMNS") {
		t.Errorf("JSONTable: got %q", jt)
	}
}

// containsAll returns true if s contains all substrings.
func containsAll(s string, subs ...string) bool {
	for _, sub := range subs {
		if !contains(s, sub) {
			return false
		}
	}
	return true
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(sub) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(sub); i++ {
				if s[i:i+len(sub)] == sub {
					return true
				}
			}
			return false
		}())
}

func TestDetectDialect(t *testing.T) {
	tests := []struct {
		driver string
		want   string
	}{
		{"sqlserver", "mssql"},
		{"mssql", "mssql"},
		{"oracle", "oracle"},
		{"godror", "oracle"},
		{"mysql", "mysql"},
		{"mariadb", "mariadb"},
		{"pgx", "postgres"},
		{"postgres", "postgres"},
	}

	for _, tt := range tests {
		d, err := quark.DetectDialect(tt.driver)
		if err != nil {
			t.Errorf("quark.DetectDialect(%s) error: %v", tt.driver, err)
			continue
		}
		if d.Name() != tt.want {
			t.Errorf("quark.DetectDialect(%s) = %s, want %s", tt.driver, d.Name(), tt.want)
		}
	}
}
