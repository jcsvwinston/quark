// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package withnucleus

import (
	"testing"

	"github.com/jcsvwinston/nucleus/pkg/nucleus"
	"github.com/jcsvwinston/nucleus/pkg/nucleustest"

	"github.com/jcsvwinston/quark/internal/integrations/guide"
	"github.com/jcsvwinston/quark/internal/integrations/notestest"
)

// The HTTP battery, against a Nucleus application booted in-process with
// the module mounted — authorization, CSRF and the framework's own error
// handler included. The deadline request goes to a second application
// whose middleware gives every request a deadline that has already passed.
func TestNotesAPI(t *testing.T) {
	t.Chdir(t.TempDir()) // the application writes its policy and state files to the working directory
	client := notestest.Client(t)

	srv := nucleustest.Start(t, nucleus.New().
		WithDatabases(nucleustest.TempSQLite(t)).
		Mount(Module(client)))
	late := nucleustest.Start(t, nucleus.New().
		WithDatabases(nucleustest.TempSQLite(t)).
		Use(notestest.Expired).
		Mount(Module(client)))

	notestest.Run(t, srv.BaseURL, late.BaseURL)
}

func TestGuideMatchesFixture(t *testing.T) {
	guide.Check(t, "Nucleus", "nucleus.go")
}
