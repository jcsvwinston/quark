// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

//go:build bugbash
// +build bugbash

// Package f13_security is bug-bash phase F13: negative / security tests.
// It verifies that SQLGuard blocks the known SQL-injection vectors on every
// engine — malicious identifiers and operators in the builder, malicious
// JSON paths, malicious JOIN ON clauses, raw queries while disabled, raw
// queries carrying injection patterns while enabled, and malicious tenant
// IDs. The contract (BUGBASH_PLAN.md §F13): 100% of known payloads must be
// blocked. A payload that reaches the engine is a P0.
//
// Each check asserts the security property — the malicious input is rejected
// with an error, so the statement never executes. Where Quark wraps a public
// sentinel (ErrInvalidJSONPath, ErrInvalidJoin, ErrInvalidQuery) the check
// also asserts errors.Is. The identifier/operator paths surface the guard's
// descriptive string error rather than a wrapped public sentinel; F13 asserts
// they are blocked (the security guarantee) without over-asserting a sentinel
// the builder does not currently wrap.
package f13_security

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark"
	"github.com/jcsvwinston/quark/bugbash/domain"
	"github.com/jcsvwinston/quark/bugbash/reporter"
	"github.com/jcsvwinston/quark/bugbash/tools"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "github.com/microsoft/go-mssqldb"
	_ "github.com/sijms/go-ora/v2"
	_ "modernc.org/sqlite"
)

const phase = "f13_security"

var engineFlag = flag.String("engines", "sqlite",
	"comma-separated engines (sqlite,postgres,mysql,mariadb,mssql,oracle) or 'all'")

func selectedEngines() []string {
	v := strings.TrimSpace(*engineFlag)
	if v == "" || v == "all" {
		return tools.AllEngines
	}
	var out []string
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// rec binds reporter.Fail to a fixed engine. Security holes are filed as gaps
// (a missing validation block) so the bugbash-reporter triages them as such.
type rec struct {
	t   *testing.T
	eng string
}

func newRec(t *testing.T, eng string) rec { return rec{t: t, eng: eng} }

func (r rec) fail(name string, sev reporter.Severity, format string, args ...any) {
	r.t.Helper()
	reporter.Fail(r.t, reporter.Failure{
		Phase:    phase,
		Test:     name,
		Engine:   r.eng,
		Category: reporter.CategoryGap,
		Severity: sev,
		Error:    fmt.Sprintf(format, args...),
		Reproducer: reporter.Reproducer{
			Command: "go test -tags=bugbash -run TestNegativeSecurity ./phases/f13_security/ -engines=" + r.eng,
		},
	})
}

// blocked is the core F13 assertion: a malicious payload must produce an
// error (so the statement never reaches the engine). A nil error means the
// payload was NOT blocked — that is a P0 security hole.
func (r rec) blocked(name string, err error) bool {
	r.t.Helper()
	if err == nil {
		r.fail(name, reporter.SeverityP0, "payload NOT blocked — the malicious input reached execution without error")
		return false
	}
	return true
}

// wrapped asserts the blocked error wraps the expected public sentinel
// (errors.Is). Only used for the paths Quark documents as wrapping one.
// P1: callers reasonably branch on errors.Is(err, ErrInvalidJoin) etc., so a
// broken wrap is an API regression, not a cosmetic one.
func (r rec) wrapped(name string, err error, sentinel error) {
	r.t.Helper()
	if err != nil && !errors.Is(err, sentinel) {
		r.fail(name, reporter.SeverityP1, "blocked, but error does not wrap the public sentinel %v: got %v", sentinel, err)
	}
}

func TestNegativeSecurity(t *testing.T) {
	engines := selectedEngines()
	ctx := context.Background()

	conns, err := tools.Up(ctx, engines)
	if err != nil {
		t.Fatalf("bring up engines %v: %v", engines, err)
	}
	t.Cleanup(func() {
		var containerEngines []string
		for _, e := range engines {
			if e != tools.SQLite {
				containerEngines = append(containerEngines, e)
			}
		}
		tools.Down(containerEngines...)
	})

	for _, eng := range engines {
		conn := conns[eng]
		t.Run(eng, func(t *testing.T) {
			client, err := quark.New(conn.Driver, conn.DSN)
			if err != nil {
				t.Fatalf("quark.New(%q): %v", conn.Driver, err)
			}
			t.Cleanup(func() {
				_ = client.Close()
				if eng == tools.SQLite {
					_ = os.Remove(conn.DSN)
				}
			})
			// Migrate so the tables exist: the malicious queries must be
			// blocked *before* execution, and the post-block intact check
			// needs a real table.
			if err := client.Migrate(ctx, domain.AllModels()...); err != nil {
				t.Fatalf("migrate domain on %s: %v", eng, err)
			}

			t.Run("Identifier", func(t *testing.T) { maliciousIdentifier(t, ctx, client, eng) })
			t.Run("Operator", func(t *testing.T) { maliciousOperator(t, ctx, client, eng) })
			t.Run("JSONPath", func(t *testing.T) { maliciousJSONPath(t, ctx, client, eng) })
			t.Run("JoinOn", func(t *testing.T) { maliciousJoinOn(t, ctx, client, eng) })
			t.Run("RawQueryDisabled", func(t *testing.T) { rawQueryDisabled(t, ctx, client, eng) })
			t.Run("RawQueryInjection", func(t *testing.T) { rawQueryInjection(t, ctx, conn, eng) })
			t.Run("TenantID", func(t *testing.T) { tenantIDInjection(t, ctx, eng) })
		})
	}
}

// maliciousIdentifier: a column identifier carrying injection must be rejected
// by guard.ValidateIdentifier before the WHERE clause is built.
func maliciousIdentifier(t *testing.T, ctx context.Context, c *quark.Client, eng string) {
	r := newRec(t, eng)
	payloads := []string{
		"id; DROP TABLE orders--",
		"id) OR (1=1",
		"1=1",
		"id OR 1=1",
		"id/**/UNION/**/SELECT",
		"status'; DELETE FROM orders--",
	}
	for _, p := range payloads {
		_, err := quark.For[domain.Order](ctx, c).Where(p, "=", "x").Limit(1).List()
		r.blocked("Where/identifier ["+p+"]", err)
	}
	// The blocked DROP/DELETE payloads must not have reached the engine: a
	// clean query on the same table still works (table intact).
	if _, err := quark.For[domain.Order](ctx, c).Count(); err != nil {
		r.fail("Identifier/table-intact", reporter.SeverityP0, "orders table unusable after blocked payloads (possible leak): %v", err)
	}
}

// maliciousOperator: a non-whitelisted operator must be rejected by
// guard.ValidateOperator.
func maliciousOperator(t *testing.T, ctx context.Context, c *quark.Client, eng string) {
	r := newRec(t, eng)
	payloads := []string{
		"; DROP TABLE orders--",
		"OR",
		"= 1 OR 1=1 --",
		"UNION",
	}
	for _, p := range payloads {
		_, err := quark.For[domain.Order](ctx, c).Where("status", p, "x").Limit(1).List()
		r.blocked("Where/operator ["+p+"]", err)
	}
	// Anti-regression: a whitelisted operator must still be accepted, so the
	// guard isn't over-blocking. (A guard that rejects everything would pass
	// the negative checks above for the wrong reason.)
	if _, err := quark.For[domain.Order](ctx, c).Where("status", "=", "paid").Limit(1).List(); err != nil {
		r.fail("Where/operator/valid-accepted", reporter.SeverityP1, "a valid '=' operator was rejected: %v", err)
	}
}

// maliciousJSONPath: an injection in a WhereJSON path must be rejected by
// guard.ValidateJSONPath and wrap ErrInvalidJSONPath.
func maliciousJSONPath(t *testing.T, ctx context.Context, c *quark.Client, eng string) {
	r := newRec(t, eng)
	payloads := []string{
		"evil'; DROP--",
		"a.b')); DROP TABLE x--",
		"$.user.name",     // leading $ is rejected by design (dotted-chain grammar)
		"name = 1 OR 1=1", // spaces/operators not allowed
	}
	for _, p := range payloads {
		_, err := quark.For[domain.Organization](ctx, c).WhereJSON("settings", p, "=", "x").Count()
		if r.blocked("WhereJSON ["+p+"]", err) {
			r.wrapped("WhereJSON ["+p+"]", err, quark.ErrInvalidJSONPath)
		}
	}
}

// maliciousJoinOn: an injection in a JOIN ON clause (raw or per-arg) must be
// rejected by guard.ValidateJoinOn and wrap ErrInvalidJoin.
func maliciousJoinOn(t *testing.T, ctx context.Context, c *quark.Client, eng string) {
	r := newRec(t, eng)
	rawPayloads := []string{
		"orders.id = order_lines.order_id; DROP TABLE orders",
		"orders.id = order_lines.order_id -- comment",
		"orders.id = order_lines.order_id OR 1=1",
		"orders.id = order_lines.order_id /* x */",
	}
	for _, p := range rawPayloads {
		_, err := quark.For[domain.Order](ctx, c).
			Join("order_lines").OnRaw(p).Limit(1).List()
		if r.blocked("Join/OnRaw ["+p+"]", err) {
			r.wrapped("Join/OnRaw ["+p+"]", err, quark.ErrInvalidJoin)
		}
	}
	// Per-arg injection through the typed .On(left, op, right) form.
	_, err := quark.For[domain.Order](ctx, c).
		Join("order_lines").On("orders.id; DROP TABLE orders", "=", "order_lines.order_id").Limit(1).List()
	if r.blocked("Join/On(left-injection)", err) {
		r.wrapped("Join/On(left-injection)", err, quark.ErrInvalidJoin)
	}
}

// rawQueryDisabled: RawQuery/Exec must be refused while AllowRawQueries is
// false (the default) and wrap ErrInvalidQuery.
func rawQueryDisabled(t *testing.T, ctx context.Context, c *quark.Client, eng string) {
	r := newRec(t, eng)
	_, err := c.RawQuery(ctx, "SELECT 1 FROM orders WHERE id = ?", 1)
	if r.blocked("RawQuery/disabled", err) {
		r.wrapped("RawQuery/disabled", err, quark.ErrInvalidQuery)
	}
	if err := c.Exec(ctx, "DELETE FROM orders WHERE id = ?", 1); err != nil {
		// expected: blocked
		r.wrapped("Exec/disabled", err, quark.ErrInvalidQuery)
	} else {
		r.fail("Exec/disabled", reporter.SeverityP0, "Exec executed while AllowRawQueries is false")
	}
}

// rawQueryInjection: with AllowRawQueries enabled, guard.ValidateRawQuery must
// still block queries that omit placeholders or carry injection patterns.
func rawQueryInjection(t *testing.T, ctx context.Context, conn tools.EngineConn, eng string) {
	r := newRec(t, eng)
	raw, err := quark.New(conn.Driver, conn.DSN, quark.WithLimits(rawEnabledLimits()))
	if err != nil {
		t.Fatalf("quark.New(raw-enabled) on %s: %v", eng, err)
	}
	defer raw.Close()

	// No placeholders → blocked (RawQuery requires them).
	_, err = raw.RawQuery(ctx, "SELECT 1 FROM orders")
	r.blocked("RawQuery/no-placeholder", err)

	// Injection patterns (each carries a placeholder so it passes the
	// placeholder gate and is judged by the anti-injection regex).
	injections := []string{
		"SELECT * FROM orders WHERE id = ? UNION SELECT email FROM users",
		"SELECT * FROM orders WHERE id = ? OR 1=1",
		"SELECT * FROM orders WHERE id = ?; DROP TABLE orders",
		"SELECT * FROM orders WHERE id = ?; DELETE FROM orders",
		"SELECT * FROM orders WHERE id = ? -- AND 1=1", // SQL line-comment tail
	}
	for _, q := range injections {
		_, err := raw.RawQuery(ctx, q, 1)
		r.blocked("RawQuery/injection ["+q+"]", err)
	}

	// Exec (DML) shares ValidateRawQuery (requirePlaceholders=false). A
	// stacked-statement injection must be blocked before it executes.
	if err := raw.Exec(ctx, "UPDATE orders SET status = ? ; DROP TABLE orders", "x"); err == nil {
		r.fail("Exec/injection [;DROP]", reporter.SeverityP0,
			"stacked DROP via Exec was NOT blocked (executed)")
	}

	// NOTE (documented limitation, not a covered vector): block comments
	// (/* */) are intentionally allowed by ValidateRawQuery — they are valid
	// optimizer hints — so comment-based evasion like `UNION/**/SELECT` is NOT
	// caught here. The real boundary for raw queries is AllowRawQueries (off
	// by default) + placeholders for values. See the phase README and
	// docs/playbooks/security.md.
}

func rawEnabledLimits() quark.Limits {
	l := quark.DefaultLimits()
	l.AllowRawQueries = true
	return l
}

// tenantIDInjection: a tenant ID that escapes the ^[a-z0-9_-]+$ grammar must
// be rejected by the router before it is ever interpolated. Engine-agnostic
// (pure validation), exercised per engine for uniformity.
func tenantIDInjection(t *testing.T, ctx context.Context, eng string) {
	r := newRec(t, eng)
	type ctxKey string
	const key ctxKey = "tenant"
	router := quark.NewTenantRouter(
		quark.DefaultTenantConfig(),
		func(ctx context.Context) string { s, _ := ctx.Value(key).(string); return s },
		nil,
	)
	for _, bad := range []string{"'; DROP--", "acme OR 1=1", "a.b", "UPPER", "tenant;drop"} {
		_, err := router.ResolveTenant(context.WithValue(ctx, key, bad))
		r.blocked("ResolveTenant ["+bad+"]", err)
	}
	// A well-formed tenant id must still be accepted (no false positives).
	if _, err := router.ResolveTenant(context.WithValue(ctx, key, "acme-tenant_1")); err != nil {
		r.fail("ResolveTenant/valid", reporter.SeverityP1, "rejected a valid tenant id: %v", err)
	}
}
