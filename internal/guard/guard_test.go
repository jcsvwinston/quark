package guard_test

import (
	"strings"
	"testing"

	"github.com/jcsvwinston/quark/internal/guard"
)

// --- ValidateIdentifier ---

func TestValidateIdentifier_Valid(t *testing.T) {
	g := guard.New()
	cases := []string{
		"users", "user_id", "myTable", "column123", "_private", "a", "z9",
	}
	for _, name := range cases {
		if err := g.ValidateIdentifier(name); err != nil {
			t.Errorf("expected %q to be valid, got: %v", name, err)
		}
	}
}

func TestValidateIdentifier_Empty(t *testing.T) {
	g := guard.New()
	if err := g.ValidateIdentifier(""); err == nil {
		t.Error("expected error for empty identifier, got nil")
	}
}

func TestValidateIdentifier_TooLong(t *testing.T) {
	g := guard.New()
	long := strings.Repeat("a", 65)
	if err := g.ValidateIdentifier(long); err == nil {
		t.Errorf("expected error for identifier longer than 64 chars")
	}
}

func TestValidateIdentifier_MaxLength(t *testing.T) {
	g := guard.New()
	exact := strings.Repeat("a", 64)
	if err := g.ValidateIdentifier(exact); err != nil {
		t.Errorf("expected 64-char identifier to be valid, got: %v", err)
	}
}

func TestValidateIdentifier_InvalidChars(t *testing.T) {
	g := guard.New()
	cases := []string{
		"user-id", "user.id", "user id", "user@id", "user$id",
		"1user", "123", "-start", "table;drop",
	}
	for _, name := range cases {
		if err := g.ValidateIdentifier(name); err == nil {
			t.Errorf("expected %q to be invalid, got nil", name)
		}
	}
}

func TestValidateIdentifier_ReservedKeywords(t *testing.T) {
	g := guard.New()
	keywords := []string{
		"SELECT", "select", "INSERT", "UPDATE", "DELETE",
		"DROP", "CREATE", "ALTER", "TRUNCATE",
		"EXEC", "EXECUTE", "UNION",
		"WHERE", "FROM", "JOIN", "TABLE", "INDEX",
	}
	for _, kw := range keywords {
		if err := g.ValidateIdentifier(kw); err == nil {
			t.Errorf("expected reserved keyword %q to be invalid, got nil", kw)
		}
	}
}

// --- ValidateIdentifiers ---

func TestValidateIdentifiers_AllValid(t *testing.T) {
	g := guard.New()
	if err := g.ValidateIdentifiers("users", "email", "created_at"); err != nil {
		t.Errorf("expected all valid, got: %v", err)
	}
}

func TestValidateIdentifiers_OneInvalid(t *testing.T) {
	g := guard.New()
	if err := g.ValidateIdentifiers("users", "SELECT", "email"); err == nil {
		t.Error("expected error for reserved keyword in list, got nil")
	}
}

// --- QuoteIdentifier ---

type mockQuoter struct{}

func (mockQuoter) Quote(id string) string { return `"` + id + `"` }

func TestQuoteIdentifier_Valid(t *testing.T) {
	g := guard.New()
	q, err := g.QuoteIdentifier(mockQuoter{}, "users")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if q != `"users"` {
		t.Errorf("expected %q, got %q", `"users"`, q)
	}
}

func TestQuoteIdentifier_Invalid(t *testing.T) {
	g := guard.New()
	_, err := g.QuoteIdentifier(mockQuoter{}, "DROP")
	if err == nil {
		t.Error("expected error for reserved keyword, got nil")
	}
}

// --- ValidateOperator ---

func TestValidateOperator_Valid(t *testing.T) {
	g := guard.New()
	ops := []string{
		"=", "!=", "<>", "<", ">", "<=", ">=",
		"LIKE", "NOT LIKE", "IN", "NOT IN",
		"IS", "IS NOT", "IS NULL", "IS NOT NULL",
		"BETWEEN", "NOT BETWEEN",
	}
	for _, op := range ops {
		if err := g.ValidateOperator(op); err != nil {
			t.Errorf("expected operator %q to be valid, got: %v", op, err)
		}
	}
}

func TestValidateOperator_CaseInsensitive(t *testing.T) {
	g := guard.New()
	ops := []string{"like", "not like", "between", "in", "not in", "is null"}
	for _, op := range ops {
		if err := g.ValidateOperator(op); err != nil {
			t.Errorf("expected lowercase operator %q to be valid, got: %v", op, err)
		}
	}
}

func TestValidateOperator_Invalid(t *testing.T) {
	g := guard.New()
	bad := []string{
		"--", "/*", "*/", "OR", "AND", ";", "EXEC", "||", "&&",
	}
	for _, op := range bad {
		if err := g.ValidateOperator(op); err == nil {
			t.Errorf("expected operator %q to be invalid, got nil", op)
		}
	}
}

// --- HasPlaceholders ---

func TestHasPlaceholders_MySQL(t *testing.T) {
	if !guard.HasPlaceholders("SELECT * FROM users WHERE id = ?") {
		t.Error("expected ? to be recognised as placeholder")
	}
}

func TestHasPlaceholders_Postgres(t *testing.T) {
	if !guard.HasPlaceholders("SELECT * FROM users WHERE id = $1") {
		t.Error("expected $1 to be recognised as placeholder")
	}
}

func TestHasPlaceholders_MSSQL(t *testing.T) {
	if !guard.HasPlaceholders("SELECT * FROM users WHERE id = @p1") {
		t.Error("expected @p1 to be recognised as placeholder")
	}
}

func TestHasPlaceholders_OraclePositional(t *testing.T) {
	if !guard.HasPlaceholders("SELECT * FROM users WHERE id = :1") {
		t.Error("expected :1 to be recognised as placeholder")
	}
}

func TestHasPlaceholders_OracleNamed(t *testing.T) {
	if !guard.HasPlaceholders("SELECT * FROM users WHERE id = :user_id") {
		t.Error("expected :user_id to be recognised as placeholder")
	}
}

func TestHasPlaceholders_None(t *testing.T) {
	if guard.HasPlaceholders("SELECT * FROM users WHERE id = 42") {
		t.Error("expected no placeholder detected in literal query")
	}
}

// --- ValidateRawQuery ---

func TestValidateRawQuery_Safe(t *testing.T) {
	g := guard.New()
	if err := g.ValidateRawQuery("SELECT * FROM users WHERE id = $1", false); err != nil {
		t.Errorf("expected safe query to pass, got: %v", err)
	}
}

func TestValidateRawQuery_RequirePlaceholders_Pass(t *testing.T) {
	g := guard.New()
	if err := g.ValidateRawQuery("SELECT * FROM users WHERE id = ?", true); err != nil {
		t.Errorf("expected query with placeholder to pass, got: %v", err)
	}
}

func TestValidateRawQuery_RequirePlaceholders_Fail(t *testing.T) {
	g := guard.New()
	if err := g.ValidateRawQuery("SELECT * FROM users", true); err == nil {
		t.Error("expected error for missing placeholder when required, got nil")
	}
}

func TestValidateRawQuery_SuspiciousDropTable(t *testing.T) {
	g := guard.New()
	if err := g.ValidateRawQuery("SELECT 1; DROP TABLE users", false); err == nil {
		t.Error("expected error for ; DROP TABLE pattern, got nil")
	}
}

func TestValidateRawQuery_SuspiciousUnionSelect(t *testing.T) {
	g := guard.New()
	if err := g.ValidateRawQuery("SELECT id FROM users UNION SELECT password FROM admins", false); err == nil {
		t.Error("expected error for UNION SELECT pattern, got nil")
	}
}

func TestValidateRawQuery_SuspiciousOrOne(t *testing.T) {
	g := guard.New()
	if err := g.ValidateRawQuery("SELECT * FROM users WHERE id = 1 OR 1=1", false); err == nil {
		t.Error("expected error for OR 1=1 pattern, got nil")
	}
}

func TestValidateRawQuery_SuspiciousDelete(t *testing.T) {
	g := guard.New()
	if err := g.ValidateRawQuery("SELECT 1; DELETE FROM users", false); err == nil {
		t.Error("expected error for ; DELETE pattern, got nil")
	}
}

// --- ValidateJSONPath ---

func TestValidateJSONPath_Valid(t *testing.T) {
	cases := []string{
		"name",
		"user_id",
		"user.name",
		"user.profile.email",
		"a.b.c.d",
		"_private.field",
		"x1.y2.z3",
	}
	for _, p := range cases {
		if err := guard.ValidateJSONPath(p); err != nil {
			t.Errorf("expected %q to be valid, got: %v", p, err)
		}
	}
}

func TestValidateJSONPath_Invalid(t *testing.T) {
	cases := []struct {
		path string
		why  string
	}{
		{"", "empty"},
		{".x", "leading dot"},
		{"x.", "trailing dot"},
		{"x..y", "double dot"},
		{"1user", "leading digit"},
		{"$.user", "leading dollar (JSONPath syntax not accepted)"},
		{"user-name", "dash"},
		{"user name", "space"},
		{"x'; DROP TABLE users--", "SQL injection attempt"},
		{"x; SELECT 1", "semicolon"},
		{"x/*y*/z", "block comment"},
		{"x\"y", "double quote"},
		{"x'y", "single quote"},
		{"x\\y", "backslash"},
		{"x\ny", "newline"},
		{"x\ty", "tab"},
		{strings.Repeat("a", 257), "exceeds max length"},
	}
	for _, c := range cases {
		if err := guard.ValidateJSONPath(c.path); err == nil {
			t.Errorf("expected error for %q (%s), got nil", c.path, c.why)
		}
	}
}

func TestValidateJSONPath_BoundMethod(t *testing.T) {
	g := guard.New()
	if err := g.ValidateJSONPath("user.name"); err != nil {
		t.Errorf("bound ValidateJSONPath rejected valid path: %v", err)
	}
	if err := g.ValidateJSONPath("x';--"); err == nil {
		t.Error("bound ValidateJSONPath accepted injectable path")
	}
}

// --- ValidateJoinOn ---

func TestValidateJoinOn_Valid(t *testing.T) {
	cases := []string{
		"users.id = orders.user_id",
		"a = b",
		"users.id=orders.user_id", // no whitespace around op
		"users.id != orders.user_id",
		"users.id <> orders.user_id",
		"users.id <= orders.user_id",
		"users.id >= orders.user_id",
		"users.id <  orders.user_id", // double space ok
		"users.id = orders.user_id AND users.tenant_id = orders.tenant_id",
		"users.id = orders.user_id and users.tenant_id = orders.tenant_id", // lowercase
		"a.x = b.y OR c.z = d.w",
		"a = b AND c = d AND e = f",
	}
	for _, expr := range cases {
		if err := guard.ValidateJoinOn(expr); err != nil {
			t.Errorf("expected %q to be valid, got: %v", expr, err)
		}
	}
}

func TestValidateJoinOn_Invalid(t *testing.T) {
	cases := []struct {
		expr string
		why  string
	}{
		{"", "empty"},
		{"users.id = orders.user_id; DROP TABLE orders", "trailing injection"},
		{"users.id = orders.user_id -- comment", "line comment"},
		{"users.id = orders.user_id /* x */", "block comment"},
		{"users.id = 1", "rhs is a literal"},
		{"users.id = 'alice'", "rhs is a string literal"},
		{"users.id = LOWER(orders.user_id)", "function call"},
		{"users.id = orders.user_id UNION SELECT 1", "union"},
		{"(users.id = orders.user_id)", "parentheses"},
		{"users.id = orders.user_id OR 1=1", "OR with literals"},
		{"users.id =", "missing rhs"},
		{"= orders.user_id", "missing lhs"},
		{"users.id orders.user_id", "missing operator"},
		{"users.id $$$ orders.user_id", "junk operator"},
		{"users-id = orders.user_id", "dash in identifier"},
		{"$.user.id = orders.user_id", "leading $"},
		{"a.b.c = d.e", "three-segment ident"},
		{"a..b = c.d", "double dot"},
	}
	for _, c := range cases {
		if err := guard.ValidateJoinOn(c.expr); err == nil {
			t.Errorf("expected error for %q (%s), got nil", c.expr, c.why)
		}
	}
}

func TestValidateJoinOn_BoundMethod(t *testing.T) {
	g := guard.New()
	if err := g.ValidateJoinOn("a.b = c.d"); err != nil {
		t.Errorf("bound ValidateJoinOn rejected valid expression: %v", err)
	}
	if err := g.ValidateJoinOn("a = b; DROP"); err == nil {
		t.Error("bound ValidateJoinOn accepted injectable expression")
	}
}
