package commands

import _ "embed"

//go:embed templates/model.go.tmpl
var modelTemplate string

//go:embed templates/migration.go.tmpl
var migrationTemplate string

//go:embed templates/seeder.go.tmpl
var seederTemplate string

// withNucleusModuleTemplate is the Nucleus module `quark init --with nucleus`
// writes. It is SOURCE TEXT on purpose: Nucleus imports Quark, so Quark can
// never compile against it (a cycle across the release train); the file is
// parsed by this package's tests and compiled by the suite's integration lane.
//
//go:embed templates/with_nucleus_module.go.tmpl
var withNucleusModuleTemplate string
