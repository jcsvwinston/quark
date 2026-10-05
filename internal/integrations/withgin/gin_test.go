// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package withgin

import (
	"os"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/jcsvwinston/quark/internal/integrations/guide"
	"github.com/jcsvwinston/quark/internal/integrations/notestest"
)

// TestMain keeps Gin quiet: in its default debug mode it prints every route
// it registers.
func TestMain(m *testing.M) {
	gin.SetMode(gin.TestMode)
	os.Exit(m.Run())
}

func TestNotesAPI(t *testing.T) {
	notestest.Exercise(t, NewEngine(notestest.Client(t)))
}

func TestGuideMatchesFixture(t *testing.T) {
	guide.Check(t, "Gin", "gin.go")
}
