// Package quarkbench is a reproducible benchmark harness that measures
// Quark's per-operation overhead against a hand-written database/sql
// baseline and against GORM, on the same schema, data, and operations.
//
// The benchmarks themselves live in the *_test.go files. This file only
// exists so the directory is a buildable, vettable Go package. See
// README.md for methodology and how to run.
package quarkbench
