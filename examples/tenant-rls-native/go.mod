// This example is its own module so it can import the driver the way an
// application does — through the Quark driver module, which registers the
// database/sql driver AND teaches Quark to classify its errors (ADR-0023).
// Those modules import Quark, so the library module itself cannot require
// them; the local replace directives point at this tree instead.
module github.com/jcsvwinston/quark/examples/tenant-rls-native

go 1.25.7

replace github.com/jcsvwinston/quark => ../..

replace github.com/jcsvwinston/quark/drivers/postgres => ../../drivers/postgres

require (
	github.com/jcsvwinston/quark v1.12.0
	github.com/jcsvwinston/quark/drivers/postgres v0.1.0
)

require (
	github.com/gabriel-vasile/mimetype v1.4.15 // indirect
	github.com/go-playground/locales v0.14.1 // indirect
	github.com/go-playground/universal-translator v0.18.1 // indirect
	github.com/go-playground/validator/v10 v10.30.4 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.10.0 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/leodido/go-urn v1.5.0 // indirect
	golang.org/x/crypto v0.55.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
)
