// Package guard provides SQL injection prevention utilities for Quark ORM.
// It validates identifiers, operators, and raw queries against known-safe patterns.
package guard

import (
	"fmt"
	"regexp"
	"strings"
)

// SQLGuard provides security validations for SQL queries.
// It prevents SQL injection by validating identifiers and enforcing safe practices.
type SQLGuard struct {
	identifierPattern *regexp.Regexp
	reservedKeywords  map[string]bool
	maxIdentifierLen  int
}

// reservedSQLKeywords contains SQL keywords that should not be used as identifiers.
var reservedSQLKeywords = map[string]bool{
	"SELECT": true, "INSERT": true, "UPDATE": true, "DELETE": true,
	"DROP": true, "CREATE": true, "ALTER": true, "TRUNCATE": true,
	"EXEC": true, "EXECUTE": true, "UNION": true, "UNION ALL": true,
	"OR": true, "AND": true, "WHERE": true, "FROM": true, "JOIN": true,
	"LEFT": true, "RIGHT": true, "INNER": true, "OUTER": true,
	"ORDER": true, "GROUP": true, "HAVING": true, "LIMIT": true,
	"OFFSET": true, "VALUES": true, "SET": true, "INTO": true,
	"TABLE": true, "DATABASE": true, "SCHEMA": true, "INDEX": true,
	"VIEW": true, "TRIGGER": true, "PROCEDURE": true, "FUNCTION": true,
}

// identifierRegex matches valid SQL identifiers.
var identifierRegex = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// jsonPathRegex matches a dotted JSON path: one or more identifier-like segments
// separated by single dots. Each segment must match the same rules as a SQL
// identifier (letter or underscore start, letters/digits/underscores after).
//
// Accepted:   "name", "user.name", "user.profile.email"
// Rejected:   "" (empty), ".x", "x.", "x..y", "1user", "$.user", "user-name",
//
//	anything containing whitespace, quotes, semicolons, comments, or
//	SQL-meaningful characters.
//
// Array indexes (e.g. "items.0.id") and engine-specific JSONPath syntax are out
// of scope for WhereJSON; users that need those should reach for RawQuery.
var jsonPathRegex = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*(\.[a-zA-Z_][a-zA-Z0-9_]*)*$`)

// maxJSONPathLen caps the total length of a JSON path. Paths longer than this
// are almost certainly an attack or a bug; legitimate paths in practice are far
// below this bound.
const maxJSONPathLen = 256

// New creates a new SQLGuard with default settings.
func New() *SQLGuard {
	return &SQLGuard{
		identifierPattern: identifierRegex,
		reservedKeywords:  reservedSQLKeywords,
		maxIdentifierLen:  64,
	}
}

// Quoter is a minimal interface for quoting SQL identifiers.
// It avoids a circular import with the dialect package.
type Quoter interface {
	Quote(identifier string) string
}

// ValidateIdentifier checks if a table or column identifier is safe to use.
func (g *SQLGuard) ValidateIdentifier(name string) error {
	if len(name) == 0 {
		return fmt.Errorf("ErrInvalidIdentifier: identifier is empty")
	}

	if len(name) > g.maxIdentifierLen {
		return fmt.Errorf("ErrInvalidIdentifier: identifier %q exceeds maximum length of %d characters",
			name, g.maxIdentifierLen)
	}

	if !g.identifierPattern.MatchString(name) {
		return fmt.Errorf("ErrInvalidIdentifier: identifier %q contains invalid characters. Only letters, numbers, and underscores allowed",
			name)
	}

	upper := strings.ToUpper(name)
	if g.reservedKeywords[upper] {
		return fmt.Errorf("ErrInvalidIdentifier: identifier %q is a reserved SQL keyword", name)
	}

	return nil
}

// ValidateIdentifiers checks multiple identifiers at once.
func (g *SQLGuard) ValidateIdentifiers(names ...string) error {
	for _, name := range names {
		if err := g.ValidateIdentifier(name); err != nil {
			return err
		}
	}
	return nil
}

// QuoteIdentifier validates and quotes an identifier using the provided Quoter.
func (g *SQLGuard) QuoteIdentifier(q Quoter, name string) (string, error) {
	if err := g.ValidateIdentifier(name); err != nil {
		return "", err
	}
	return q.Quote(name), nil
}

// ValidateJSONPath checks that a JSON path is shaped like a dotted identifier
// chain (e.g. "user.profile.email") and rejects anything that could carry SQL
// injection: empty paths, paths with quotes, semicolons, comment markers,
// whitespace, leading "$", or paths longer than maxJSONPathLen.
//
// It is a package-level function (not a method) because the validation has no
// configurable state and the dialect layer needs to call it without holding
// an SQLGuard instance.
//
// The error message starts with "ErrInvalidJSONPath:" so callers in package
// quark can wrap it with the public sentinel via errors.Join.
func ValidateJSONPath(path string) error {
	if len(path) == 0 {
		return fmt.Errorf("ErrInvalidJSONPath: path is empty")
	}
	if len(path) > maxJSONPathLen {
		return fmt.Errorf("ErrInvalidJSONPath: path exceeds maximum length of %d characters", maxJSONPathLen)
	}
	if !jsonPathRegex.MatchString(path) {
		return fmt.Errorf("ErrInvalidJSONPath: path %q must be a dotted identifier chain (e.g. \"user.name\")", path)
	}
	return nil
}

// ValidateJSONPath is the SQLGuard-bound counterpart to the package-level
// function; both share the same logic and accept the same shapes.
func (g *SQLGuard) ValidateJSONPath(path string) error {
	return ValidateJSONPath(path)
}

// joinOnRegex matches the minimal grammar Quark accepts for JOIN ... ON
// clauses while a structured Join().On() builder is pending (Phase 2 AST).
//
// Grammar (case-insensitive AND/OR keywords):
//
//	token       = ident ( "." ident )?
//	op          = "=" | "!=" | "<>" | "<" | "<=" | ">" | ">="
//	condition   = token whitespace? op whitespace? token
//	expression  = condition ( whitespace ("AND"|"OR") whitespace condition )*
//
// Identifier-to-identifier comparisons only. Literal values, function calls,
// subqueries, and parentheses are rejected — drop down to a structured Join
// (Phase 2) or RawQuery if you need them.
var joinOnRegex = regexp.MustCompile(
	`^\s*` +
		// First condition.
		`[a-zA-Z_][a-zA-Z0-9_]*(?:\.[a-zA-Z_][a-zA-Z0-9_]*)?` +
		`\s*(?:=|!=|<>|<=|>=|<|>)\s*` +
		`[a-zA-Z_][a-zA-Z0-9_]*(?:\.[a-zA-Z_][a-zA-Z0-9_]*)?` +
		// Optional repeated conditions joined by AND / OR.
		`(?:\s+(?i:AND|OR)\s+` +
		`[a-zA-Z_][a-zA-Z0-9_]*(?:\.[a-zA-Z_][a-zA-Z0-9_]*)?` +
		`\s*(?:=|!=|<>|<=|>=|<|>)\s*` +
		`[a-zA-Z_][a-zA-Z0-9_]*(?:\.[a-zA-Z_][a-zA-Z0-9_]*)?` +
		`)*\s*$`)

// maxJoinOnLen caps the JOIN ... ON expression length. Legitimate identifier-
// based ON clauses fit comfortably; longer strings are almost always payloads.
const maxJoinOnLen = 512

// ValidateJoinOn checks that an ON clause matches the minimal identifier-
// only grammar Quark accepts while a structured Join().On() builder is
// pending. It rejects anything containing literals, semicolons, comments,
// quotes, parentheses, or arbitrary whitespace surrounding non-token
// characters — so injection payloads ("...; DROP TABLE x") cannot pass.
//
// Returns an error whose message starts with "ErrInvalidJoin:" so callers
// in package quark can wrap it with the public sentinel via errors.Join.
func ValidateJoinOn(expr string) error {
	if len(expr) == 0 {
		return fmt.Errorf("ErrInvalidJoin: ON clause is empty")
	}
	if len(expr) > maxJoinOnLen {
		return fmt.Errorf("ErrInvalidJoin: ON clause exceeds maximum length of %d characters", maxJoinOnLen)
	}
	if !joinOnRegex.MatchString(expr) {
		return fmt.Errorf("ErrInvalidJoin: ON clause %q must be identifier-only conditions joined by AND/OR (e.g. \"users.id = orders.user_id\")", expr)
	}
	return nil
}

// ValidateJoinOn is the SQLGuard-bound counterpart to the package-level
// function; both share the same logic and accept the same shapes.
func (g *SQLGuard) ValidateJoinOn(expr string) error {
	return ValidateJoinOn(expr)
}

// jsonTablePathRegex matches the more permissive JSONPath grammar accepted by
// MariaDB's JSON_TABLE root expression — it allows array iterators (`[*]`)
// and bracket indexes (`[0]`) on top of dotted keys, but still rejects
// quotes, semicolons, comment markers, whitespace, and other SQL-meaningful
// characters.
var jsonTablePathRegex = regexp.MustCompile(`^\$(\.[a-zA-Z_][a-zA-Z0-9_]*|\[(\*|[0-9]+)\])*$`)

// ValidateJSONTablePath is a defensive validator for MariaDB's JSON_TABLE
// root path. Unlike ValidateJSONPath it accepts the JSONPath shapes that
// JSON_TABLE requires (`$`, `$[*]`, `$.items[0]`, `$.items[*].name`) but
// rejects anything that looks like injection.
//
// The JSON_TABLE entry point is internal-only today; this validator exists so
// that any caller in package quark or trusted internal code paths still gets
// a fail-fast error if a bad value reaches the dialect.
func ValidateJSONTablePath(path string) error {
	if len(path) == 0 {
		return fmt.Errorf("ErrInvalidJSONPath: JSON_TABLE path is empty")
	}
	if len(path) > maxJSONPathLen {
		return fmt.Errorf("ErrInvalidJSONPath: JSON_TABLE path exceeds maximum length of %d characters", maxJSONPathLen)
	}
	if !jsonTablePathRegex.MatchString(path) {
		return fmt.Errorf("ErrInvalidJSONPath: JSON_TABLE path %q must be a JSONPath rooted at $ (e.g. \"$[*]\", \"$.items[0].name\")", path)
	}
	return nil
}

// ValidateOperator checks if an operator is in the allowed whitelist.
func (g *SQLGuard) ValidateOperator(op string) error {
	allowedOperators := map[string]bool{
		"=": true, "!=": true, "<>": true, "<": true, ">": true,
		"<=": true, ">=": true,
		"LIKE": true, "NOT LIKE": true,
		"IN": true, "NOT IN": true,
		"IS": true, "IS NOT": true,
		"IS NULL": true, "IS NOT NULL": true,
		"BETWEEN": true, "NOT BETWEEN": true,
	}

	upper := strings.ToUpper(strings.TrimSpace(op))
	if !allowedOperators[upper] {
		return fmt.Errorf("ErrInvalidQuery: operator %q is not allowed", op)
	}
	return nil
}

// HasPlaceholders checks if a query string contains parameter placeholders.
func HasPlaceholders(query string) bool {
	patterns := []string{
		`\?`,    // MySQL, SQLite: ?
		`\$\d+`, // PostgreSQL: $1, $2
		`@p\d+`, // MSSQL: @p1, @p2
		`:\d+`,  // Oracle: :1, :2
		`:\w+`,  // Oracle named: :name
	}

	for _, pattern := range patterns {
		matched, _ := regexp.MatchString(pattern, query)
		if matched {
			return true
		}
	}

	return false
}

// ValidateRawQuery performs basic validation on a raw SQL query. It is a
// best-effort heuristic backstop for the opt-in raw path (AllowRawQueries),
// NOT a complete anti-injection filter. It does not parse SQL, so it has known
// false positives — e.g. a `--` inside a string literal (`'range--max'`) is
// rejected even though it is not a comment; rephrase such a query. The real
// boundary for raw queries is AllowRawQueries (off by default) + placeholders
// for values.
func (g *SQLGuard) ValidateRawQuery(query string, requirePlaceholders bool) error {
	if requirePlaceholders && !HasPlaceholders(query) {
		return fmt.Errorf("ErrInvalidQuery: raw queries must use placeholders (?, $1, @p1, :1, etc.)")
	}

	suspiciousPatterns := []string{
		`;\s*DROP\s`,
		`;\s*DELETE\s`,
		`;\s*UPDATE\s+\w+\s+SET\s+\w+\s*=`,
		`UNION\s+SELECT`,
		`OR\s+1\s*=\s*1`,
		`OR\s+'\s*1\s*'\s*=\s*'\s*1`,
		`--`, // SQL line comment: the classic injection tail (`... OR 1=1 --`).
		// Block comments (/* */) are intentionally NOT rejected: they are
		// legitimate in raw queries as optimizer hints (MySQL `/*+ ... */`,
		// Oracle `/*+ INDEX(...) */`). ValidateRawQuery is a best-effort
		// heuristic backstop, not a complete anti-injection filter — the real
		// boundary is AllowRawQueries (off by default) + placeholders for
		// values. See docs/playbooks/security.md.
	}

	upper := strings.ToUpper(query)
	for _, pattern := range suspiciousPatterns {
		matched, _ := regexp.MatchString(pattern, upper)
		if matched {
			return fmt.Errorf("ErrInvalidQuery: query contains suspicious patterns that may indicate SQL injection")
		}
	}

	return nil
}
