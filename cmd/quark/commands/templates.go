package commands

import _ "embed"

//go:embed templates/model.go.tmpl
var modelTemplate string

//go:embed templates/migration.go.tmpl
var migrationTemplate string

//go:embed templates/seeder.go.tmpl
var seederTemplate string

// withServerHTTPTemplate and withServerGRPCTemplate are the
// cmd/<app>-server/main.go `quark init --with` writes beside a chi, Echo,
// Gin or gRPC package: it opens the process's one client, migrates the
// model and serves the package. The package itself is not a template — it is
// a copy of a tested fixture (integrationFixtures). Neither main compiles
// here, since both import the project's packages; TestInitWithBuilds builds,
// vets and tests what they render in a project of its own, against this
// tree.
//
//go:embed templates/with_server_http.go.tmpl
var withServerHTTPTemplate string

//go:embed templates/with_server_grpc.go.tmpl
var withServerGRPCTemplate string
