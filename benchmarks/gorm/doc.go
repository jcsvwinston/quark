// Package gormbench holds the GORM comparison benchmarks. It is a separate
// package (and test binary) from the Quark/raw benchmarks so the glebarez
// SQLite driver and modernc.org/sqlite do not both try to register the
// database/sql "sqlite" driver in one binary. The benchmarks live in the
// *_test.go files; this file exists only to make the directory a buildable,
// vettable package.
package gormbench
