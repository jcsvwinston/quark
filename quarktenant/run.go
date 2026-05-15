// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quarktenant

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/jcsvwinston/quark"
)

// Action is the subcommand quarktenant is asked to run. The F5-3
// scope ships a single action; the type is kept open-ended so the
// follow-up F5-N items (drop, list, audit) can extend it without
// breaking the CLI shape.
type Action string

const (
	// ActionInstallRLSPolicies generates the RLS policy DDL for
	// every model registered on the [quark.Client]. With --dry-run,
	// the DDL is printed to stdout and no database changes are made.
	// Without --dry-run, the DDL is applied under a distributed
	// migration lock so concurrent installers are serialised.
	ActionInstallRLSPolicies Action = "install-rls-policies"
)

// Exit codes returned by [Run]:
//
//	0 — success: DDL printed (dry-run) or applied successfully.
//	2 — operational error (unsupported dialect, generation failure,
//	    apply failure, unknown action).
const (
	ExitSuccess = 0
	ExitError   = 2
)

// ParseAction returns the [Action] for the given CLI argument. The
// empty string and any unknown value return an error so the caller
// can map to [ExitError] and surface the failure with usage text.
func ParseAction(arg string) (Action, error) {
	switch Action(arg) {
	case ActionInstallRLSPolicies:
		return ActionInstallRLSPolicies, nil
	default:
		return "", fmt.Errorf("quarktenant: unknown action %q (expected: %s)",
			arg, ActionInstallRLSPolicies)
	}
}

// Run is the CLI entry point. Pass it the raw args slice (typically
// `os.Args[1:]`) and a [quark.Client] with models registered. Returns
// an exit code suitable for `os.Exit(...)`.
//
// Flags consumed (in addition to the action positional):
//
//	--dry-run                  print DDL, do not apply
//	--tenant-col <name>        column name (default: tenant_id)
//	--native-rls-var <name>    PG setting name (default: app.tenant_id)
//	--cast <sql>               cast appended after current_setting (default: text)
//	--no-force-rls             omit ALTER TABLE ... FORCE ROW LEVEL SECURITY
//	--lock-name <name>         migration lock name (default: quark_install_rls_policies)
//	--lock-timeout <duration>  migration lock acquire timeout (default: 30s)
//
// Output goes to stdout for DDL and stderr for errors / status; the
// destinations are overrideable with the [RunWithIO] variant for
// tests.
func Run(ctx context.Context, args []string, client *quark.Client) int {
	return RunWithIO(ctx, args, client, os.Stdout, os.Stderr)
}

// RunWithIO is [Run] with explicit io.Writer destinations. Used by
// the test suite to capture stdout/stderr without touching globals.
func RunWithIO(ctx context.Context, args []string, client *quark.Client, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintf(stderr, "quarktenant: missing action; expected one of: %s\n", ActionInstallRLSPolicies)
		return ExitError
	}
	action, err := ParseAction(args[0])
	if err != nil {
		fmt.Fprintln(stderr, err)
		return ExitError
	}

	opts := DefaultInstallOptions()
	fs := flag.NewFlagSet(string(action), flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&opts.DryRun, "dry-run", opts.DryRun, "print DDL, do not apply")
	fs.StringVar(&opts.TenantColumn, "tenant-col", opts.TenantColumn, "tenant column name")
	fs.StringVar(&opts.NativeRLSVar, "native-rls-var", opts.NativeRLSVar, "PostgreSQL session variable name")
	fs.StringVar(&opts.TenantColumnSQLCast, "cast", opts.TenantColumnSQLCast, "explicit SQL cast for current_setting result (default text)")
	fs.StringVar(&opts.LockName, "lock-name", opts.LockName, "distributed migration lock name")
	fs.DurationVar(&opts.LockTimeout, "lock-timeout", opts.LockTimeout, "migration lock acquire timeout")
	noForce := fs.Bool("no-force-rls", false, "skip ALTER TABLE ... FORCE ROW LEVEL SECURITY")
	if err := fs.Parse(args[1:]); err != nil {
		// flag already printed usage to stderr.
		return ExitError
	}
	if *noForce {
		opts.ForceRLS = false
	}

	switch action {
	case ActionInstallRLSPolicies:
		return runInstall(ctx, client, opts, stdout, stderr)
	default:
		// ParseAction already guards this; defensive only.
		fmt.Fprintf(stderr, "quarktenant: unhandled action %q\n", action)
		return ExitError
	}
}

func runInstall(ctx context.Context, client *quark.Client, opts InstallOptions, stdout, stderr io.Writer) int {
	stmts, err := InstallRLSPolicies(ctx, client, opts)
	if err != nil {
		// Surface the error. Pre-flight failures (no models,
		// unsupported dialect, ErrInvalidCast) return no statements;
		// apply failures inside the transaction return the full
		// rendered list (the tx rolled back, so nothing landed). We
		// emit the statements so the operator can inspect them, but
		// add a stderr marker that signals the rollback explicitly
		// — without it the operator might think some statements
		// were committed.
		fmt.Fprintf(stderr, "quarktenant: %v\n", err)
		if len(stmts) > 0 {
			fmt.Fprintf(stderr, "quarktenant: apply transaction rolled back; %d statements rendered but not committed\n", len(stmts))
			for _, s := range stmts {
				fmt.Fprintln(stdout, s+";")
			}
		}
		return ExitError
	}

	for _, s := range stmts {
		fmt.Fprintln(stdout, s+";")
	}
	if opts.DryRun {
		fmt.Fprintf(stderr, "quarktenant: dry-run — %d statements printed, no changes applied\n", len(stmts))
	} else {
		fmt.Fprintf(stderr, "quarktenant: applied %d statements\n", len(stmts))
	}
	return ExitSuccess
}
