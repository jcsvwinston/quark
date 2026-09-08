// This example is its own module so it can import the driver the way an
// application does — through the Quark driver module, which registers the
// database/sql driver AND teaches Quark to classify its errors (a duplicate
// title answers 409 below). Those modules import Quark, so the library
// module itself cannot require them; the local replace directives point at
// this tree instead. Drop the two replace lines to use it as a template.
module github.com/jcsvwinston/quark/examples/gin

go 1.25.7

replace github.com/jcsvwinston/quark => ../..

replace github.com/jcsvwinston/quark/drivers/sqlite => ../../drivers/sqlite

require (
	github.com/gin-gonic/gin v1.12.0
	github.com/jcsvwinston/quark v1.11.0
	github.com/jcsvwinston/quark/drivers/sqlite v0.1.2
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/bytedance/gopkg v0.1.3 // indirect
	github.com/bytedance/sonic v1.15.0 // indirect
	github.com/bytedance/sonic/loader v0.5.0 // indirect
	github.com/cloudwego/base64x v0.1.6 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/gabriel-vasile/mimetype v1.4.15 // indirect
	github.com/gin-contrib/sse v1.1.0 // indirect
	github.com/go-playground/locales v0.14.1 // indirect
	github.com/go-playground/universal-translator v0.18.1 // indirect
<<<<<<< HEAD
	github.com/go-playground/validator/v10 v10.30.4 // indirect
=======
	github.com/go-playground/validator/v10 v10.30.1 // indirect
>>>>>>> origin/main
	github.com/go-sql-driver/mysql v1.10.1 // indirect
	github.com/goccy/go-json v0.10.5 // indirect
	github.com/goccy/go-yaml v1.19.2 // indirect
	github.com/golang-sql/civil v0.0.0-20220223132316-b832511892a9 // indirect
	github.com/golang-sql/sqlexp v0.1.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.10.0 // indirect
	github.com/jackc/puddle/v2 v2.2.2 // indirect
	github.com/json-iterator/go v1.1.12 // indirect
	github.com/klauspost/cpuid/v2 v2.3.0 // indirect
<<<<<<< HEAD
	github.com/leodido/go-urn v1.5.0 // indirect
=======
	github.com/leodido/go-urn v1.4.0 // indirect
>>>>>>> origin/main
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/microsoft/go-mssqldb v1.11.0 // indirect
	github.com/modern-go/concurrent v0.0.0-20180306012644-bacd9c7ef1dd // indirect
	github.com/modern-go/reflect2 v1.0.2 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/pelletier/go-toml/v2 v2.2.4 // indirect
	github.com/quic-go/qpack v0.6.0 // indirect
	github.com/quic-go/quic-go v0.59.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/shopspring/decimal v1.4.0 // indirect
	github.com/sijms/go-ora/v2 v2.9.0 // indirect
	github.com/twitchyliquid64/golang-asm v0.15.1 // indirect
	github.com/ugorji/go/codec v1.3.1 // indirect
	go.mongodb.org/mongo-driver/v2 v2.5.0 // indirect
	golang.org/x/arch v0.22.0 // indirect
	golang.org/x/crypto v0.55.0 // indirect
<<<<<<< HEAD
	golang.org/x/net v0.58.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
=======
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
>>>>>>> origin/main
	modernc.org/libc v1.75.6 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
	modernc.org/sqlite v1.58.0 // indirect
)
