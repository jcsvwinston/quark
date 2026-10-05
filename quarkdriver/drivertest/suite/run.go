// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

// Package suite is the engine conformance suite: what Quark checks on each of
// the six engines it ships, in a form a driver module outside this repository
// runs against its own engine.
//
// The dialect kit (drivertest.VerifyDialect) asks whether a dialect writes
// SQL its engine accepts and means what the contract says. This suite asks
// the next question: whether Quark as a whole — CRUD, the query builder,
// transactions and savepoints, hooks, soft deletes, optimistic locking,
// dirty tracking, preloads and joins, subqueries and window functions, batch
// writes, JSON and array columns, time zones, Sync and reversible migrations,
// the unique-violation classifier — behaves on that engine as it does on the
// six it ships. A driver runs both:
//
//	func TestEngineSuite(t *testing.T) {
//	    client, err := quark.New("extsql", os.Getenv("EXTSQL_TEST_DSN"))
//	    if err != nil {
//	        t.Fatal(err)
//	    }
//	    defer client.Close()
//	    suite.Run(t, client)
//	}
//
// The subtests are the ones internal/enginesuite ran in its shared suite,
// moved here unchanged: the in-repo engines run this package, through
// internal/enginesuite, in every integration lane. What stays behind in
// internal/enginesuite is engine-specific — checks that branch on a built-in
// engine's name, start a container, or need Redis or an OpenTelemetry
// collector — and the dialect kit covers its dialect half for any engine.
//
// The suite creates and drops its own tables (dropped before they are
// created, so a run that died half-way does not poison the next), and some
// subtests open transactions on more than one connection: give the client a
// database it may write to and a pool of more than one connection. A subtest
// whose feature the engine lacks fails; skip it by name with go test's -skip
// flag, and say why in the driver's documentation.
//
// The package imports Quark and the standard library only — no driver, no
// container library — so importing it adds nothing to a driver module's
// go.mod that requiring Quark did not.
package suite

import (
	"context"
	"testing"

	"github.com/jcsvwinston/quark"
)

// subtests are the suite, in the order internal/enginesuite ran them.
var subtests = []struct {
	name string
	run  func(context.Context, *testing.T, *quark.Client)
}{
	{"CRUD", testCRUD},
	{"QueryBuilder", testQueryBuilder},
	{"Transactions", testTransactions},
	{"SavepointHookUnwind", testSavepointHookUnwind},
	{"Hooks", testHooks},
	{"Validation", testValidation},
	{"SoftDelete", testSoftDelete},
	{"Pagination", testPagination},
	{"IdentifierSecurity", testIdentifierSecurity},
	{"M2MLinkErrors", testM2MLinkErrors},
	{"UpdateZeroValues", testUpdateZeroValues},
	{"BoolDefault", testBoolDefault},
	{"JoinOnSecurity", testJoinOnSecurity},
	{"DirtyTracking", testDirtyTracking},
	{"OptimisticLocking", testOptimisticLocking},
	{"SoftDeleteScopes", testSoftDeleteScopes},
	{"Nullable", testNullable},
	{"JSONField", testJSONField},
	{"Array", testArray},
	{"TZ", testTZ},
	{"BackfillOrchestration", testBackfillOrchestration},
	{"INChunking", testINChunking},
	{"HavingAggregate", testHavingAggregate},
	{"NestedPreload", testNestedPreload},
	{"ExprAST", testExprAST},
	{"Subquery", testSubquery},
	{"Window", testWindow},
	{"MigrationIsReversible", testMigrationIsReversible},
	{"DownRefusesWhatItCannotRebuild", testDownRefusesWhatItCannotRebuild},
	{"TypeWidths", testTypeWidths},
	{"AutoPKWidth", testAutoPKWidth},
	{"JoinBuilder", testJoinBuilder},
	{"BB2JoinProjection", testBB2JoinProjection},
	{"QualifiedColumns", testQualifiedColumns},
	{"Events", testEvents},
	{"Middleware", testMiddleware},
	{"Sync", testSync},
	{"RecursiveAssociations", testRecursiveAssociations},
	{"Stress", testStress},
	{"CompositePK", testCompositePK},
	{"BatchOps", testBatchOps},
	{"BatchHooks", testBatchHooks},
	{"UniqueViolationClassifiable", testUniqueViolationClassifiable},
}

// Run runs the engine suite against client, one subtest per check.
func Run(t *testing.T, client *quark.Client) {
	t.Helper()
	ctx := context.Background()
	for _, s := range subtests {
		t.Run(s.name, func(t *testing.T) { s.run(ctx, t, client) })
	}
}

// dropTable drops a table the suite owns, ignoring the error of one that
// does not exist: the plain DROP TABLE every engine accepts, where the
// shared suite used to spell IF EXISTS per engine name.
func dropTable(client *quark.Client, table string) {
	_, _ = client.Raw().Exec("DROP TABLE " + client.Dialect().Quote(table))
}

// q quotes an identifier in the client's dialect, for the assertions that
// look for a column or table name in the SQL Quark rendered.
func q(client *quark.Client, ident string) string {
	return client.Dialect().Quote(ident)
}
