// This example is its own module so it can import the driver the way an
// application does — through the Quark driver module, which registers the
// database/sql driver AND teaches Quark to classify its errors (ADR-0023).
// Those modules import Quark, so the library module itself cannot require
// them; the local replace directives point at this tree instead.
module github.com/jcsvwinston/quark/examples/mssql

go 1.25.7

replace github.com/jcsvwinston/quark => ../..

replace github.com/jcsvwinston/quark/drivers/mssql => ../../drivers/mssql

require (
	github.com/jcsvwinston/quark v1.13.0
	github.com/jcsvwinston/quark/drivers/mssql v0.1.0
)

require (
	github.com/gabriel-vasile/mimetype v1.4.15 // indirect
	github.com/go-playground/locales v0.14.1 // indirect
	github.com/go-playground/universal-translator v0.18.1 // indirect
	github.com/go-playground/validator/v10 v10.30.4 // indirect
	github.com/golang-sql/civil v0.0.0-20220223132316-b832511892a9 // indirect
	github.com/golang-sql/sqlexp v0.1.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/leodido/go-urn v1.5.0 // indirect
	github.com/microsoft/go-mssqldb v1.11.0 // indirect
	github.com/shopspring/decimal v1.4.0 // indirect
	golang.org/x/crypto v0.55.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
)
