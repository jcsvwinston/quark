// The driver template is a module of its own, as every Quark driver is: a
// driver module requires Quark and its engine's Go driver, and an application
// imports it for the registrations its init makes. Never published: its path
// is not this repository's, so the Go toolchain gives it no access to Quark's
// internal packages, the same as a third party's module; and the local
// replace directive points Quark at this tree, so the template and the guide
// that is checked against it always describe the code under review. A driver
// started from it drops the replace and requires a published Quark.
module example.com/drivertemplate

go 1.25.7

replace github.com/jcsvwinston/quark => ../..

require (
	github.com/jcsvwinston/quark v1.16.0
	modernc.org/sqlite v1.58.0
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/gabriel-vasile/mimetype v1.4.15 // indirect
	github.com/go-playground/locales v0.14.1 // indirect
	github.com/go-playground/universal-translator v0.18.1 // indirect
	github.com/go-playground/validator/v10 v10.30.4 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/leodido/go-urn v1.5.0 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/ncruces/go-strftime v1.0.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/crypto v0.55.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.41.0 // indirect
	modernc.org/libc v1.75.6 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.12.1 // indirect
)
