package migrate_test

import (
	"reflect"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark/internal/migrate"
)

func TestSQLType_IntPK_AutoIncrement(t *testing.T) {
	int64Type := reflect.TypeOf(int64(0))

	cases := []struct {
		dialect string
		want    string
	}{
		{"sqlite", "INTEGER PRIMARY KEY AUTOINCREMENT"},
		{"postgres", "SERIAL PRIMARY KEY"},
		{"mysql", "INT AUTO_INCREMENT PRIMARY KEY"},
		{"mariadb", "INT AUTO_INCREMENT PRIMARY KEY"},
		{"mssql", "INT IDENTITY(1,1) PRIMARY KEY"},
		{"oracle", "NUMBER GENERATED ALWAYS AS IDENTITY PRIMARY KEY"},
	}

	for _, tc := range cases {
		got := migrate.SQLType(tc.dialect, int64Type, true)
		if got != tc.want {
			t.Errorf("dialect=%s: expected %q, got %q", tc.dialect, tc.want, got)
		}
	}
}

func TestSQLType_StringPK_UUID(t *testing.T) {
	strType := reflect.TypeOf("")

	cases := []struct {
		dialect string
		want    string
	}{
		{"sqlite", "VARCHAR(36) PRIMARY KEY"},
		{"postgres", "VARCHAR(36) PRIMARY KEY"},
		{"mysql", "VARCHAR(36) PRIMARY KEY"},
		{"mariadb", "VARCHAR(36) PRIMARY KEY"},
		{"mssql", "NVARCHAR(36) PRIMARY KEY"},
		{"oracle", "VARCHAR2(36) PRIMARY KEY"},
	}

	for _, tc := range cases {
		got := migrate.SQLType(tc.dialect, strType, true)
		if got != tc.want {
			t.Errorf("dialect=%s: expected %q, got %q", tc.dialect, tc.want, got)
		}
	}
}

func TestSQLType_NonPK_String(t *testing.T) {
	strType := reflect.TypeOf("")

	cases := []struct {
		dialect string
		want    string
	}{
		{"sqlite", "TEXT"},
		{"postgres", "TEXT"},
		{"mysql", "VARCHAR(255)"},
		{"mariadb", "VARCHAR(255)"},
		{"mssql", "NVARCHAR(255)"},
		{"oracle", "VARCHAR2(255)"},
	}

	for _, tc := range cases {
		got := migrate.SQLType(tc.dialect, strType, false)
		if got != tc.want {
			t.Errorf("dialect=%s: expected %q, got %q", tc.dialect, tc.want, got)
		}
	}
}

// TestSQLType_Bool_N3_Regression guards against the regression where MSSQL bool
// was incorrectly mapped to NUMBER(1) (Oracle-only type) instead of BIT.
func TestSQLType_Bool_N3_Regression(t *testing.T) {
	boolType := reflect.TypeOf(false)

	cases := []struct {
		dialect string
		want    string
	}{
		{"mssql", "BIT"},
		{"oracle", "NUMBER(1)"},
		{"postgres", "BOOLEAN"},
		{"sqlite", "BOOLEAN"},
		{"mysql", "BOOLEAN"},
		{"mariadb", "BOOLEAN"},
	}

	for _, tc := range cases {
		got := migrate.SQLType(tc.dialect, boolType, false)
		if got != tc.want {
			t.Errorf("N3 bool regression dialect=%s: expected %q, got %q", tc.dialect, tc.want, got)
		}
	}
}

func TestSQLType_NoPK_DoesNotContainPRIMARY(t *testing.T) {
	types := []reflect.Type{
		reflect.TypeOf(""),
		reflect.TypeOf(int64(0)),
		reflect.TypeOf(float64(0)),
		reflect.TypeOf(false),
	}
	dialects := []string{"sqlite", "postgres", "mysql", "mariadb", "mssql", "oracle"}

	for _, dt := range types {
		for _, d := range dialects {
			got := migrate.SQLType(d, dt, false)
			if strings.Contains(got, "PRIMARY") {
				t.Errorf("dialect=%s type=%s: non-PK should not contain PRIMARY, got %q", d, dt, got)
			}
		}
	}
}
