// Copyright 2026 jcsvwinston
// SPDX-License-Identifier: Apache-2.0

package enterprisebench

// The "operacion" family: running a Quark application, not writing one.
// Replicas, locking, savepoints, audit, soft delete, cache, observability,
// events, pagination, schema sync, seeding and the test kit.
//
// Each title says exactly what its probe measured, which for half of these
// controls is narrower than the capability's name. A title that promises the
// capability while the probe measures a corner of it is the failure mode this
// bench exists to avoid: it passes forever and reports ground that was never
// walked.
//
// The second rule is about the verdict rather than the title: no probe here
// reaches its RECORDED verdict by two roads. Every fact the title and the note
// assert is measured separately and the verdict is their conjunction, so a
// capability that gains ground — or loses it — cannot land on the value the
// bench already publishes. A probe that answered `partial` both when a control
// is half-built and when it is broken would keep this family green through a
// regression, and a family that stays green through a regression is not
// measuring anything.

func controlsOperacion() []control {
	return []control{
		{
			id:     "OPS-01",
			family: "operacion",
			title:  "Read replicas: reads served by the replica pool, spread round-robin, and pinned to the primary by Sticky",
			want:   present,
			probe:  probeReplicaRouting,
		},
		{
			id:     "OPS-02",
			family: "operacion",
			title:  "Replica failure: an error that is not a transient connection failure is not retried on the primary and does not leave rotation; Raw() hands out the primary pool",
			want:   partial,
			note:   "Measured: a replica that answers with a non-connection error is not retried on the primary, and the second read fails the same way — nothing took it out of rotation. Sticky is the only escape. Raw() hands out the primary pool, so replica health, replica pool stats and replication lag have no accessor. What is NOT measured here, and is therefore not in the title: that a TRANSIENT connection error does fall back, and whether a cooldown exists. Both need a replica that can be dropped mid-flight — internal/enginesuite and the acceptance harness, not SQLite in-process.",
			probe:  probeReplicaFailureHandling,
		},
		{
			id:     "OPS-03",
			family: "operacion",
			title:  "Pessimistic locking: per-dialect FOR UPDATE / FOR SHARE / SKIP LOCKED / NOWAIT clauses, with a refusal where the engine has none",
			want:   present,
			probe:  probePessimisticLocking,
		},
		{
			id:     "OPS-04",
			family: "operacion",
			title:  "Optimistic locking guards Update and Tracked.Save; Delete carries no version predicate",
			want:   partial,
			note:   "Measured: an Update with a stale entity returns ErrStaleEntity, and so does a Tracked.Save whose row moved underneath the handle. A Delete with the SAME stale entity removes the winner's row and returns nil — the emitted DELETE matches on the primary key alone. Of the batch paths, UpdateMap and DeleteBatch emit no version predicate at all; UpdateBatch does emit one and then reports nothing when it matches no row, so a stale entity in a batch is dropped on the floor and the call returns nil. Nothing in the API or the documentation warns about the asymmetry.",
			probe:  probeOptimisticLocking,
		},
		{
			id:     "OPS-05",
			family: "operacion",
			title:  "Savepoints and nested transactions, with the post-commit callbacks registered after a savepoint unwound with it",
			want:   present,
			probe:  probeSavepoints,
		},
		{
			id:     "OPS-06",
			family: "operacion",
			title:  "Audit log written inside the write's own transaction, for single-row CRUD only",
			want:   partial,
			note:   "Measured: Create/Update/Delete write quark_audit inside the transaction (a rolled-back write leaves no row), CreateBatch writes two rows and no audit line, and the table EnableAuditLog creates has zero indexes — the fastest-growing table in the schema, with no index to read it by. The audit surface an application holds is exactly EnableAuditLog and DisableAuditLog, and AuditConfig carries no retention knob, so nothing on the API shrinks the table either.",
			probe:  probeAuditLog,
		},
		{
			id:     "OPS-07",
			family: "operacion",
			title:  "Soft delete with scopes and Restore, on a column fixed to deleted_at",
			want:   partial,
			note:   "Measured: Delete emits UPDATE … SET deleted_at, and the default/OnlyTrashed/Unscoped scopes and Restore all agree. On a model whose deletion timestamp is called removed_at the same call emits a physical DELETE and the row is gone; declaring that column with quark:\"softdelete\" does not name it either — the tag vocabulary is closed and the model is refused with ErrInvalidTag before it migrates. The table a soft delete writes into gains no author column, so there is no record of who deleted. DeleteBy is a hard delete even on a model that has deleted_at.",
			probe:  probeSoftDelete,
		},
		{
			id:     "OPS-08",
			family: "operacion",
			title:  "Query cache with table and row-level tag invalidation, except for composite primary keys",
			want:   partial,
			note:   "Measured against a store that drops only the entries carrying the tags it is given: a repeated cached query sends no second statement, and a write to a single-key model invalidates exactly [ops_account, ops_account:2] — the table tag and the written row's tag. The same write on a composite-key model carries exactly [ops_composite], so one row's change drops every cached query on that table. A cache hit emits no QueryEvent either, so an application cannot count hits and misses from the observer surface — there is no cache instrument anywhere.",
			probe:  probeQueryCache,
		},
		{
			id:     "OPS-09",
			family: "operacion",
			title:  "Cache stampede control: in-process singleflight, and the cross-instance hand-off through a CacheLocker store",
			want:   partial,
			note:   "Measured in one process: eight concurrent readers of a cold key produce one query; with WithCacheCrossInstance the winner takes and releases the per-key lock, and a client denied the lock serves the value the peer published without querying. What this bench cannot reach is the deployment the feature is for — two processes against a real Redis. Only the lock primitive is exercised against a live Redis today (cache/redis), never the wrapper that uses it.",
			probe:  probeCacheStampede,
		},
		{
			id:     "OPS-10",
			family: "operacion",
			title:  "OpenTelemetry spans with bind-value redaction by default; spans name no table and the instruments cover queries only",
			want:   partial,
			note:   "Measured against the OTel SDK in memory: every operation opens a span carrying the parameterised SQL, the bound value never appears by default, and WithSpanRedaction(IncludeArgs) does expose it. No span carries db.table, and the collected instrument set has nothing for connection pools, cache or replica routing — the three signals an operator reaches for first.",
			probe:  probeOpenTelemetry,
		},
		{
			id:     "OPS-11",
			family: "operacion",
			title:  "Slow query log: one WARN over the threshold with the parameterised SQL and without the bind values",
			want:   present,
			probe:  probeSlowQueryLog,
		},
		{
			id:     "OPS-12",
			family: "operacion",
			title:  "Strict reads: unbounded Iter/Cursor warned or rejected, a per-query escape, and one N+1 warning per tracked context and table",
			want:   present,
			probe:  probeStrictReads,
		},
		{
			id:     "OPS-13",
			family: "operacion",
			title:  "CRUD event bus for single-row writes; dirty-tracking saves and batch writes emit nothing",
			want:   partial,
			note:   "Measured: Create/Update/Delete publish created/updated/deleted on ops_account, one event each; a Tracked.Save that writes a row and its audit line publishes none, and CreateBatch publishes none. The batch boundary is documented (ADR-0013); the Tracked.Save one is not documented anywhere, so a subscriber silently misses every update made through the tracking API. Delivery has no outbox: with a bus whose Publish fails, the row is committed and stays, and the error reaches the caller after the fact — the write stands and the event is lost.",
			probe:  probeEventBus,
		},
		{
			id:     "OPS-14",
			family: "operacion",
			title:  "Dirty tracking: an unchanged entity sends no statement, and the UPDATE writes only the changed columns",
			want:   present,
			probe:  probeDirtyTracking,
		},
		{
			id:     "OPS-15",
			family: "operacion",
			title:  "Keyset pagination: no entrypoint emits a continuation predicate or returns a resumable page token",
			want:   absent,
			note:   "Measured on both entrypoints the API offers: Paginate spends two statements per page (a COUNT and a SELECT) and moves the window with OFFSET, so page N makes the server walk the pages before it; Cursor streams one plain SELECT with no WHERE and nothing to resume from. Neither emits a comparison against the last row read. An application can hand-roll the predicate with Where/OrderBy, but the seek, the tuple comparison and the token are its own problem. (The one place in the repository that says 'keyset' is a mislabelled benchmark case whose body is just .Cursor().)",
			probe:  probePagination,
		},
		{
			id:     "OPS-16",
			family: "operacion",
			title:  "Hot schema sync adds and renames columns; its transaction does not cover the tables it creates",
			want:   partial,
			note:   "Measured: Sync adds a new column and renames one through the rename tag, carrying the old column's value into the new name. Then a sync whose column drop the engine refuses fails — and the table the same call had just created is still there afterwards, because the CREATE goes through the client while only the ALTERs go through the transaction. Whether Sync takes a migration lock cannot be answered here: AcquireMigrationLock is a separate opt-in call and SQLite's dialect refuses it with ErrUnsupportedFeature, so two replicas starting together is a question for internal/enginesuite.",
			probe:  probeSchemaSync,
		},
		{
			id:     "OPS-17",
			family: "operacion",
			title:  "Seeding is an ordered registry: no ledger, no idempotency, no transaction",
			want:   partial,
			note:   "Measured: Register/Names/Get/Count keep and return registration order, which is the documented execution order. Running the same seeder twice against the same database lands both times, and the database holds no record that it ever ran — unlike migrations, which have state. And a seeder that writes a row and then fails leaves the row behind: the package hands back a Func and nothing wraps it, so there is no per-seeder transaction and no dependency order beyond registration. The CLI runner lives in cmd/quark, another module, and is out of this bench's reach.",
			probe:  probeSeeding,
		},
		{
			id:     "OPS-18",
			family: "operacion",
			title:  "Application test kit: SQLite client, one-line schema and rollback-per-test; commit-time behaviour is out of reach",
			want:   partial,
			note:   "Measured: quarktest.SQLite opens a sqlite client, Migrate creates the schema, and a write inside quarktest.Tx is visible to the test and gone afterwards. A post-commit callback registered inside that transaction never runs, so anything that only happens at commit cannot be tested through the kit. And with QUARK_TEST_POSTGRES_DSN set in the environment — the variable the engine suites read — the kit still hands back a sqlite client and does not skip: there is no path from the kit to PostgreSQL/MySQL/MSSQL/Oracle.",
			probe:  probeTestKit,
		},
	}
}
