package commands

import _ "embed"

//go:embed templates/model.go.tmpl
var modelTemplate string

//go:embed templates/migration.go.tmpl
var migrationTemplate string

//go:embed templates/seeder.go.tmpl
var seederTemplate string
