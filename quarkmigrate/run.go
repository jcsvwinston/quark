// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package quarkmigrate is the thin CLI wrapper that turns a
// [quark.Client] + a set of Go model values into a plan/verify/apply
// workflow. It's designed to be embedded in a tiny user-side
// `main.go` so the user binary can drive migrations from CI, a
// Makefile, or a developer's shell — Quark itself ships no binary
// that knows about user models (Go's lack of runtime model
// registration makes that path require codegen, which is out of
// scope for F3-5).
//
// Usage:
//
//	// myapp/migrations/main.go
//	package main
//	import (
//	    "context"
//	    "os"
//	    "github.com/jcsvwinston/quark"
//	    "github.com/jcsvwinston/quark/quarkmigrate"
//	    "myapp/models"
//	)
//	func main() {
//	    client, err := quark.New(os.Getenv("QUARK_DIALECT"), os.Getenv("QUARK_DSN"))
//	    if err != nil { os.Exit(2) }
//	    defer client.Close()
//	    action, err := quarkmigrate.ParseAction(argOrEmpty(os.Args, 1))
//	    if err != nil { os.Exit(2) }
//	    os.Exit(quarkmigrate.Run(context.Background(), action, client,
//	        &models.User{}, &models.Order{}))
//	}
//
// Then in CI / Makefile:
//
//	go run ./migrations plan     # print plan, exit 0 (informational)
//	go run ./migrations verify   # exit 1 if schema has drifted (CI gate)
//	go run ./migrations apply    # actually run the plan
package quarkmigrate

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/jcsvwinston/quark"
)

// Action is the subcommand quarkmigrate is asked to run. Three
// shapes cover the realistic CLI surface; anything else (rollback,
// snapshot, generate-go-code, etc.) belongs to a separate tool.
type Action string

const (
	// ActionPlan prints the plan and returns 0. Use for
	// informational `quark plan`-style invocations where the
	// caller wants to see what would change.
	ActionPlan Action = "plan"

	// ActionVerify prints the plan and returns 0 if empty, 1 if
	// non-empty. Use as a CI gate to fail builds when the live
	// schema and the models have drifted.
	ActionVerify Action = "verify"

	// ActionApply prints the plan and (if non-empty) applies it.
	// Returns 0 on success or empty-plan, 2 on apply error.
	ActionApply Action = "apply"
)

// Exit codes returned by [Run]:
//
//	0 — success: plan/verify with empty plan, or apply succeeded.
//	1 — verify with non-empty plan (drift detected). Reserved for
//	    ActionVerify only; the other actions never return this.
//	2 — operational error (PlanMigration failed, ApplyPlan failed,
//	    or unknown action).
const (
	ExitSuccess       = 0
	ExitDriftDetected = 1
	ExitError         = 2
)

// ParseAction picks the [Action] from a string (typically the first
// positional arg of the user's CLI). Empty input defaults to
// [ActionPlan] — the safest default since it has no side effects.
// Returns an error with a human-readable message on unknown input
// so the caller can fmt.Fprintln(os.Stderr, err) before exiting.
func ParseAction(s string) (Action, error) {
	switch s {
	case "", "plan":
		return ActionPlan, nil
	case "verify":
		return ActionVerify, nil
	case "apply":
		return ActionApply, nil
	default:
		return "", fmt.Errorf("unknown action %q — want one of: plan, verify, apply", s)
	}
}

// Run is the orchestrator. It builds the plan, renders it to stdout
// (via [RunWithOutput]'s default of os.Stdout), and dispatches per
// the action. See the package godoc for usage examples and the
// constants block for exit-code semantics.
//
// Errors from [quark.Client.PlanMigration] and
// [quark.Client.ApplyPlan] are written to os.Stderr with full
// context; the caller doesn't need to handle them.
func Run(ctx context.Context, action Action, client *quark.Client, models ...any) int {
	return RunWithOutput(ctx, action, client, os.Stdout, os.Stderr, models...)
}

// RunWithOutput is the test-friendly variant of [Run] that takes
// explicit stdout / stderr writers. The public [Run] forwards to
// it with os.Stdout / os.Stderr. Test code can pass bytes.Buffer
// values to capture output and assert against it without messing
// with global os.Stdout redirection.
func RunWithOutput(ctx context.Context, action Action, client *quark.Client, stdout, stderr io.Writer, models ...any) int {
	plan, err := client.PlanMigration(ctx, models...)
	if err != nil {
		fmt.Fprintf(stderr, "PlanMigration failed: %v\n", err)
		return ExitError
	}
	fmt.Fprintln(stdout, renderPlan(plan))

	switch action {
	case ActionPlan:
		return ExitSuccess
	case ActionVerify:
		if plan.IsEmpty() {
			return ExitSuccess
		}
		fmt.Fprintln(stderr, "verify: schema has drifted from models — re-run with `apply` to bring them in sync")
		return ExitDriftDetected
	case ActionApply:
		if plan.IsEmpty() {
			return ExitSuccess
		}
		if err := client.ApplyPlan(ctx, plan); err != nil {
			fmt.Fprintf(stderr, "ApplyPlan failed: %v\n", err)
			return ExitError
		}
		fmt.Fprintln(stdout, "Plan applied successfully.")
		return ExitSuccess
	default:
		fmt.Fprintf(stderr, "unknown action %q\n", string(action))
		return ExitError
	}
}

// renderPlan produces the human-facing block printed at the top of
// every Run invocation. The format is intentionally minimal — Plan
// has its own String() — and prefixes with the short plan hash so
// the user can correlate runs against the resumable state table
// (`quark_migration_state.plan_hash`).
//
// Empty plans render as a single confidence-building line so the
// CI log isn't ambiguous about "nothing happened".
func renderPlan(p quark.Plan) string {
	if p.IsEmpty() {
		return "Schema is in sync — no changes required."
	}
	var b strings.Builder
	hash := p.Hash()
	if len(hash) > 8 {
		hash = hash[:8]
	}
	fmt.Fprintf(&b, "Plan (%s) — %d operation(s):\n", hash, len(p.Ops))
	b.WriteString(p.String())
	return b.String()
}
