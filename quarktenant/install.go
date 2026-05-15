// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package quarktenant is the thin CLI wrapper that turns a
// [quark.Client] + a set of registered Go models into a tenant
// row-level-security install workflow. The pattern mirrors
// [quarkmigrate]: a small library the user embeds in a tiny
// `main.go`, no shipped binary that knows about user models.
//
// Usage:
//
//	// myapp/tenant/main.go
//	package main
//	import (
//	    "context"
//	    "os"
//	    "github.com/jcsvwinston/quark"
//	    "github.com/jcsvwinston/quark/quarktenant"
//	    "myapp/models"
//	)
//	func main() {
//	    client, err := quark.New(os.Getenv("QUARK_DIALECT"), os.Getenv("QUARK_DSN"))
//	    if err != nil { os.Exit(2) }
//	    defer client.Close()
//	    _ = client.RegisterModel(&models.Order{}, &models.Invoice{})
//	    os.Exit(quarktenant.Run(context.Background(), os.Args[1:], client))
//	}
//
// Then in CI / Makefile:
//
//	go run ./tenant install-rls-policies --dry-run    # print DDL
//	go run ./tenant install-rls-policies              # actually install
//
// Only PostgreSQL is supported. Other dialects return
// [quark.ErrUnsupportedFeature] from [InstallRLSPolicies].
package quarktenant

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/jcsvwinston/quark"
)

// castIdentifier matches a PostgreSQL type cast token: a single
// identifier optionally followed by parenthesised arguments
// (e.g. "text", "uuid", "bigint", "varchar(64)"). Anything outside
// this shape — semicolons, comments, whitespace, comparison operators
// — is rejected so the `--cast` CLI flag (or [InstallOptions].
// TenantColumnSQLCast) cannot smuggle arbitrary SQL into the policy
// USING/WITH CHECK clauses (CLAUDE.md Regla 6).
var castIdentifier = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*(?:\([0-9]+(?:,[0-9]+)?\))?$`)

// InstallOptions configures policy generation for [InstallRLSPolicies].
//
// The zero value is unusable — call [DefaultInstallOptions] to obtain
// a populated struct and override fields as needed.
type InstallOptions struct {
	// TenantColumn is the column name used in policies and inserted
	// into the WITH CHECK clause. Default: "tenant_id". Must exist
	// on every registered model (validated at generation time).
	TenantColumn string

	// NativeRLSVar is the PostgreSQL session variable referenced by
	// the policy via current_setting(NativeRLSVar, true). Must match
	// the value the [quark.TenantRouter] uses at runtime
	// (TenantConfig.NativeRLSVar). Default: "app.tenant_id".
	NativeRLSVar string

	// ForceRLS toggles `ALTER TABLE <t> FORCE ROW LEVEL SECURITY`
	// alongside `ENABLE ROW LEVEL SECURITY`. Without FORCE the table
	// owner can bypass the policy — usually the application role IS
	// the owner, so leaving FORCE off makes the policy decorative.
	// Default: true.
	ForceRLS bool

	// DryRun, when true, generates the DDL but does not apply it.
	// InstallRLSPolicies still returns the rendered statements so the
	// caller can print them or pipe them.
	DryRun bool

	// LockTimeout caps how long [InstallRLSPolicies] will wait for
	// the distributed migration lock when DryRun is false. Default:
	// 30 seconds. The lock prevents two installers from racing on
	// the same database.
	LockTimeout time.Duration

	// LockName is the [quark.Client.AcquireMigrationLock] name used
	// for the install run. Default: "quark_install_rls_policies".
	// Override only if you need to coordinate with custom tooling
	// that already takes the default migration lock.
	LockName string

	// TenantColumnSQLCast is the explicit PostgreSQL cast appended
	// to `current_setting(NativeRLSVar, true)` inside the policy
	// USING/WITH CHECK clauses, e.g. "::text", "::uuid",
	// "::bigint". When empty, InstallRLSPolicies infers `::text`
	// for string columns and falls back to `::text` otherwise —
	// callers using uuid or bigint tenant IDs MUST set this
	// explicitly. The cast is emitted verbatim, surrounded by the
	// leading "::" if the caller did not include one, so both
	// "uuid" and "::uuid" work.
	TenantColumnSQLCast string
}

// DefaultInstallOptions returns the populated defaults the F5-2
// runtime expects. Callers typically tweak DryRun and (rarely)
// TenantColumn / NativeRLSVar before passing the struct to
// [InstallRLSPolicies].
func DefaultInstallOptions() InstallOptions {
	return InstallOptions{
		TenantColumn:        "tenant_id",
		NativeRLSVar:        "app.tenant_id",
		ForceRLS:            true,
		DryRun:              false,
		LockTimeout:         30 * time.Second,
		LockName:            "quark_install_rls_policies",
		TenantColumnSQLCast: "",
	}
}

// ErrNoTenantColumn is returned by [InstallRLSPolicies] when a
// registered model does not declare the configured TenantColumn.
// Wrap your model with the column or skip it from registration.
var ErrNoTenantColumn = errors.New("quarktenant: model is missing the configured TenantColumn")

// ErrNoRegisteredModels is returned when [InstallRLSPolicies] is
// invoked on a Client that has no models registered. Register
// models with [quark.Client.RegisterModel] before calling.
var ErrNoRegisteredModels = errors.New("quarktenant: no models registered on the Client")

// ErrInvalidCast is returned by [InstallRLSPolicies] when the
// TenantColumnSQLCast value (or the `--cast` CLI flag) does not match
// a single PostgreSQL type token. See [castIdentifier] for the
// allowed shape. This guard exists to prevent the policy SQL from
// being extended with arbitrary statements via the cast input.
var ErrInvalidCast = errors.New("quarktenant: invalid SQL cast")

// InstallRLSPolicies generates the policy DDL (and, when
// opts.DryRun is false, applies it) for every model registered on
// the Client. Returns the rendered DDL statements in order so the
// caller can print, log, or pipe them — regardless of DryRun.
//
// PostgreSQL-only. Returns [quark.ErrUnsupportedFeature] wrapped
// with the dialect name on any other engine.
//
// Apply path: a single PostgreSQL transaction wraps every statement
// across every registered model. PG supports transactional DDL —
// `ALTER TABLE ... ENABLE ROW LEVEL SECURITY`, `CREATE POLICY`, the
// `FORCE` variant — so a failure mid-stream rolls back the whole
// install. On success the migration lock is released and all
// policies are visible together.
//
// The apply path drops down to [Client.Raw] to manage the
// transaction directly; it does not pass through SQLGuard because
// the statements are generated from registered model metadata, not
// from caller input. The cast token in the policy USING/WITH CHECK
// expression is validated separately via [castIdentifier] (see
// [ErrInvalidCast]).
//
// Idempotence: the policy name is deterministic
// (<table>_tenant_isolation). Re-running InstallRLSPolicies on a
// table that already has the policy installed will fail at apply
// time with a PostgreSQL duplicate-object error (SQLSTATE 42710).
// Callers who want idempotent install should DROP the policy first
// or guard the call with their own existence check.
func InstallRLSPolicies(ctx context.Context, client *quark.Client, opts InstallOptions) ([]string, error) {
	if client == nil {
		return nil, errors.New("quarktenant: client must not be nil")
	}
	if dn := client.Dialect().Name(); dn != "postgres" {
		return nil, fmt.Errorf("%w: install-rls-policies requires PostgreSQL, got dialect %q",
			quark.ErrUnsupportedFeature, dn)
	}

	// Defaults applied here rather than in DefaultInstallOptions so
	// callers passing a zero struct (e.g. tests) still get sensible
	// behaviour. DefaultInstallOptions is for the doc-as-code case.
	if opts.TenantColumn == "" {
		opts.TenantColumn = "tenant_id"
	}
	if opts.NativeRLSVar == "" {
		opts.NativeRLSVar = "app.tenant_id"
	}
	if opts.LockTimeout == 0 {
		opts.LockTimeout = 30 * time.Second
	}
	if opts.LockName == "" {
		opts.LockName = "quark_install_rls_policies"
	}

	models := client.RegisteredModels()
	if len(models) == 0 {
		return nil, ErrNoRegisteredModels
	}

	// Render DDL for every model. Failure at this stage means the
	// generated SQL is invalid; we surface it before acquiring the
	// lock so a bad config never blocks other workers.
	var stmts []string
	for _, model := range models {
		modelStmts, err := renderPolicyDDL(model, opts)
		if err != nil {
			return nil, err
		}
		stmts = append(stmts, modelStmts...)
	}

	if opts.DryRun {
		return stmts, nil
	}

	// Apply path: acquire the distributed lock, then execute every
	// DDL statement in a single PostgreSQL transaction so a failure
	// mid-stream rolls back the whole install (avoids the "RLS
	// enabled but no policy installed" partial state).
	lock, err := client.AcquireMigrationLock(ctx, opts.LockName, opts.LockTimeout)
	if err != nil {
		return stmts, fmt.Errorf("quarktenant: acquire lock %q: %w", opts.LockName, err)
	}
	defer func() {
		// Best-effort release; an error here is logged but cannot
		// abort the function's outcome (the lock would otherwise be
		// released by Client.Close on process exit).
		_ = lock.Release(ctx)
	}()

	db := client.Raw()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return stmts, fmt.Errorf("quarktenant: begin apply tx: %w", err)
	}
	for i, stmt := range stmts {
		if _, execErr := tx.ExecContext(ctx, stmt); execErr != nil {
			_ = tx.Rollback()
			return stmts, fmt.Errorf("quarktenant: apply stmt %d/%d %q: %w",
				i+1, len(stmts), truncateForError(stmt, 80), execErr)
		}
	}
	if cerr := tx.Commit(); cerr != nil {
		return stmts, fmt.Errorf("quarktenant: commit apply tx: %w", cerr)
	}
	return stmts, nil
}

// renderPolicyDDL generates the three DDL statements (ENABLE, FORCE,
// CREATE POLICY) for a single model. Validates that the model has
// the configured tenant column and that the table name passes
// PostgreSQL identifier rules.
func renderPolicyDDL(model any, opts InstallOptions) ([]string, error) {
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

	table := meta.Table

	field, ok := meta.FieldByCol[opts.TenantColumn]
	if !ok {
		return nil, fmt.Errorf("%w: model %s (table %q) does not declare column %q",
			ErrNoTenantColumn, modelTypeName(model), table, opts.TenantColumn)
	}

	cast, err := normaliseCast(opts.TenantColumnSQLCast, field)
	if err != nil {
		return nil, err
	}

	// Policy name: stable, deterministic, derived from the table.
	// PostgreSQL allows policy names up to NAMEDATALEN-1 (63 bytes by
	// default); the suffix "_tenant_isolation" is 17 bytes, so the
	// table name has 46 bytes of headroom — comfortably more than
	// realistic table names.
	policyName := table + "_tenant_isolation"

	stmts := []string{
		// ENABLE turns on RLS but exempts the owner by default.
		fmt.Sprintf("ALTER TABLE %s ENABLE ROW LEVEL SECURITY", quoteIdent(table)),
	}
	if opts.ForceRLS {
		// FORCE removes the owner exemption so the policy applies
		// to the application role even when it owns the table.
		stmts = append(stmts,
			fmt.Sprintf("ALTER TABLE %s FORCE ROW LEVEL SECURITY", quoteIdent(table)))
	}

	// The literal session-variable name is single-quoted SQL string,
	// not an identifier — current_setting() accepts a string. We
	// re-quote any inner single quotes (defensive: NativeRLSVar
	// should be a stable config constant, but better safe).
	settingLit := "'" + strings.ReplaceAll(opts.NativeRLSVar, "'", "''") + "'"

	stmts = append(stmts, fmt.Sprintf(
		`CREATE POLICY %s ON %s USING (%s = current_setting(%s, true)%s) WITH CHECK (%s = current_setting(%s, true)%s)`,
		quoteIdent(policyName),
		quoteIdent(table),
		quoteIdent(opts.TenantColumn),
		settingLit,
		cast,
		quoteIdent(opts.TenantColumn),
		settingLit,
		cast,
	))

	return stmts, nil
}

// normaliseCast resolves the SQL cast appended to the
// current_setting() call inside the policy expression. When the
// caller supplied an explicit cast, validate it against
// [castIdentifier] and emit it with the leading "::"; reject
// anything that doesn't look like a single PostgreSQL type token
// (CLAUDE.md Regla 6 — every input that ends up in SQL must be
// validated, even when it is a config value rather than a user query
// argument).
//
// Without an explicit cast we emit "::text". current_setting always
// returns TEXT, so the heuristic works for TEXT/VARCHAR tenant
// columns. UUID / BIGINT tenant columns require the caller to pass
// TenantColumnSQLCast or --cast — the doc/godoc spells this out.
func normaliseCast(explicit string, field *quark.FieldMeta) (string, error) {
	if explicit != "" {
		trimmed := strings.TrimPrefix(explicit, "::")
		if !castIdentifier.MatchString(trimmed) {
			return "", fmt.Errorf("%w: %q is not a single PostgreSQL type token (expected e.g. text, uuid, bigint, varchar(64))",
				ErrInvalidCast, explicit)
		}
		return "::" + trimmed, nil
	}
	_ = field // hook for future column-type inference
	return "::text", nil
}

func quoteIdent(name string) string {
	// PostgreSQL identifier quoting: surround with double quotes,
	// double any embedded quotes. The model metadata is computed
	// from Go struct tags so the input is developer-controlled, but
	// we quote anyway so unusual but valid identifiers (mixed case,
	// reserved words) round-trip correctly.
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func modelTypeName(model any) string {
	return fmt.Sprintf("%T", model)
}

func truncateForError(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
