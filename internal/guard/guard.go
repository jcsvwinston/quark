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

// ValidateRawQuery performs basic validation on a raw SQL query.
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
