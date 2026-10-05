// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package withecho

import (
	"testing"

	"github.com/jcsvwinston/quark/internal/integrations/guide"
	"github.com/jcsvwinston/quark/internal/integrations/notestest"
)

func TestNotesAPI(t *testing.T) {
	notestest.Exercise(t, NewServer(notestest.Client(t)))
}

func TestGuideMatchesFixture(t *testing.T) {
	guide.Check(t, "Echo", "echo.go")
}
