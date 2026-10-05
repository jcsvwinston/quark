// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package notestest

import (
	"testing"

	"github.com/jcsvwinston/quark/internal/integrations/guide"
)

// The guide's testing section shows how the battery opens its client and
// how it proves the request context reaches the database.
func TestGuideMatchesFixture(t *testing.T) {
	guide.Check(t, "Testing handlers", "notestest.go")
}
