// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package quarktenant_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jcsvwinston/quark/quarktenant"
)

// verify-rls-policies shared one FlagSet with the install action and read
// only ForceRLS from it, so every other flag was accepted and ignored
// (QCD-QK-3). The dangerous case is not cosmetic: verifying with the
// default --tenant-col against an installation made with another column
// returned OK without having checked what the caller believed.
//
// Flags that do not change the verdict are now refused for this action
// rather than silently dropped.
func TestRunVerify_RejectsInstallOnlyFlags(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name   string
		args   []string
		wantIn string
	}{
		{"dry_run", []string{"verify-rls-policies", "--dry-run"}, "--dry-run"},
		{"lock_name", []string{"verify-rls-policies", "--lock-name", "x"}, "--lock-name"},
		{"lock_timeout", []string{"verify-rls-policies", "--lock-timeout", "5s"}, "--lock-timeout"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out, errOut strings.Builder
			code := quarktenant.RunWithIO(ctx, tc.args, nil, &out, &errOut)
			if code != quarktenant.ExitError {
				t.Fatalf("an install-only flag must be refused, got exit %d\nstderr: %s", code, errOut.String())
			}
			if !strings.Contains(errOut.String(), tc.wantIn) {
				t.Errorf("the error must name the offending flag %q, got: %s", tc.wantIn, errOut.String())
			}
		})
	}
}

// The flags that DO change the verdict must stay accepted: the predicate
// check needs them to know what a correct policy looks like.
func TestRunVerify_AcceptsVerdictFlags(t *testing.T) {
	ctx := context.Background()
	var out, errOut strings.Builder
	// nil client fails later, on the nil-client guard — what matters here
	// is that flag parsing did not refuse these.
	_ = quarktenant.RunWithIO(ctx, []string{"verify-rls-policies", "--tenant-col", "org_id", "--native-rls-var", "app.org"}, nil, &out, &errOut)
	for _, rejected := range []string{"--tenant-col", "--native-rls-var"} {
		if strings.Contains(errOut.String(), "not applicable") && strings.Contains(errOut.String(), rejected) {
			t.Errorf("%s changes the verdict and must be accepted, got: %s", rejected, errOut.String())
		}
	}
}

// The runner re-prefixed an error that already carried the package prefix
// (QCD-QK-1, cosmetic half).
func TestRunVerify_DoesNotDoublePrefix(t *testing.T) {
	ctx := context.Background()
	var out, errOut strings.Builder
	_ = quarktenant.RunWithIO(ctx, []string{"verify-rls-policies"}, nil, &out, &errOut)
	if strings.Contains(errOut.String(), "quarktenant: quarktenant:") {
		t.Errorf("the prefix must not be doubled, got: %s", errOut.String())
	}
}
