// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quarktenant

import (
	"context"
	"errors"
	"fmt"
	"reflect"
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

		rows, err := client.RawQuery(ctx, `
			SELECT c.relrowsecurity, c.relforcerowsecurity,
			       EXISTS (
			         SELECT 1 FROM pg_policies p
			         WHERE p.schemaname = n.nspname
			           AND p.tablename  = c.relname
			           AND p.policyname = $2
			       )
			FROM pg_class c
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE c.relname = $1
			  AND c.relkind = 'r'
			  AND n.nspname = current_schema()`,
			meta.Table, f.PolicyName)
		if err != nil {
			return nil, fmt.Errorf("quarktenant: verify table %q: %w", meta.Table, err)
		}
		if rows.Next() {
			f.TableExists = true
			if err := rows.Scan(&f.RowSecurityEnabled, &f.ForceRowSecurity, &f.PolicyPresent); err != nil {
				rows.Close()
				return nil, fmt.Errorf("quarktenant: verify table %q: scan: %w", meta.Table, err)
			}
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("quarktenant: verify table %q: %w", meta.Table, err)
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
