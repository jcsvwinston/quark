// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quarktenant

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/jcsvwinston/quark"
)

// ErrRLSNotEnforced reports that at least one registered model's table does
// not actually enforce engine-level row security. This is THE dangerous
// misconfiguration of [quark.RowLevelSecurityNative]: the router happily
// sets the session variable on every query, but with no policy installed
// (or with the owner exemption still active) PostgreSQL applies no
// predicate — every tenant reads every row, silently.
var ErrRLSNotEnforced = errors.New("quarktenant: row-level security is not enforced")

// VerifyFinding describes one table whose engine-level enforcement is
// incomplete. Zero findings means every registered model's table is
// enforced as configured.
type VerifyFinding struct {
	// Table is the model's table name.
	Table string
	// TableExists is false when the table is missing entirely (run your
	// migrations first — there is nothing to enforce on).
	TableExists bool
	// RowSecurityEnabled mirrors pg_class.relrowsecurity
	// (ALTER TABLE … ENABLE ROW LEVEL SECURITY).
	RowSecurityEnabled bool
	// ForceRowSecurity mirrors pg_class.relforcerowsecurity
	// (ALTER TABLE … FORCE ROW LEVEL SECURITY). Without it the table
	// OWNER bypasses every policy — and the application role usually IS
	// the owner, so the policy is decorative. Only demanded when
	// [InstallOptions].ForceRLS is true (the default).
	ForceRowSecurity bool
	// PolicyPresent reports whether the deterministic policy
	// (<table>_tenant_isolation) exists in pg_policies for the current
	// schema.
	PolicyPresent bool
	// PolicyName is the policy name that was looked up.
	PolicyName string
	// PolicyCommand mirrors pg_policies.cmd ("ALL" for the policy this
	// package installs). A policy narrowed to SELECT leaves every write
	// path unprotected.
	PolicyCommand string
	// PredicateGap explains why the installed policy does not isolate,
	// empty when the predicate is sound. A policy can carry the right
	// NAME and still read every tenant's rows — `USING (true)`, a filter
	// on the wrong column, or a read of the wrong session variable — so
	// the name alone proves nothing.
	PredicateGap string
}

// gap returns the human description of what is missing, empty when the
// finding is fully enforced under the given expectation.
func (f VerifyFinding) gap(expectForce bool) string {
	if !f.TableExists {
		return "table does not exist (run your migrations before verifying)"
	}
	var parts []string
	if !f.RowSecurityEnabled {
		parts = append(parts, "ENABLE ROW LEVEL SECURITY is not set")
	}
	if expectForce && !f.ForceRowSecurity {
		parts = append(parts, "FORCE ROW LEVEL SECURITY is not set (the table owner bypasses the policy)")
	}
	if !f.PolicyPresent {
		parts = append(parts, fmt.Sprintf("policy %q is not installed", f.PolicyName))
	} else if f.PredicateGap != "" {
		parts = append(parts, f.PredicateGap)
	}
	return strings.Join(parts, "; ")
}

// VerifyRLSPolicies checks, for every model registered on the client, that
// the table actually enforces the row security [InstallRLSPolicies]
// installs: RLS enabled, the FORCE variant when opts.ForceRLS (the
// default), and the deterministic <table>_tenant_isolation policy present
// in the current schema.
//
// It is the boot-time guardrail for [quark.RowLevelSecurityNative]: a
// router configured Native against a database where the DDL was never
// applied emits NO predicate — a silent cross-tenant leak. Call this at
// startup (fail the boot on error) or gate deploys on the
// verify-rls-policies action of your tenant runner ([Run]).
//
// The returned slice contains one entry per DEFICIENT table (empty slice +
// nil error = fully enforced). When any table is deficient the error wraps
// [ErrRLSNotEnforced] and names every table with its exact gap and the
// remedy. Only PostgreSQL is supported; other dialects return
// [quark.ErrUnsupportedFeature], and a client with no registered models
// returns [ErrNoRegisteredModels] — verifying nothing proves nothing.
func VerifyRLSPolicies(ctx context.Context, client *quark.Client, opts InstallOptions) ([]VerifyFinding, error) {
	if client == nil {
		return nil, errors.New("quarktenant: client must not be nil")
	}
	if dn := client.Dialect().Name(); dn != "postgres" {
		return nil, fmt.Errorf("%w: verify-rls-policies requires PostgreSQL, got dialect %q",
			quark.ErrUnsupportedFeature, dn)
	}

	models := client.RegisteredModels()
	if len(models) == 0 {
		return nil, ErrNoRegisteredModels
	}

	var findings []VerifyFinding
	for _, model := range models {
		t := reflect.TypeOf(model)
		if t == nil {
			return nil, fmt.Errorf("quarktenant: cannot resolve metadata for untyped-nil model")
		}
		if t.Kind() == reflect.Ptr {
			t = t.Elem()
		}
		meta := quark.GetModelMetaByType(t)
		if meta == nil {
			return nil, fmt.Errorf("quarktenant: cannot resolve metadata for model %T", model)
		}

		f := VerifyFinding{
			Table:      meta.Table,
			PolicyName: meta.Table + "_tenant_isolation",
		}

		// client.Raw() rather than client.RawQuery: this is a catalog
		// query built from registered model metadata, never from caller
		// input, so it does not belong behind the SQLGuard raw-query
		// switch — the same reasoning InstallRLSPolicies already applies
		// on its apply path. Requiring AllowRawQueries here made the
		// guardrail unusable from the very client the package documents
		// (QCD-QK-1): the preflight failed indistinguishably from a real
		// outage.
		var (
			qual      sql.NullString
			withCheck sql.NullString
			cmd       sql.NullString
		)
		row := client.Raw().QueryRowContext(ctx, `
			SELECT c.relrowsecurity, c.relforcerowsecurity,
			       p.polname IS NOT NULL,
			       pg_get_expr(p.polqual,      p.polrelid),
			       pg_get_expr(p.polwithcheck, p.polrelid),
			       p.polcmd
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			LEFT JOIN pg_policy p
			       ON p.polrelid = c.oid
			      AND p.polname  = $2
			WHERE c.relname = $1
			  AND c.relkind = 'r'
			  AND n.nspname = current_schema()`,
			meta.Table, f.PolicyName)

		switch err := row.Scan(&f.RowSecurityEnabled, &f.ForceRowSecurity, &f.PolicyPresent, &qual, &withCheck, &cmd); {
		case errors.Is(err, sql.ErrNoRows):
			// Table absent: TableExists stays false and gap() says so.
		case err != nil:
			return nil, fmt.Errorf("quarktenant: verify table %q: %w", meta.Table, err)
		default:
			f.TableExists = true
			f.PolicyCommand = policyCommandName(cmd.String)
			if f.PolicyPresent {
				f.PredicateGap = predicateGap(qual.String, withCheck.String, f.PolicyCommand, meta.Table, opts)
			}
		}

		if f.gap(opts.ForceRLS) != "" {
			findings = append(findings, f)
		}
	}

	if len(findings) == 0 {
		return nil, nil
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%v — the Native RLS router would serve these tables WITHOUT any tenant predicate:", ErrRLSNotEnforced)
	for _, f := range findings {
		fmt.Fprintf(&b, "\n  %s: %s", f.Table, f.gap(opts.ForceRLS))
	}
	b.WriteString("\nRemedy: run install-rls-policies (quarktenant.InstallRLSPolicies, or the install-rls-policies action of your tenant runner).")
	return findings, fmt.Errorf("%w%s", ErrRLSNotEnforced, strings.TrimPrefix(b.String(), ErrRLSNotEnforced.Error()))
}

// policyCommandName renders pg_policy.polcmd, whose stored form is a single
// character, as the SQL keyword an operator would recognise.
func policyCommandName(raw string) string {
	switch raw {
	case "*":
		return "ALL"
	case "r":
		return "SELECT"
	case "a":
		return "INSERT"
	case "w":
		return "UPDATE"
	case "d":
		return "DELETE"
	case "":
		return ""
	}
	return raw
}

// currentSettingCall matches a current_setting('<name>' …) call and captures
// the variable name, tolerating the ::text cast PostgreSQL adds when it
// normalises the expression it stores.
var currentSettingCall = regexp.MustCompile(`current_setting\(\s*'([^']+)'`)

// predicateGap reports why an installed policy fails to isolate tenants,
// and returns "" when the predicate is sound.
//
// This exists because checking that a policy NAMED <table>_tenant_isolation
// exists proves nothing about what it does (QCD-QK-2). A policy carrying
// the right name with `USING (true)` passed the preflight while every
// tenant read every row — a green light that stops an operator from
// looking, which is worse than no check at all.
//
// The check is deliberately structural rather than a string comparison
// against the installed DDL: PostgreSQL normalises what it stores (casts
// added, whitespace collapsed, identifiers requoted), so an exact match
// would fail on correct policies. It demands the two things that make the
// predicate isolate at all — a reference to the tenant COLUMN, and a read
// of the expected session VARIABLE — which is the minimum the audit that
// found this asked for.
//
// Scope, stated so the guarantee is not overread: it verifies THE policy
// this package installs (one policy, FOR ALL). A deployment that isolates
// through several hand-written policies, or that restricts by role, is not
// something this function can judge, and it will report the deviation
// rather than guess.
func predicateGap(qual, withCheck, cmd, table string, opts InstallOptions) string {
	column := strings.TrimSpace(opts.TenantColumn)
	if column == "" {
		column = "tenant_id"
	}
	variable := strings.TrimSpace(opts.NativeRLSVar)
	if variable == "" {
		variable = "app.tenant_id"
	}

	if cmd != "" && cmd != "ALL" {
		return fmt.Sprintf("policy %q applies only to %s, so every other command runs with NO tenant predicate",
			table+"_tenant_isolation", cmd)
	}

	if gap := expressionGap("USING", qual, column, variable); gap != "" {
		return gap
	}
	// A policy with no WITH CHECK reuses USING for the write path, which is
	// what the installed policy's equivalent form means — sound. One that
	// declares its own must isolate too, or a tenant can plant rows under
	// another tenant's identifier.
	if strings.TrimSpace(withCheck) != "" {
		if gap := expressionGap("WITH CHECK", withCheck, column, variable); gap != "" {
			return gap
		}
	}
	return ""
}

// expressionGap applies the two structural demands to one policy
// expression, naming precisely which one failed.
func expressionGap(label, expr, column, variable string) string {
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return fmt.Sprintf("the policy's %s expression is empty, so it filters nothing", label)
	}

	// The session variable is read first, and its literal is REMOVED from
	// the expression before looking for the column. Without this the check
	// fools itself: the default variable "app.tenant_id" contains the
	// default column "tenant_id" as a substring, so `status =
	// current_setting('app.tenant_id', true)` — which isolates nothing —
	// would appear to reference the tenant column.
	var found []string
	for _, m := range currentSettingCall.FindAllStringSubmatch(trimmed, -1) {
		found = append(found, m[1])
	}
	if len(found) == 0 {
		return fmt.Sprintf("the policy's %s expression (%s) never reads a session variable, so it applies the same predicate to every tenant",
			label, condense(trimmed))
	}
	matched := false
	for _, v := range found {
		if v == variable {
			matched = true
			break
		}
	}
	if !matched {
		return fmt.Sprintf("the policy's %s expression reads %s, but the Native RLS router sets %q — the predicate can never match",
			label, quoteList(found), variable)
	}

	stripped := currentSettingCall.ReplaceAllString(trimmed, "current_setting(")
	if !identifierReferenced(stripped, column) {
		return fmt.Sprintf("the policy's %s expression (%s) does not filter on the tenant column %q",
			label, condense(trimmed), column)
	}
	return ""
}

// identifierReferenced reports whether expr uses name as a standalone SQL
// identifier, so that a column called "id" is not considered referenced by
// "tenant_id".
func identifierReferenced(expr, name string) bool {
	re, err := regexp.Compile(`(^|[^\w."])` + regexp.QuoteMeta(name) + `($|[^\w."])`)
	if err != nil {
		return strings.Contains(expr, name)
	}
	return re.MatchString(expr)
}

func quoteList(vals []string) string {
	parts := make([]string, 0, len(vals))
	for _, v := range vals {
		parts = append(parts, fmt.Sprintf("%q", v))
	}
	return strings.Join(parts, ", ")
}

// condense keeps an error message readable when a hand-written policy
// carries a long expression.
func condense(expr string) string {
	flat := strings.Join(strings.Fields(expr), " ")
	if len(flat) <= 80 {
		return flat
	}
	return flat[:77] + "…"
}
