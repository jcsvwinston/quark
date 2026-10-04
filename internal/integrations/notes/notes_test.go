// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package notes_test

import (
	"testing"

	"github.com/jcsvwinston/quark/internal/integrations/guide"
)

// The guide shows the model and the status mapping once, before the first
// framework, and every section builds on them.
func TestGuideMatchesFixture(t *testing.T) {
	guide.Check(t, "What every section serves", "notes.go")
}
