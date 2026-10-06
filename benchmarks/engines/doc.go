// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package engines is the engine bench: Quark measured against the two
// baselines a Go program on PostgreSQL actually has — database/sql over pgx,
// and pgx's native pool — on a real PostgreSQL, plus MySQL's FindByPK with
// and without a reused prepared statement (and with Quark's statement cache),
// and a MySQL batch insert that reads its keys back.
//
// It is a meter, not a leaderboard. Each operation is one control with a
// RECORDED verdict and RECORDED ratios, and TestEngineBench checks a fresh
// measurement against both, the way the other benches of this repository
// check their probes: closing a gap, or opening one, turns the test red and
// asks for the record to move in the same change.
//
// What is recorded for time is a RATIO, never a time. Every round measures every arm
// of an operation back to back, in a rotating order, and the ratio of the
// round is Quark's time over the baseline's on the same machine and in the
// same stretch of wall clock; the bench publishes the median over the rounds.
// A slower machine slows all arms together, so the ratio survives a change of
// runner far better than a time does: across the CPU models GitHub's runners
// drew, absolute times differed by 40–50 % and ratios by at most 16 %. Not
// entirely, though, so the time checks are asserted only on the machine the
// record was taken on — the CI runner, and on it only the CPU models the
// reference runs drew — and allocations, which do not depend on the machine
// at all, are asserted everywhere.
//
// The benchmarks themselves live in the _test.go files: run.sh starts the
// engines and runs them the way the published page describes, and the
// ordinary `go test -bench` entry points (BenchmarkPostgres, BenchmarkMySQL)
// share the same operations for profiling. Without a DSN everything skips,
// so `go test ./...` in this module stays offline.
package engines
