# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [1.2.0](https://github.com/jcsvwinston/quark/compare/v1.1.5...v1.2.0) (2026-07-11)


### Added

* **cache:** cross-instance cache-stampede coordination (ADR-0020) ([03aa61d](https://github.com/jcsvwinston/quark/commit/03aa61df475fcba4da517d1b6279205373a54df7))
* **cache:** cross-instance stampede coordination (ADR-0020) ([bc2a174](https://github.com/jcsvwinston/quark/commit/bc2a174f9519235ac6092cca828655e4ee163415))
* **cache:** review follow-ups — in-memory lock GC, comment accuracy, ctx-cancel test ([6973073](https://github.com/jcsvwinston/quark/commit/697307361472a5aed69a23c5dbe7c731ae64131e))
* **sharding:** scatter-gather cross-shard reads (ADR-0022) ([21b1b35](https://github.com/jcsvwinston/quark/commit/21b1b35a5eb5bf90d7363658e35cc712579f4950))
* **sharding:** scatter-gather cross-shard reads (ADR-0022) ([b009b2e](https://github.com/jcsvwinston/quark/commit/b009b2e48a817ecfbb6f8835cc8c2a9ae4913f67))
* **sharding:** shard key from entity via ShardKeyer (ADR-0021) ([563ae32](https://github.com/jcsvwinston/quark/commit/563ae32b74922feeb6966ff20a5c63d087c715f5))
* **sharding:** shard key from entity via ShardKeyer (ADR-0021) ([69b92c7](https://github.com/jcsvwinston/quark/commit/69b92c732102cc64da01a9ca612fcbbf5051cef3))


### Fixed

* **query:** warn only on skipped scalar zero-values in Update, not nil pointers ([4f261bc](https://github.com/jcsvwinston/quark/commit/4f261bc6de41ddd591551fc582de397451610d47))
* **security:** pin toolchain go1.26.5 + pgx v5.9.2 — upstream security fixes accumulated against the v1.1.5 pins. The commit title's "11 advisories" was a point-in-time module-level `govulncheck` count (11→0) recorded in the umbrella repo's session log, not anything tracked in this repo — the count is omitted here because it goes stale as the vulnerability database moves; SECURITY.md now names govulncheck-in-CI as the standing source of truth. ([#235](https://github.com/jcsvwinston/quark/issues/235)) ([85cd55e](https://github.com/jcsvwinston/quark/commit/85cd55e98fc6ad630c74ceefb6a966ce18e75b6e))


### Performance

* **query:** memoize scan plan + pre-size result slice (AUD-2) ([ac99f87](https://github.com/jcsvwinston/quark/commit/ac99f872b2e1a6273d69f20b409c0e2532d58e33))
* **query:** memoize scan plan + pre-size result slice (AUD-2) ([f646a38](https://github.com/jcsvwinston/quark/commit/f646a3863849109bfc7b3c8e06dfcca31f9af55d))

## [1.1.5](https://github.com/jcsvwinston/quark/compare/v1.1.4...v1.1.5) (2026-06-18)


### Fixed

* **guard:** wrap ErrInvalidQuery for rejected operators + raw queries (DS-8) ([deeb2a1](https://github.com/jcsvwinston/quark/commit/deeb2a193ebd1bd4081b0364eda87babc57a7e43))
* **guard:** wrap ErrInvalidQuery for rejected operators and raw queries ([3208f2c](https://github.com/jcsvwinston/quark/commit/3208f2cf926ce5344f1bd17e805a3b69c3613273))

## [1.1.4](https://github.com/jcsvwinston/quark/compare/v1.1.3...v1.1.4) (2026-06-17)


### Fixed

* **query:** back-fill generated PKs in CreateBatch on MySQL and SQL Server ([49c4abd](https://github.com/jcsvwinston/quark/commit/49c4abd82e501f3ed36952eb6cfcf4a6ec6df301))
* **query:** back-fill generated PKs in CreateBatch on MySQL and SQL Server ([753106f](https://github.com/jcsvwinston/quark/commit/753106fdf1d3cd54045c2b6b9a5cacf6c76fcfa9))
* **query:** omit RECURSIVE keyword for Oracle and SQL Server CTEs ([1f4044f](https://github.com/jcsvwinston/quark/commit/1f4044fd29c8be342dce64f1ae409b9972bfc7a7))
* **query:** omit the RECURSIVE keyword for Oracle and SQL Server CTEs ([8b04484](https://github.com/jcsvwinston/quark/commit/8b04484e2f48da39957c4bd83a83f22ca1ff921b))
* **query:** run BeforeCreate in Upsert and UpsertBatch ([b1ab7b2](https://github.com/jcsvwinston/quark/commit/b1ab7b205c8fff79b7d85f3d94ba804bca06c1f1))
* **query:** run BeforeCreate/BeforeUpdate per entity in CreateBatch/UpdateBatch ([c9a081c](https://github.com/jcsvwinston/quark/commit/c9a081cc821ae0bab9374a752449a24b62579f31))
* **query:** run BeforeCreate/BeforeUpdate per entity in CreateBatch/UpdateBatch ([02554a0](https://github.com/jcsvwinston/quark/commit/02554a02614707c46731480271e13e812ce3ecdf))
* **query:** set-op Limit ORDER BY (J) + Upsert/UpsertBatch BeforeCreate (I) ([fac4524](https://github.com/jcsvwinston/quark/commit/fac45240f679b79c3f12a8fc411e86858aacd7d4))
* **query:** use positional ORDER BY for set-op pagination on MSSQL/Oracle ([c66175e](https://github.com/jcsvwinston/quark/commit/c66175e79d4a90c67e309528b2fa6bd62d439170))


### Documentation

* **playbooks:** refresh query-builder can/can't-express list ([7c221c8](https://github.com/jcsvwinston/quark/commit/7c221c8c1b61833a57fc6453d0557d2841a7723b))


### Tests

* **superapp:** fix the 4 harness-side gate failures (S7-fix-harness) ([6031cd1](https://github.com/jcsvwinston/quark/commit/6031cd1134edafd50941e3fc7f1ea03ded8a722c))
* **superapp:** fix the 4 harness-side gate failures (S7-fix-harness) ([f74284a](https://github.com/jcsvwinston/quark/commit/f74284aaabcfbd62c716baf3f4fe602db0ebe7f3))

## [1.1.3](https://github.com/jcsvwinston/quark/compare/v1.1.2...v1.1.3) (2026-06-15)


### Fixed

* **cache:** invalidate L2 table tag on CreateBatch for RETURNING dialects ([b28a608](https://github.com/jcsvwinston/quark/commit/b28a608dba821dc4addb2bf459d5d70549c5a285))
* **cache:** invalidate L2 table tag on CreateBatch for RETURNING dialects ([0d64b49](https://github.com/jcsvwinston/quark/commit/0d64b496ae9e1c6986be6cb7ba62cc184aed5cc8))
* **migrate:** stop MariaDB JSON columns drifting to longtext-&gt;JSON ([b6d7089](https://github.com/jcsvwinston/quark/commit/b6d7089927764adca26b48025b8a9fcbb989dabc))
* **migrate:** stop MariaDB JSON columns drifting to longtext→JSON ([d845f71](https://github.com/jcsvwinston/quark/commit/d845f710ef1634cec55c96d00849a11f1cb4f08f))
* **query:** back-fill generated PK in Oracle CreateBatch ([572b972](https://github.com/jcsvwinston/quark/commit/572b972af6f1691c2882f09fa496061d1833866e))
* **query:** back-fill generated PK in Oracle CreateBatch ([824b338](https://github.com/jcsvwinston/quark/commit/824b338396ef6cbb309cb586e780616b2551beb2))


### Documentation

* close the post-release audit gaps (docs-auditor report) ([5a935b7](https://github.com/jcsvwinston/quark/commit/5a935b74d5adb9b7e5bcfbcd3e9e8a5c4a1e5dae))
* mirror the patch release notes into the served 1.1.0 snapshot ([83ef6c5](https://github.com/jcsvwinston/quark/commit/83ef6c5aa583e35345f9c9a9fbd02a8724eece00))
* mirror the patch release notes into the served 1.1.0 snapshot ([04d2775](https://github.com/jcsvwinston/quark/commit/04d27756505b06c9359ec2fc5633fe6f0d4d2b37))
* v1.1.2 post-release — version bumps + patch notes on the public site ([4225030](https://github.com/jcsvwinston/quark/commit/422503096329acb2df0db587d1d0e6a67b35b772))
* v1.1.2 post-release — version bumps + patch notes on the public site ([87d4e5e](https://github.com/jcsvwinston/quark/commit/87d4e5e89c062769d44f176dba43b142c9213cc1))


### Tests

* **superapp:** add builder-advanced exerciser (S5) — the remaining 35 Query methods ([5005ad8](https://github.com/jcsvwinston/quark/commit/5005ad8a5f51f7b377753c840d2e9e493f9d6884))
* **superapp:** add cross-engine parity oracle — closes S5 ([ba4a801](https://github.com/jcsvwinston/quark/commit/ba4a80125a2a73a202d3b601641bc4097e8a6431))
* **superapp:** add HA exercisers (S5) — replicas, sharding, deadlock retry ([8e0429a](https://github.com/jcsvwinston/quark/commit/8e0429a5ec98ebe04b37668c3a96508a254115e0))
* **superapp:** add main.go entrypoint (S6) — run, reconcile, render matrix, gate ([d8d4281](https://github.com/jcsvwinston/quark/commit/d8d4281366983d627571b6cf22998d707aa36c8d))
* **superapp:** add observability exerciser (S5) — in-memory OTel + redaction ([ee9f532](https://github.com/jcsvwinston/quark/commit/ee9f5321cdc61c2359572e963142980be0b6e00c))
* **superapp:** builder-advanced exerciser (S5) — the remaining 35 Query methods ([b8fdd66](https://github.com/jcsvwinston/quark/commit/b8fdd66631236043e2d1eb96e9e3024a945c8519))
* **superapp:** cross-engine parity oracle — closes S5 ([b143e97](https://github.com/jcsvwinston/quark/commit/b143e979fbf5169b90b09cc8c8dc4670d95f9221))
* **superapp:** HA exercisers (S5) — replicas, sharding, deadlock retry ([77bc140](https://github.com/jcsvwinston/quark/commit/77bc140e0c4316d1ad2f36affe7dd41a3e18732c))
* **superapp:** observability exerciser (S5) — in-memory OTel + redaction ([c4ef9e8](https://github.com/jcsvwinston/quark/commit/c4ef9e897616297f613a71e9cd883dcde1480dcd))

## [1.1.2](https://github.com/jcsvwinston/quark/compare/v1.1.1...v1.1.2) (2026-06-10)


### Fixed

* **migrate:** make PlanMigration round-trip clean on a freshly migrated database ([bdc78a4](https://github.com/jcsvwinston/quark/commit/bdc78a46d6436195dfaef51fa934d1ba6ca4f98b))
* **migrate:** make PlanMigration round-trip clean on a freshly migrated database (re-target [#186](https://github.com/jcsvwinston/quark/issues/186)) ([3d26be0](https://github.com/jcsvwinston/quark/commit/3d26be0ce95aa1647bf53f553b6cdf5fc4a19b3b))
* **migrate:** render the primary key in plan-created tables (F3-2-pk) ([0f60b2a](https://github.com/jcsvwinston/quark/commit/0f60b2a8b6d43a10cf565102c7154cbe1385a95c))
* **migrate:** render the primary key in plan-created tables (F3-2-pk) ([6f9346f](https://github.com/jcsvwinston/quark/commit/6f9346fa1704d7028c6b017f68278a2d07f93b7b))


### Documentation

* correct ROADMAP F3-1 — Oracle migration lock landed via ADR-0018 ([81efa95](https://github.com/jcsvwinston/quark/commit/81efa95f0e8b9a3a054b1e14b8df3335307f11c2))


### Tests

* **superapp:** add migrate exerciser (S5) — plan/apply, sync, backfill, lock, versioned cycle ([b1dd5f4](https://github.com/jcsvwinston/quark/commit/b1dd5f4716b3634c3bfb89bea01e3fee01cdb289))
* **superapp:** create the migrate exerciser ledger via ApplyPlan ([23ac6b1](https://github.com/jcsvwinston/quark/commit/23ac6b13df9d5f2e273fd5b529f541904f7cfe2c))
* **superapp:** harden migrate exerciser asserts to strict IsEmpty ([7885a7f](https://github.com/jcsvwinston/quark/commit/7885a7f5f4c89c11dbd11c05c5f8c026ed8bd62c))
* **superapp:** migrate exerciser (S5) — plan/apply, sync, backfill, lock, versioned cycle ([9783729](https://github.com/jcsvwinston/quark/commit/97837298f7c22ce4a68ca32f6e6263844a978949))

## [1.1.1](https://github.com/jcsvwinston/quark/compare/v1.1.0...v1.1.1) (2026-06-09)


### Added

* **superapp:** add cache exerciser (S5) — hit / invalidation / N+1 by query-count ([8707d67](https://github.com/jcsvwinston/quark/commit/8707d67baee93cd7599e85eac3cc32d5c6542b0e))
* **superapp:** add cache exerciser (S5) — hit / invalidation / N+1 by query-count ([ea9ada3](https://github.com/jcsvwinston/quark/commit/ea9ada3002168143b6f339a1224c8da0eb49eb22))
* **superapp:** add RLSNative tenant exerciser (S5) ([23ea358](https://github.com/jcsvwinston/quark/commit/23ea3585a0c7ab83f82b828e6be02a5f74a7c7f6))
* **superapp:** add RLSNative tenant exerciser (S5) ([90ac143](https://github.com/jcsvwinston/quark/commit/90ac14319435e65c372a9ed1ee4b17dc7dfc227a))
* **superapp:** add SchemaPerTenant + DatabasePerTenant exercisers (S5) ([6cca23c](https://github.com/jcsvwinston/quark/commit/6cca23c47abf881272b10a86f58e6df7a8cb2114))
* **superapp:** add SchemaPerTenant + DatabasePerTenant exercisers (S5) ([854bd64](https://github.com/jcsvwinston/quark/commit/854bd643a1908caec59f528ec9f107e4cbfaf9d3))
* **superapp:** add tenant exerciser (S5) — RowLevelSecurityClient isolation ([a454b97](https://github.com/jcsvwinston/quark/commit/a454b973bf5238eca98190e7019e3bba1f112199))
* **superapp:** add tenant exerciser (S5) — RowLevelSecurityClient isolation ([bacfae2](https://github.com/jcsvwinston/quark/commit/bacfae25b0a48589c57995d2da8656902aac433d))


### Fixed

* **bugbash:** index the soak JOIN column — resolves BB-14 ([#164](https://github.com/jcsvwinston/quark/issues/164)) ([a8d2a0a](https://github.com/jcsvwinston/quark/commit/a8d2a0a79eb633966e951217cb1b5f67767a3d57))
* **cache:** invalidate the table tag on Create across all insert paths ([574e82d](https://github.com/jcsvwinston/quark/commit/574e82d36f024214dab2b804189002be4639b79a))
* **cache:** invalidate the table tag on Create across all insert paths ([984475e](https://github.com/jcsvwinston/quark/commit/984475e86e3ac90c265df5f747c38caa5fddf7c8))
* **cli:** model generate creates --out and reports failures as non-zero exit ([002d996](https://github.com/jcsvwinston/quark/commit/002d996f5b1c9088a08a8134f0bc5f6769fd9d90))
* **migrator:** normalize boolean column defaults per dialect ([1e497de](https://github.com/jcsvwinston/quark/commit/1e497dee35ebff0a0bd43b6c74e88e21a620263f))
* **migrator:** normalize boolean column defaults per dialect ([03170a4](https://github.com/jcsvwinston/quark/commit/03170a4b89279b4038c61e85049ea9e1b84405b4))
* **security:** make ErrInvalidIdentifier reachable via errors.Is ([31cf4df](https://github.com/jcsvwinston/quark/commit/31cf4dfe2582e784663e71603620ad6016b7d22f))
* **security:** make ErrInvalidIdentifier reachable via errors.Is ([10a91a7](https://github.com/jcsvwinston/quark/commit/10a91a763f37b84d069722cf6a9c166c6fd0a3cc))


### Documentation

* **cache:** refresh playbook test names + note XFetch assumption in regression ([add1c71](https://github.com/jcsvwinston/quark/commit/add1c719bee7878c7aeb8988e8f1dc27b474f56d))
* close doc-sync tail (DS-5 + DS-1/DS-2/DS-4 propagation) ([0d3ae31](https://github.com/jcsvwinston/quark/commit/0d3ae3171e5bf500e84fbf637d11a7132d119816))
* close doc-sync tail (DS-5 + DS-1/DS-2/DS-4 propagation) ([4fbb1d2](https://github.com/jcsvwinston/quark/commit/4fbb1d20432f26fb7f776d14af6a9b605e8e3a1d))
* **contributing:** note version-neutral harness scopes ([7735fdf](https://github.com/jcsvwinston/quark/commit/7735fdfa04da2ffc3588685bbc0184f3e6e287ff))
* corregir desfases docs↔código detectados en auditoría pre-v1.2 ([0d2d6d5](https://github.com/jcsvwinston/quark/commit/0d2d6d54bdaa363f50a49e01618de662abac75f2))
* pasada de saneamiento post-v1.1.0 ([#159](https://github.com/jcsvwinston/quark/issues/159)) ([13e2915](https://github.com/jcsvwinston/quark/commit/13e2915993ce4deea35db1cf5bc949fb8294bfba))
* **tasks:** enrich BB-14 with root-cause hypothesis + repro plan ([#162](https://github.com/jcsvwinston/quark/issues/162)) ([32f960b](https://github.com/jcsvwinston/quark/commit/32f960ba873bff25bd262b3209fc0eaf3a12df60))
* **tasks:** record BB-15 (Create table-tag cache invalidation) + F7 correction ([8f37825](https://github.com/jcsvwinston/quark/commit/8f37825d300d281051666fe827c4eda67990b5d4))
* **tasks:** register superapp harness + Code handoff ([9545da1](https://github.com/jcsvwinston/quark/commit/9545da18086d27da062f84219d545e7fa039e06a))
* type convention is the effective version-neutral barrier, not exclude-paths ([c2bd3d9](https://github.com/jcsvwinston/quark/commit/c2bd3d906737009e6cdd090f3047572730cfb533))
* type convention is the effective version-neutral barrier, not exclude-paths ([2a4656c](https://github.com/jcsvwinston/quark/commit/2a4656c38a36b47ea84f2c865c7a72b019a25d93))
* v1.1.0 post-release — fix release-notes site URL + TASKS release marker ([#157](https://github.com/jcsvwinston/quark/issues/157)) ([c04dde4](https://github.com/jcsvwinston/quark/commit/c04dde43594e832303f144632ce4c2d97a8a1ed9))
* v1.1.0 release DoD — Docusaurus version snapshot + release notes ([#155](https://github.com/jcsvwinston/quark/issues/155)) ([4fb4547](https://github.com/jcsvwinston/quark/commit/4fb4547c14137abd9ccb084036aaf6ec54318974))
* **website:** add v1.1.0 to the public release-notes page ([#158](https://github.com/jcsvwinston/quark/issues/158)) ([44c9eab](https://github.com/jcsvwinston/quark/commit/44c9eab02b4c1f709d6422b193b76f210053579c))


### Tests

* **examples:** builder exerciser (superapp S5, part 2) ([9afff40](https://github.com/jcsvwinston/quark/commit/9afff40b1ab1fc2ed01fc0f8f9f415f8d840e4e0))
* **examples:** builder exerciser (superapp S5, part 2) ([74e08e7](https://github.com/jcsvwinston/quark/commit/74e08e7ddd487eda1ae4e558dfa9150f65296a40))
* **examples:** CLI (cmd/quark) coverage smoke for the harness (superapp S9) ([e7b6c4e](https://github.com/jcsvwinston/quark/commit/e7b6c4e1c8800983f915187e03caca4585e7a068))
* **examples:** engine matrix runner + leak check (superapp S4) ([f969de0](https://github.com/jcsvwinston/quark/commit/f969de0f42305ee11f777103136b75be10f8f6b9))
* **examples:** engine matrix runner + leak check (superapp S4) ([d75f236](https://github.com/jcsvwinston/quark/commit/d75f2367feae53f21700dc72d5d3eaa9026dd75c))
* **examples:** exercise harness + crud/tx exercisers (superapp S5, part 1) ([4cd1d2a](https://github.com/jcsvwinston/quark/commit/4cd1d2a64405c34143edfe4761a3d2ba856caab6))
* **examples:** exercise harness + crud/tx exercisers (superapp S5, part 1) ([739fdab](https://github.com/jcsvwinston/quark/commit/739fdabacc099f205166555fcf853d22807f9468))
* **examples:** full CLI exerciser, incl. database-first model gen (superapp S9) ([57dda22](https://github.com/jcsvwinston/quark/commit/57dda225c23d233a2df9ebe2996c73d3505e1e0d))
* **examples:** gen-apisurface — coverage denominator (superapp S3) ([fc110d4](https://github.com/jcsvwinston/quark/commit/fc110d4e99a80c2165fbb5ba3523b434d5334e5e))
* **examples:** gen-apisurface — generate the coverage denominator (superapp S3) ([a570c6b](https://github.com/jcsvwinston/quark/commit/a570c6b05cd5c8dcc52e02d884af88fe40353513))
* **examples:** high-volume workload + executive report for the superapp ([a0c5fba](https://github.com/jcsvwinston/quark/commit/a0c5fbaaea983dcf8c717bde58d577414234cde0))
* **examples:** recorder package for the acceptance harness (superapp S2) ([a9729f1](https://github.com/jcsvwinston/quark/commit/a9729f114d1f138f3b6a09be5a4b0c452be3cdc2))
* **examples:** relations exerciser + restore portable bool defaults (superapp S5, part 3) ([4e612a4](https://github.com/jcsvwinston/quark/commit/4e612a48f58c7f135e020717efee379a144f8754))
* **examples:** relations exerciser + restore portable bool defaults (superapp S5, part 3) ([5ede622](https://github.com/jcsvwinston/quark/commit/5ede622060037b976e4f5f47d86c0d34420bf8a1))
* **examples:** security exerciser — SQLGuard attack suite (superapp S5, part 4) ([008c3cc](https://github.com/jcsvwinston/quark/commit/008c3ccff742fe9e81492753ad35fd51a80ff543))
* **examples:** security exerciser — SQLGuard attack suite (superapp S5, part 4) ([46f9230](https://github.com/jcsvwinston/quark/commit/46f9230a46734dcc0b395550a06c9e45fe2fc915))


### Chore

* **release:** exclude harness paths from version calc + retarget v1.1.1 ([21278cb](https://github.com/jcsvwinston/quark/commit/21278cbbd62a6ef85011b7ba5f83ff704f1617f1))

## [1.1.0](https://github.com/jcsvwinston/quark/compare/v1.0.0...v1.1.0) (2026-06-06)


### Added

* **events:** inbound PostgreSQL LISTEN/NOTIFY listener (ADR-0019) ([#132](https://github.com/jcsvwinston/quark/issues/132)) ([1832aca](https://github.com/jcsvwinston/quark/commit/1832aca26b8a3b709ef8d6dad30a387d71c21dcd))


### Fixed

* **dialects:** emit LOCK IN SHARE MODE on MariaDB; auto-detect MariaDB (BB-3) ([#139](https://github.com/jcsvwinston/quark/issues/139)) ([9c134f3](https://github.com/jcsvwinston/quark/commit/9c134f39c93a1cdc776f0a7dae97f099cc17a024))
* **query-builder:** make ForUpdate().List() work on Oracle (BB-4) ([#138](https://github.com/jcsvwinston/quark/issues/138)) ([04c49b3](https://github.com/jcsvwinston/quark/commit/04c49b35f6bbfd6c82d17e40b473449f3add6d7b))
* **query-builder:** scope typed-join SELECT/soft-delete/ORDER BY to base table (BB-2) ([#137](https://github.com/jcsvwinston/quark/issues/137)) ([cae5216](https://github.com/jcsvwinston/quark/commit/cae52165532fa8c2ddc06efd9f4f7e7ffcff7057))


### Documentation

* align public docs to the published v1.0.0 ([#130](https://github.com/jcsvwinston/quark/issues/130)) ([70c9b25](https://github.com/jcsvwinston/quark/commit/70c9b25f93eaa6714dca244644e934fbdca8dc8e))
* **modeling:** warn against mapping uuid.UUID to UNIQUEIDENTIFIER on MSSQL (BB-1) ([#140](https://github.com/jcsvwinston/quark/issues/140)) ([1adb0ba](https://github.com/jcsvwinston/quark/commit/1adb0ba595c036c58bac84429054ed8d2dcc24db))


### Tests

* **bugbash:** add F10 sharding phase (no findings) ([#149](https://github.com/jcsvwinston/quark/issues/149)) ([7e03273](https://github.com/jcsvwinston/quark/commit/7e03273adbc3ce9742d2b79ce95cb6ef5265a66d))
* **bugbash:** add F11 replicas phase (no findings) ([#148](https://github.com/jcsvwinston/quark/issues/148)) ([a985709](https://github.com/jcsvwinston/quark/commit/a985709119046bf3b2b9001236b1f01230d2a7d6))
* **bugbash:** add F12 resilience & concurrency phase (no findings) ([#147](https://github.com/jcsvwinston/quark/issues/147)) ([bea688a](https://github.com/jcsvwinston/quark/commit/bea688ac9a3189c68a920fa085e7b256728f43ef))
* **bugbash:** add F13 security phase + fix(security): reject -- in ValidateRawQuery ([#141](https://github.com/jcsvwinston/quark/issues/141)) ([76b8cc9](https://github.com/jcsvwinston/quark/commit/76b8cc98481ff7bef7bf3b876588e1fc2e9cc740))
* **bugbash:** add F14 soak phase (no findings) — completes bug-bash F0-F14 ([#152](https://github.com/jcsvwinston/quark/issues/152)) ([3566062](https://github.com/jcsvwinston/quark/commit/3566062b6ad0b08116f2d6809ead4f1632ca2ffc))
* **bugbash:** add F3 relations phase + fix(preload): nullable-FK, Oracle m2m, MSSQL null []byte (BB-5/6/7) ([#142](https://github.com/jcsvwinston/quark/issues/142)) ([572b70a](https://github.com/jcsvwinston/quark/commit/572b70a463def88ec18af3e8c6225031d3d61d8c))
* **bugbash:** add F4 volume phase + fix(crud): chunk CreateBatch to bind-param ceiling (BB-10) ([#146](https://github.com/jcsvwinston/quark/issues/146)) ([79795e0](https://github.com/jcsvwinston/quark/commit/79795e0d9eb19711ec8bb0d1bd0f736df3e39636))
* **bugbash:** add F5 multi-tenancy phase + fix(multi-tenant): SchemaPerTenant write routing (BB-8) ([#143](https://github.com/jcsvwinston/quark/issues/143)) ([bc8c421](https://github.com/jcsvwinston/quark/commit/bc8c4215602717c6d89e0177724742d87ba9963f))
* **bugbash:** add F6 migrations phase + fix(migrate): MSSQL versioned DDL (BB-12) & MariaDB diff false-positive (BB-11) ([#151](https://github.com/jcsvwinston/quark/issues/151)) ([a34a681](https://github.com/jcsvwinston/quark/commit/a34a6818bb75946a2450d13ffcdd4ebd4e6d4b5e))
* **bugbash:** add F7 cache phase (singleflight, granular invalidation, Redis) ([#144](https://github.com/jcsvwinston/quark/issues/144)) ([322a0f3](https://github.com/jcsvwinston/quark/commit/322a0f3f845a60cf19e976c21a49debf623e9db4))
* **bugbash:** add F8 hooks/events/audit phase + fix(tx): dialect-aware savepoints (BB-9) ([#145](https://github.com/jcsvwinston/quark/issues/145)) ([561603f](https://github.com/jcsvwinston/quark/commit/561603f2102599f61f35f5de26cb2ed4fc8da5c4))
* **bugbash:** add F9 codegen phase (no findings) ([#150](https://github.com/jcsvwinston/quark/issues/150)) ([fa3a3cf](https://github.com/jcsvwinston/quark/commit/fa3a3cffeafb7aa9d5f318dc07496df0ef80c401))
* **bugbash:** add phase F2 (API surface) — finds BB-2/BB-3/BB-4 ([#136](https://github.com/jcsvwinston/quark/issues/136)) ([8409bc9](https://github.com/jcsvwinston/quark/commit/8409bc9ed839f3b5be932a75ac88b0885b8ccaf6))
* **bugbash:** add reporter + phase F1 (smoke per engine) ([#135](https://github.com/jcsvwinston/quark/issues/135)) ([fe5a1c3](https://github.com/jcsvwinston/quark/commit/fe5a1c37edd2d10ec300f2188f24a8992a979ecf))
* **bugbash:** bootstrap executable harness + phase F0 (install & boot) ([#134](https://github.com/jcsvwinston/quark/issues/134)) ([799073d](https://github.com/jcsvwinston/quark/commit/799073da5b63ccfe54cc6349a2a361f20bf8ca90))

## [Unreleased]

### Fixed

- **query:** `CreateBatch` now back-fills generated primary keys into every
  entity on Oracle. Oracle falls back to per-row inserts (the multi-row
  `VALUES` form conflicts with `GENERATED ALWAYS AS IDENTITY`), and that path
  issued a plain `INSERT` without `RETURNING … INTO`, so entities came back with
  `ID == 0` while the rows inserted fine — a silent divergence from the other
  RETURNING dialects (PostgreSQL/SQLite/MariaDB). The per-row loop now wraps the
  insert in a PL/SQL `RETURNING … INTO` bind, matching single `Create`. A
  cross-engine regression in `testBatchOps` (SharedSuite) asserts every PK is
  populated on RETURNING dialects. Surfaced by the superapp acceptance harness.

- **migrate:** `PlanMigration` no longer reports spurious `longtext → JSON`
  drift for JSON columns on MariaDB. MariaDB implements the JSON type as a
  LONGTEXT alias and `INFORMATION_SCHEMA` reports the column as `longtext`, so a
  model's desired `JSON` column diffed to an ALTER on every plan over a freshly
  migrated database. The MariaDB introspector now relabels a `longtext` column
  to `json` when it carries the `json_valid(col)` CHECK that MariaDB auto-adds
  to JSON columns — a genuine `longtext` column (no such CHECK) is left
  untouched. Covered by a cross-engine `PlanMigration`-after-`Migrate` assertion
  in `testJSONField` (SharedSuite). Surfaced by the superapp acceptance harness.

- **cache:** `CreateBatch` now invalidates the L2 table tag (and the inserted
  row tags) on the RETURNING dialects (PostgreSQL/SQLite/MariaDB). Those
  dialects scan generated primary keys through a query path that, unlike
  `executeExec`, invalidates nothing — so a cached list / filtered-query /
  aggregate read went stale after a batch insert, the batch sibling of the
  single-`Create` table-tag fix in v1.1.1. The fix drops the table tag plus the
  back-filled row tags in one `InvalidateTags` call per chunk (Oracle's per-row
  path batches the same way). Cross-engine regression in
  `testCacheInsertInvalidation` (SharedSuite). Surfaced while reviewing the
  Oracle `CreateBatch` fix.

### Security

- **guard:** `ValidateRawQuery` now rejects the SQL line-comment tail `--` in
  raw queries (`RawQuery`/`Exec` under `AllowRawQueries=true`), closing a
  classic injection-truncation vector and aligning the code with the documented
  behaviour (the security playbook already listed `--` as filtered; the regex
  did not implement it). Block comments (`/* */`) remain allowed by design —
  they are legitimate optimizer hints (`/*+ … */`); `ValidateRawQuery` is a
  best-effort heuristic backstop, not a complete filter, and the real boundary
  for raw queries stays `AllowRawQueries` (off by default) + placeholders for
  values. Surfaced while implementing bug-bash phase F13. Covered by
  `guard_test.go` (`TestValidateRawQuery_SuspiciousLineComment` /
  `_BlockCommentAllowed`).

### Documentation

- **modeling:** documented the SQL Server `UNIQUEIDENTIFIER` footgun for
  `uuid.UUID`. Mapping `uuid.UUID` to the native `UNIQUEIDENTIFIER` column
  silently corrupts values on round-trip — SQL Server stores the first three
  GUID groups little-endian while `github.com/google/uuid` (RFC 4122) is
  big-endian, so `go-mssqldb` returns them byte-swapped. Added a warning in
  the custom type-mappers guide steering `uuid.UUID` to `VARCHAR(36)` /
  `NVARCHAR(36)` on SQL Server (Quark's auto-migrator already does this), and
  pointing to the driver's `mssql.UniqueIdentifier` type for anyone who needs
  the native column. No code change — the working path was already the
  default. Found by the post-v1.0 bug-bash (BB-1, phase F1). See
  `website/docs/guides/modeling.mdx` § Custom type mappers.

### Added

- **dialects:** automatic MariaDB detection. MariaDB ships no dedicated
  `database/sql` driver — it uses `go-sql-driver/mysql` (driver name `"mysql"`),
  so Quark could not tell it apart from MySQL by driver name and applied the
  MySQL dialect. `New()` now probes `SELECT VERSION()` once on a `"mysql"`
  connection and switches to the MariaDB dialect when the server identifies as
  MariaDB. `quark.New("mysql", dsn)` therefore picks the correct dialect for
  both engines; an explicit `WithDialect(quark.MariaDB())` still works and skips
  the probe. This is what makes the BB-3 `ForShare` fix below take effect on a
  plain MariaDB connection.

### Fixed

- **migrate:** versioned migrations now work on SQL Server. `Migrator.Init`
  emitted `CREATE TABLE IF NOT EXISTS quark_migrations (… applied_at TIMESTAMP …)`
  unconditionally — but SQL Server has no `CREATE TABLE IF NOT EXISTS` (and its
  `TIMESTAMP` is a rowversion, not a datetime), so `Migrator.Up`/`Down` failed
  with "Incorrect syntax near 'quark_migrations'". The bookkeeping-table DDL is
  now per-dialect (SQL Server `IF NOT EXISTS (SELECT … sys.tables)` + `DATETIME`;
  Oracle `VARCHAR2` + ORA-00955-swallow; others keep `CREATE TABLE IF NOT
  EXISTS`), mirroring the backfill state table. Found by the post-v1.0 bug-bash
  (BB-12, phase F6); verified on SQLite + PostgreSQL + MySQL + MariaDB + SQL
  Server.
- **migrate:** `PlanMigration` no longer reports a spurious column diff on
  MariaDB. MariaDB's `DESCRIBE` returns the literal string `"NULL"` as the
  default of a nullable, no-default column (MySQL returns a real SQL NULL), so
  the schema differ saw `default "NULL" → <nil>` on every such column and
  emitted a false-positive `OpAlterColumn` — breaking "empty plan when nothing
  changed". The MySQL/MariaDB introspector now normalizes that literal `"NULL"`
  to "no default". Found by the post-v1.0 bug-bash (BB-11, phase F6); verified
  cross-engine.
- **crud:** `CreateBatch` now chunks large slices so each `INSERT … VALUES`
  statement stays within the dialect's bind-parameter ceiling. Previously it
  emitted one statement with `rows × columns` placeholders, which overran SQL
  Server's ~2100-parameter limit at a few hundred wide rows and
  SQLite/PostgreSQL/MySQL's limits at a few thousand — the call simply failed
  ("too many parameters" / "too many SQL variables"). `DeleteBatch` already
  chunked; `CreateBatch` did not, so the bug was latent on every engine but
  SQLite (where the unit suite runs) until a batch grew large. Chunks loop on
  the bound executor (an explicit transaction or native-RLS session still routes
  correctly) and, like `DeleteBatch`, are not wrapped in an implicit transaction
  — wrap `CreateBatch` in `client.Tx` for all-or-nothing across chunks. Oracle
  is unaffected (it already uses a single-row INSERT loop). Found by the
  post-v1.0 bug-bash (BB-10, phase F4); verified across SQLite + PostgreSQL +
  MySQL + MariaDB + SQL Server. See `website/docs/guides/batch-operations.mdx`.
  (`UpsertBatch` has the same un-chunked shape and is tracked separately.)
- **tx:** nested transactions (savepoints) now work on SQL Server and Oracle.
  `Tx.Savepoint` / `Tx.RollbackTo` / `Tx.ReleaseSavepoint` emitted the ANSI
  `SAVEPOINT` / `ROLLBACK TO SAVEPOINT` / `RELEASE SAVEPOINT` statements
  unconditionally — correct for PostgreSQL/MySQL/MariaDB/SQLite, but SQL Server
  needs `SAVE TRANSACTION` / `ROLLBACK TRANSACTION` and has no release
  statement, and Oracle has no `RELEASE SAVEPOINT`. A nested `tx.Tx(...)` (which
  brackets each level with a savepoint) therefore failed on those two engines.
  The statements are now resolved per dialect via the new optional
  `SavepointDialect` interface; dialects that do not implement it keep the ANSI
  statements (so custom dialects are unaffected — this is additive, not a
  breaking `Dialect` change). Found by the post-v1.0 bug-bash (BB-9, phase F8).
  Covered by `savepoint_dialect_test.go` and the F8 hooks phase on all six
  engines.
- **multi-tenant:** `SchemaPerTenant` writes now hit the tenant's schema. On
  `Create`/`Update` the persistence path built its INSERT/UPDATE from a
  `BaseQuery` that copied the tenant id and column but **not** the resolved
  schema, so writes emitted a bare table name and landed in the default
  `search_path` schema while reads (which honour the schema) looked in the
  tenant schema — rows effectively vanished, and every tenant's writes
  co-mingled in one schema. The save path now propagates the schema, so writes
  and reads agree. Found by the post-v1.0 bug-bash (BB-8, phase F5). Covered by
  `schema_per_tenant_write_test.go` (asserts the emitted INSERT is
  schema-qualified) and the F5 tenancy phase on PostgreSQL.
- **preload:** relations whose foreign key maps to a pointer field (a nullable
  FK such as `*int64`) now load correctly. The eager loader keyed its
  parent/child match map by the raw field value, so a `*int64` key never
  compared equal to the related row's non-pointer primary key — the relation
  loaded silently as `nil`/empty. This hit every nullable-FK relation,
  including the common self-referential tree (`Parent *T` / `Children []T` over
  a `ParentID *int64`) and optional belongs_to (e.g. an `OrderID *int64`). Keys
  are now normalised to their pointee before matching (a `NULL` FK matches no
  parent, as expected). Found by the post-v1.0 bug-bash (BB-5, phase F3).
  Covered by `preload_nullable_fk_test.go` and the F3 relations phase.
- **preload:** `many_to_many` relations now load on Oracle. Two defects in the
  m2m loader combined to make the join match find nothing and the relation load
  empty: (1) the related-row scan looked up `FieldByCol[col]` with the
  driver-reported column name verbatim, but Oracle upper-cases identifiers
  while `FieldByCol` is keyed by the lower-case db tag — so no column mapped and
  the related rows scanned all-zero (the other loaders already lower-cased; m2m
  did not); and (2) the join-table FK columns were scanned into `interface{}`,
  so go-ora returned its `NUMBER` values as `string`, which did not compare
  equal to the `int64` parent keys. The scan now lower-cases the column name and
  reads the join FKs into destinations typed to the owner/related PK fields, so
  keys match on every driver. Found by the post-v1.0 bug-bash (BB-7, phase F3);
  the reflect path already worked on the other five engines. Covered by the F3
  relations phase on Oracle. The same lower-casing was applied to the
  polymorphic loader's parent-key lookup for consistency (it was the last
  loader still comparing the PK column name case-sensitively).
- **types:** a SQL-NULL `Nullable[[]byte]` now inserts on SQL Server. An
  invalid `sql.Null[[]byte]` hands the driver an untyped `nil`, which
  go-mssqldb encodes as `nvarchar` — and SQL Server rejects the implicit
  `nvarchar`→`varbinary(max)` conversion, failing the INSERT of any row with a
  NULL binary column. Quark now binds a typed `[]byte(nil)` (a binary NULL on
  every driver), so the insert succeeds. Found by the post-v1.0 bug-bash
  (BB-6, phase F3). Covered by `nullable_bytes_test.go` and the F3 relations
  phase.
- **dialects:** `ForShare()` on MariaDB no longer emits invalid SQL. MariaDB has
  no `FOR SHARE` keyword (MySQL-8 syntax — it raises `Error 1064`); Quark now
  emits the older `LOCK IN SHARE MODE` for shared locks on MariaDB. Because that
  form takes no modifiers, combining `ForShare()` with `SkipLocked()`/`NoWait()`
  returns `ErrUnsupportedFeature` on MariaDB (use `ForUpdate()` for those).
  `ForUpdate()` on MariaDB is unchanged. MySQL keeps emitting `FOR SHARE`. Found
  by the post-v1.0 bug-bash (BB-3, phase F2); regression covered by
  `TestLockSuffix_PerDialect` and `testPessimisticLocking` in the SharedSuite.
  See `website/docs/guides/querying.mdx` § Pessimistic Locking.

- **query-builder:** pessimistic locking on Oracle no longer hard-fails through
  `List()`. Oracle rejects `FOR UPDATE`/`SKIP LOCKED`/`NOWAIT` combined with a
  row-limiting clause (`OFFSET … FETCH`) — ORA-02014 — and `List()` applies an
  implicit `Limit(100)`, so `ForUpdate().List()` was unusable on Oracle. The
  implicit cap is now suppressed under a lock on Oracle (the lock spans all
  matching rows; a `WARN` is logged), so `ForUpdate().List()` works. An
  *explicit* `Limit`/`Offset` alongside a lock returns `ErrUnsupportedFeature`
  on Oracle (no valid single-statement form) instead of silently widening the
  lock or emitting invalid SQL. The other five engines are unaffected
  (PostgreSQL/MySQL/MariaDB allow `LIMIT` + `FOR UPDATE`; SQL Server uses table
  hints). Found by the post-v1.0 bug-bash (BB-4, phase F2); regression covered
  by `testPessimisticLocking` in the SharedSuite. See
  `website/docs/guides/querying.mdx` § Oracle: locking and row limits don't mix.
- **query-builder:** typed queries with a `Join`/`LeftJoin`/`RightJoin` and no
  explicit `Select` now project only the base table (`SELECT "orders".*`)
  instead of a bare `SELECT *`. A bare `*` over a JOIN pulled every joined
  table's columns into the result set, so shared column names (`id`,
  `deleted_at`, …) collided — a hard *ambiguous column* error on the strict
  engines (PostgreSQL/MSSQL/Oracle) or a silent mis-bind of a joined table's
  column into the model on the lax ones (e.g. a NULL `order_lines.id` from an
  outer join scanned into `Order.ID` → `converting NULL to int64`). The
  auto-injected soft-delete predicate is likewise qualified with the base
  table under a join (`"orders"."deleted_at" IS NULL`) so it stays unambiguous
  when the joined table also exposes `deleted_at`. `Join().List()` is now a
  supported path on all six engines (previously only `Count()` worked over a
  join). Found by the post-v1.0 bug-bash (BB-2, phase F2); regression covered
  by `testBB2JoinProjection` in the SharedSuite. See
  `website/docs/guides/querying.mdx` § Projection under a join.

### Added

- **events:** inbound PostgreSQL `LISTEN/NOTIFY` listener. `ListenerFactory.CreateListener`
  now returns a working `EventListener` on PostgreSQL (`Listen`/`Unlisten`/`Receive`/`Close`)
  that pins a dedicated connection from the pool via pgx; non-PostgreSQL dialects still return
  `ErrDialectNotSupported`. Closes the v1.0 known-limitation that inbound `LISTEN/NOTIFY` was
  deferred post-v1.0. New sentinels `ErrListenerClosed` (operation after `Close`) and
  `ErrNoSubscription` (`Receive` before any `Listen`). Single-goroutine, fire-and-forget
  delivery semantics (no durable replay). See [ADR-0019](docs/adr/0019-inbound-listen-notify-dedicated-conn.md)
  and `website/docs/advanced/events.mdx`.

### Tests

- **bug-bash:** bootstrapped the executable bug-bash harness (`bugbash/`, a
  separate module via local `replace`). Adds the ERP-SaaS domain (20 models
  exercising decimal/uuid/JSON[T]/Array[T]/per-column TZ/composite PK), the
  per-engine container plumbing (`bugbash/tools`), and phase **F0
  (install & boot)** which migrates the whole domain per engine. Verified on
  SQLite and PostgreSQL; gated by the `bugbash` build tag so it stays out of
  the library's `go test ./...`. See `bugbash/README.md` and
  [`docs/BUGBASH_PLAN.md`](docs/BUGBASH_PLAN.md).
- **bug-bash:** added the `reporter` package (structured `Fail`/`Failure`
  sink → `failures.jsonl`) and phase **F1 (smoke per engine)** — round-trips
  every rich column type (decimal, uuid, JSON[T], Array[T], per-column TZ,
  Duration, `[]byte`, Nullable set/NULL) and exercises the CRUD primitives
  (`Create`/`Find`/`Count`/`Update`/`UpdateFields`/`List`/`Delete`/`HardDelete`)
  against the real domain. Green on SQLite and PostgreSQL (no findings).
- **bug-bash:** added phase **F2 (API surface)** — exercises the query
  builder across all six engines: predicates + Expr AST + subqueries,
  aggregates, group-by/having, ordering/pagination, streaming, joins, set
  ops, locking, soft delete, batches, optimistic locking, preload, and
  window/CTE SQL generation. Ran on all six engines; surfaced three findings
  filed under `TASKS.md` § "Bug-bash hallazgos": **BB-2** (typed `Join` does
  `SELECT *` without scoping to the base table), **BB-3** (MariaDB rejects
  `FOR SHARE`), **BB-4** (Oracle `ForUpdate` + implicit `List()` limit →
  ORA-02014). No library code changed.

## [1.0.0](https://github.com/jcsvwinston/quark/compare/v0.13.0...v1.0.0) (2026-05-27)


### release

* v1.0.0 DoD — release notes + Docusaurus 1.0.0 snapshot ([#129](https://github.com/jcsvwinston/quark/issues/129)) ([4a99f41](https://github.com/jcsvwinston/quark/commit/4a99f412b628810780dbeab1b0ca388253dfee75))


### Added

* **oracle:** distributed migration lock via DBMS_LOCK — v1.0 gate §A Item 1 PR (c) ([#126](https://github.com/jcsvwinston/quark/issues/126)) ([9231d1d](https://github.com/jcsvwinston/quark/commit/9231d1df44ede14cc69abad4a9ff6281a3676f78))
* **oracle:** schema introspection (F3-2) — v1.0 gate §A Item 1 PR (b) ([#125](https://github.com/jcsvwinston/quark/issues/125)) ([424a012](https://github.com/jcsvwinston/quark/commit/424a012f24ef9ad09f461330b242b1512f35e557))
* **replicas:** replica strategies + single-row read routing (F6-5 follow-up) ([#118](https://github.com/jcsvwinston/quark/issues/118)) ([ed5ad96](https://github.com/jcsvwinston/quark/commit/ed5ad96b5dd915989bb345381cefc3294b1a7b0d))
* **sharding:** pluggable ShardRouter — route per query by shard key (F6-7) ([#115](https://github.com/jcsvwinston/quark/issues/115)) ([039f7ef](https://github.com/jcsvwinston/quark/commit/039f7ef951f5189c00994470709f3529f587c72d))


### Fixed

* **oracle:** JSON path literal (ORA-40454) + NULL-&gt;empty-string scan — v1.0 gate §A Item 1 PR (a) ([#123](https://github.com/jcsvwinston/quark/issues/123)) ([6180446](https://github.com/jcsvwinston/quark/commit/61804466a7f37406f728f03e9c1e421b5b5441b5))


### Documentation

* **adr:** ADR-0017 — retire ADR-0002 ≥3× p99 codegen gate, reframe codegen as type-safety ([6b16c3c](https://github.com/jcsvwinston/quark/commit/6b16c3c0f082a045266522d34f0c755b2761ff31))
* doc-sync pass — align public docs to v0.13.0 ([#119](https://github.com/jcsvwinston/quark/issues/119)) ([c1e97d8](https://github.com/jcsvwinston/quark/commit/c1e97d815382f313a772326dfa7a74a0288aa58c))
* **sharding:** runnable example + close v1.0-gate §A Item 2 ([#121](https://github.com/jcsvwinston/quark/issues/121)) ([ad8e284](https://github.com/jcsvwinston/quark/commit/ad8e28491e70d4934cdbc9dcc891a3bb2f4b665a))
* **v1-gate:** record Oracle Salida A decision + local 187/24 diagnosis ([#122](https://github.com/jcsvwinston/quark/issues/122)) ([dc82b04](https://github.com/jcsvwinston/quark/commit/dc82b04b399f02c489ffc434bc862341cf8798c2))
* v1.0 gate checklist (V1_GATE.md) + close §A Items 3 & 4 ([#120](https://github.com/jcsvwinston/quark/issues/120)) ([4049805](https://github.com/jcsvwinston/quark/commit/404980557fbf61fff579a8c3bd9d8fe572dec177))


### Tests

* **benchmarks:** add ent + sqlc codegen-tier comparison — F6-8b ([#128](https://github.com/jcsvwinston/quark/issues/128)) ([2347a15](https://github.com/jcsvwinston/quark/commit/2347a15e7634933140c096a315e21f34a63894ad))
* **oracle:** make DirtyTracking/CTE asserts case-insensitive ([#29](https://github.com/jcsvwinston/quark/issues/29)) ([#124](https://github.com/jcsvwinston/quark/issues/124)) ([eff5a72](https://github.com/jcsvwinston/quark/commit/eff5a72e447c3c25f48fa16ee6227358b667a06a))

## [0.13.0](https://github.com/jcsvwinston/quark/compare/v0.12.0...v0.13.0) (2026-05-24)


### Added

* **replicas:** replica failover + health cooldown (F6-6) ([#113](https://github.com/jcsvwinston/quark/issues/113)) ([73bb580](https://github.com/jcsvwinston/quark/commit/73bb580813b7e0204bebf38a5857014ed871dad8))
* **replicas:** WithReplicas + Sticky read-replica routing (F6-5) ([#110](https://github.com/jcsvwinston/quark/issues/110)) ([33e5e9e](https://github.com/jcsvwinston/quark/commit/33e5e9e3eda42570f93525febdab8d9ad6924069))


### Performance

* **query:** copy-on-write builder clone via capacity-bounded append ([#107](https://github.com/jcsvwinston/quark/issues/107)) ([65b68d8](https://github.com/jcsvwinston/quark/commit/65b68d84fde0f229e3334a616e0f3550c42b0969))


### Documentation

* full sync pass (docs-auditor first run) ([#112](https://github.com/jcsvwinston/quark/issues/112)) ([585cf16](https://github.com/jcsvwinston/quark/commit/585cf16dc3489448ba1c40bce7abde481ca9c250))


### Tests

* **benchmarks:** F6-9 stress/load harness + documented run ([#109](https://github.com/jcsvwinston/quark/issues/109)) ([9940b5c](https://github.com/jcsvwinston/quark/commit/9940b5c888d89c70e1b47a202f85e0ca58999277))

## [0.12.0](https://github.com/jcsvwinston/quark/compare/v0.11.0...v0.12.0) (2026-05-24)


### Added

* **codegen:** typed compile-time column accessors (F6-4) ([#105](https://github.com/jcsvwinston/quark/issues/105)) ([34ea945](https://github.com/jcsvwinston/quark/commit/34ea945e70a0be5f417bf247e08e73fca2f2bd40))


### Performance

* **crud:** compute audit row diff only when a sink is configured ([02ec854](https://github.com/jcsvwinston/quark/commit/02ec85439b108220b58c2f3a64de569b4d66f3e5))


### Documentation

* **release:** v0.11.0 DoD backfill — docs versioning + release notes ([#103](https://github.com/jcsvwinston/quark/issues/103)) ([d5dc9ce](https://github.com/jcsvwinston/quark/commit/d5dc9cec3f40c561453342bcc0a0c2a17335f89c))
* **release:** v0.12.0 DoD — docs versioning + release notes ([#106](https://github.com/jcsvwinston/quark/issues/106)) ([cab5828](https://github.com/jcsvwinston/quark/commit/cab5828ae6464acb38c56375d4e62cb9490f2973))
* **tasks:** mark F6-1/F6-2/F6-3a/F6-8a as merged in v0.11.0 ([844ad04](https://github.com/jcsvwinston/quark/commit/844ad04e5b7c40b27619d90f9fded616fa6c34fa))
* **tasks:** mark F6-4 merged ([#105](https://github.com/jcsvwinston/quark/issues/105)), release v0.12.0 pending ([e72b0c2](https://github.com/jcsvwinston/quark/commit/e72b0c2c38304061d3550a2ab66cabb8165c12fa))
* **tasks:** record rowToMap lazy perf lever as shipped ([4131d52](https://github.com/jcsvwinston/quark/commit/4131d52619a71917280cb367fede51431c2f8356))


### Tests

* **audit:** cover excluded-table gate in recordAudit no-alloc guard ([5c9d555](https://github.com/jcsvwinston/quark/commit/5c9d555252437496d08d08e0d3bb45f963405bb9))

## [0.11.0](https://github.com/jcsvwinston/quark/compare/v0.10.0...v0.11.0) (2026-05-24)


### Added

* **codegen:** generated INSERT binder on the write path (F6-3a) ([550c13f](https://github.com/jcsvwinston/quark/commit/550c13f875d529227d2f364d590d7f931a1b8319))
* **codegen:** generated typed scanners on the read path (F6-2) ([9fcc3db](https://github.com/jcsvwinston/quark/commit/9fcc3dbd681ec4ff9e98a361a64c1b9b9e7c1302))
* **codegen:** quark gen + typed-registry contract (F6-1) ([#99](https://github.com/jcsvwinston/quark/issues/99)) ([ce85abc](https://github.com/jcsvwinston/quark/commit/ce85abc94fc68f61d9661f80d724f6815e8a19f0))


### Documentation

* **benchmarks:** profile per-op cost + ADR-0002 gate analysis ([#102](https://github.com/jcsvwinston/quark/issues/102)) ([d5ba67a](https://github.com/jcsvwinston/quark/commit/d5ba67ac82d0c54a18a70776dcb4e75e7a18ab4c))
* **codegen:** amend ADR-0014 for AST gen + restore cmd/quark build ([#96](https://github.com/jcsvwinston/quark/issues/96)) ([c278d3d](https://github.com/jcsvwinston/quark/commit/c278d3dfb05718cb2c68cc9fca5a2e3a129d7887))

## [0.10.0](https://github.com/jcsvwinston/quark/compare/v0.9.0...v0.10.0) (2026-05-22)


### Added

* **tenant:** warn on raw SQL under RowLevelSecurityNative ([#91](https://github.com/jcsvwinston/quark/issues/91)) ([2ab4cb2](https://github.com/jcsvwinston/quark/commit/2ab4cb2a5d839729358ebe88cb398b544c9be300))


### Fixed

* **tx:** unwind queued hooks on savepoint rollback ([#88](https://github.com/jcsvwinston/quark/issues/88)) ([3889707](https://github.com/jcsvwinston/quark/commit/3889707d52d911cf42be0a89d00b28ed24dc0f30))
* **types:** round-trip JSON[T]/Array[T] on SQL Server ([#89](https://github.com/jcsvwinston/quark/issues/89)) ([bb99242](https://github.com/jcsvwinston/quark/commit/bb99242c3fb9456a59b241b05a9821de0e7bb57a))


### Tests

* **tx:** real cross-engine deadlock retry integration test ([#90](https://github.com/jcsvwinston/quark/issues/90)) ([81f0167](https://github.com/jcsvwinston/quark/commit/81f016786f9f9dddef277777d8b6885ea6b6e57a))

## [Unreleased]

<!-- release-please manages versioned sections below; entries for the
     next release are generated from Conventional Commits. v0.10.0
     entries live in the [0.10.0] section (PR #94) and in
     docs/RELEASE_NOTES_v0.10.0.md. -->

### Added

- **oracle:** schema introspection (`Client.IntrospectSchema`) now supports
  Oracle, reading the data dictionary (`USER_TABLES`, `USER_TAB_COLUMNS`,
  `USER_INDEXES`, `USER_CONSTRAINTS`) for tables, columns, non-PK indexes,
  foreign keys, and CHECK constraints. Completes F3-2 across all six dialects
  and unblocks `PlanMigration` / `ApplyPlan` on Oracle. (#30)
- **migrate:** new optional `ColumnTypeMapper` Dialect interface — translates a
  neutral column-type string to the dialect's native form before DDL. Oracle
  implements it to map the generic `TEXT` to `CLOB`. Dialects that don't
  implement it leave types untouched. (#30)
- **oracle:** distributed migration lock (`Client.AcquireMigrationLock`) now
  supports Oracle via `DBMS_LOCK` (session-scoped, survives DDL's implicit
  commits). Completes F3-1 across all engines except SQLite. Requires `GRANT
  EXECUTE ON DBMS_LOCK TO <user>` — see ADR-0018. (#31)

### Fixed

- **migrate:** `ApplyPlan` of an `OpAddColumn{Type: "TEXT"}` no longer fails
  with `ORA-00902` on Oracle — the type is mapped to `CLOB` via
  `ColumnTypeMapper`. The schema diff also treats an Oracle identity column's
  bare `NUMBER` and its sequence default as equivalent to the model's
  `NUMBER(19)`, so a migrated model round-trips clean. (#30)
- **oracle:** `WhereJSON` now inlines the JSON path as a literal on Oracle.
  Oracle's `JSON_VALUE` rejects a bound path (`ORA-40454: path expression not
  a literal`); the validated path (`internal/guard.ValidateJSONPath`,
  `[A-Za-z0-9_.]` grammar) is inlined instead, which stays injection-safe by
  the same rule that makes a validated identifier safe. Other dialects keep
  binding the path. (#28)
- **scan:** a `NULL` column scanned into a non-pointer `string` field now
  coerces to `""` instead of failing with `converting NULL to string is
  unsupported`. This is consistent across all six dialects and reconciles
  Oracle — which stores `''` as `NULL` — so empty strings round-trip the same
  everywhere. Use `*string` or `sql.Null[string]` to keep the `NULL` vs `""`
  distinction. (#27)

### Tests

- **benchmarks:** added the code-generation tier to the comparison harness
  (F6-8b) — `benchmarks/ent` (ent, schema-generated typed client) and
  `benchmarks/sqlc` (sqlc, SQL-generated `database/sql` wrappers), each its
  own test binary mirroring `benchmarks/gorm`. The run confirms the ADR-0017
  finding: sqlc sits on the raw `database/sql` floor (no runtime) while ent
  (codegen + a rich runtime) stays in the reflect class, so cross-library
  speed tracks runtime/allocation design, not reflect-vs-codegen. Published
  numbers and methodology in `website/docs/reference/benchmarks.mdx`. This is
  informational, not a v1.0 gate (the ≥3× p99 gate it once fed was retired by
  ADR-0017).

## [0.9.0] - 2026-05-21

Phase 5 release — engine-enforced multi-tenancy, transactional hooks,
events, and audit. Closes F5-1 through F5-7: PostgreSQL native RLS
(`RowLevelSecurityNative` via `set_config` + `CREATE POLICY`) with the
`quarktenant` policy-installer CLI; transactional `After*` hooks that
fire post-commit plus new `BeforeFind`/`AfterFind`; public
`Tx.OnCommit`/`Tx.OnRollback` + `quark.TxFromContext`; a real
`EventBus`; and an optional audit log written atomically with each
write. Two **breaking-minor** changes — see
[`docs/MIGRATION_v0.9.0.md`](docs/MIGRATION_v0.9.0.md). Detailed notes
in [`docs/RELEASE_NOTES_v0.9.0.md`](docs/RELEASE_NOTES_v0.9.0.md).

PRs included in this release: [#77] (Phase 5 opening, ADR-0012/0013),
[#78] (F5-1), [#80] (F5-2), [#81] (F5-3), [#82] (F5-4), [#83] (F5-5),
[#84] (F5-6), [#85] (F5-7).

[#77]: https://github.com/jcsvwinston/quark/pull/77
[#78]: https://github.com/jcsvwinston/quark/pull/78
[#80]: https://github.com/jcsvwinston/quark/pull/80
[#81]: https://github.com/jcsvwinston/quark/pull/81
[#82]: https://github.com/jcsvwinston/quark/pull/82
[#83]: https://github.com/jcsvwinston/quark/pull/83
[#84]: https://github.com/jcsvwinston/quark/pull/84
[#85]: https://github.com/jcsvwinston/quark/pull/85

### Added

#### F5-7 — Audit log (`Client.EnableAuditLog`)
- audit: `Client.EnableAuditLog(ctx, AuditConfig)` records every
  `Create`/`Update`/`Delete` into a `quark_audit` table. The table is
  migrated from a model so the DDL is portable across all six
  dialects (no hand-written `JSONB`/`BIGSERIAL`). Columns: `id`, `ts`,
  `tenant_id`, `user_id`, `table_name`, `operation`, `pk`, `diff`.
- audit: the audit row is written **inline on the CRUD
  connection/transaction**, so under `Client.Tx` it commits (or rolls
  back) atomically with the data — no committed data without its
  trail, no trail for rolled-back work (the "junto al commit" contract
  from ADR-0013, stronger than the post-commit EventBus emission).
- audit: `diff` payload — full row for `created`/`deleted`; new values
  for plain `Update`; per-column `{"old":…,"new":…}` delta for
  `Tracked.Save`. `AuditConfig` carries `UserFromContext`,
  `TenantFromContext`, `IncludeTables`, `ExcludeTables`
  (`quark_audit` always excluded — no recursion). Bulk/WHERE-based
  methods are not audited.
- docs: new `website/docs/advanced/audit-log.mdx` + sidebar entry.

#### F5-6 — `EventBus` (CRUD lifecycle events)
- events: public `EventBus` interface (`Publish(ctx, Event) error`)
  and `Event` interface (`Kind`/`Table`/`Payload`). `Client.UseEventBus(bus)`
  wires it to the CRUD pipeline — every `Create`/`Update`/`Delete`
  publishes a `created`/`updated`/`deleted` event once the write is
  durable. Inside `Client.Tx` the emit registers a `Tx.OnCommit` (fires
  post-commit, discarded on rollback); non-transactional CRUD emits
  inline after the statement.
- events: in-tree `LoggerEventBus` (slog) and `OTelEventBus`
  (correlation-tagged slog record) implementations as reference sinks.
- events: emit failures never roll back the committed write. The
  non-transactional path returns the new `quark.ErrEventEmitFailed`
  sentinel (wrapped) to the CRUD caller; the transactional path logs
  `quark.event.emit_failure` (no propagation — the commit already
  succeeded). Delivery is synchronous, at-least-once, no outbox
  (ADR-0013).
- docs: new `website/docs/advanced/events.mdx` (interfaces, in-tree
  buses, delivery semantics, external-broker skeleton). Sidebar entry
  added under Advanced.

#### F5-5 — `Tx.OnCommit` / `Tx.OnRollback` + `quark.TxFromContext`
- tx: `Tx.OnCommit(func(context.Context) error)` and
  `Tx.OnRollback(func(context.Context) error)` register
  side-effect callbacks that fire when the transaction reaches its
  terminal state. `OnCommit` callbacks fire FIFO after the model
  `After*` hooks once the commit succeeds; `OnRollback` callbacks
  fire FIFO after the rollback. A callback returning an error is
  logged (`quark.hook.on_commit_error` / `quark.hook.on_rollback_error`)
  but never blocks the chain or changes the value `Client.Tx`
  returns. Commit failures discard every queue.
- tx: `quark.TxFromContext(ctx) *Tx` resolves the active
  transaction from a context. `ForTx[T]` injects the `*Tx` into the
  query context so lifecycle hooks — which only receive `ctx` —
  can register OnCommit/OnRollback side-effects of their own.
  Returns nil outside a transaction.
- docs: `website/docs/guides/transactions.mdx` gains a
  "Side-effects on commit/rollback" section with the drain-order
  table and the `TxFromContext`-inside-a-hook pattern.

#### F5-4 — Transactional hooks (`After*` fire post-commit) + `BeforeFind`/`AfterFind`
- hooks: new `quark.BeforeFindHook` / `quark.AfterFindHook`
  interfaces; implementations are dispatched once per call to
  `List`, `First`, `Find`, `Iter`, or `Cursor`. `BeforeFind` fires
  before SQL is built; `AfterFind` fires after results are hydrated
  (including `Preload`). `Iter` and `Cursor` fire `AfterFind` only
  on successful completion.
- tx: `*quark.Tx` now carries a FIFO queue of model `After*` hooks
  that were issued through CRUD operations bound to that
  transaction via `ForTx[T]`. `Tx.Commit` drains the queue after
  the underlying `*sql.Tx.Commit` succeeds; `Tx.Rollback` discards
  it. Hooks returning an error post-commit are logged via the
  Client's `*slog.Logger` (event
  `quark.hook.after_post_commit_error`) and the cascade continues
  — once the database has confirmed the commit, application-level
  handlers cannot undo it (ADR-0013 Regla 2).
- docs: new `website/docs/guides/hooks.mdx` documenting all eight
  hook interfaces, the v0.9.0 timing-change table, FIFO ordering,
  and the `For[T]` vs `ForTx[T]` semantics. Sidebar entry added.

#### F5-3 — `quarktenant` CLI for installing PG RLS policies
- multi-tenant: new package `github.com/jcsvwinston/quark/quarktenant`
  ships an embedded-library CLI (`install-rls-policies` subcommand)
  that reads every model registered on a `*quark.Client`, generates
  the per-table policy DDL (`ALTER TABLE ... ENABLE/FORCE ROW LEVEL
  SECURITY` + `CREATE POLICY <table>_tenant_isolation`) and, when
  `--dry-run` is absent, applies it inside a single PostgreSQL
  transaction under a distributed migration lock. A failure mid-stream
  rolls back the entire install. See
  [`row-level-native.mdx`](website/docs/advanced/row-level-native.mdx)
  for the embedding pattern and
  [`examples/tenant-rls-native/main.go`](examples/tenant-rls-native/main.go)
  for a runnable example.
- multi-tenant: `quarktenant.InstallOptions` covers `TenantColumn`,
  `NativeRLSVar`, `ForceRLS` (default true), `DryRun`, `LockTimeout`,
  `LockName`, and `TenantColumnSQLCast`. The cast value is validated
  against a single-type-token whitelist (`text`, `uuid`, `bigint`,
  `varchar(64)`, …) and rejected with `ErrInvalidCast` otherwise —
  SQL-injection guard for the `--cast` flag.
- multi-tenant: `quarktenant.Run(ctx, args, client)` returns an exit
  code (`ExitSuccess=0`, `ExitError=2`) suitable for the user's
  `main.go` shell, mirroring the `quarkmigrate.Run` shape. CLI flags:
  `--dry-run`, `--tenant-col`, `--native-rls-var`, `--cast`,
  `--no-force-rls`, `--lock-name`, `--lock-timeout`.

#### F5-2 — Native PostgreSQL row-level security
- multi-tenant: nueva estrategia `quark.RowLevelSecurityNative`
  (PG-only) que delega aislamiento al motor. Cada query se ejecuta
  en una transacción implícita que emite
  `SELECT set_config('app.tenant_id', <tenantID>, true)`; las
  `CREATE POLICY` instaladas referencian ese setting para filtrar.
  El motor enforza incluso desde `client.Raw()`. Ver
  [`docs/adr/0012`](docs/adr/0012-rls-real-postgres-set-local-plus-policies.md)
  y [`row-level-native.mdx`](website/docs/advanced/row-level-native.mdx).
- multi-tenant: `TenantConfig.NativeRLSVar` (default `"app.tenant_id"`)
  para configurar el nombre del setting referenciado por las policies.
- multi-tenant: `TenantRouter.Tx(ctx, fn)` — método recomendado bajo
  Native. Abre una sola tx, emite `set_config`, invoca `fn(tx)`. Para
  estrategias non-Native delega al `Client.Tx` subyacente sin emitir
  el `set_config`.
- multi-tenant: implicit-tx vía `For[T](ctx, router)` bajo Native
  envuelve `Exec`/`Query`/`QueryRow` en transacciones implícitas con
  `set_config` emitido antes. El commit ocurre vía
  `context.AfterFunc(ctx, ...)` por la opacidad de `*sql.Rows`. Para
  ctx long-lived (CLI batch), usar `router.Tx` explícito.
- multi-tenant: construir un `Query[T]` bajo `RowLevelSecurityNative`
  con dialecto no-PostgreSQL devuelve `ErrUnsupportedFeature`. Igual
  comportamiento desde `TenantRouter.Tx`.

#### Fase 5 — apertura formal (planning)
- docs: [ADR-0012](docs/adr/0012-rls-real-postgres-set-local-plus-policies.md)
  — RLS real Postgres vía `SET LOCAL app.tenant_id` + `CREATE POLICY`.
  Supersedes ADR-0003. Anticipa F5-1..F5-3 (rename + motor Native +
  CLI `quark tenant install-rls-policies`).
- docs: [ADR-0013](docs/adr/0013-transactional-hooks-and-sync-eventbus.md)
  — hooks transaccionales (`Before*` inside-tx-abortable, `After*`
  post-commit, nuevos `BeforeFindHook`/`AfterFindHook`), `Tx.OnCommit`/
  `Tx.OnRollback`, y `EventBus` síncrono en commit-phase (at-least-once,
  sin outbox). Anticipa F5-4..F5-7.
- docs: `TASKS.md` Fase 5 — descomposición formal en F5-1..F5-7 con
  archivo:línea, definition of done y estimación por ítem.
- docs: `docs/ROADMAP.md` Phase 5 — entrega esperada en v0.9.0.
- docs: `docs/playbooks/tenant.md` actualizado tras ADR-0012 (frontmatter,
  P0-1 movido a histórico, plan apuntando a F5-1/F5-2/F5-3).

### Changed
- docs: ADR-0003 marcado `superseded` por ADR-0012 (banner + frontmatter
  `superseded-by: 0012` + entrada de índice).
- multi-tenant: la constante `TenantStrategy` `RowLevelSecurity` se
  renombra a `RowLevelSecurityClient` (F5-1). El nombre antiguo
  permanece como **alias deprecado con el mismo valor** — el código
  existente sigue compilando sin cambios. La doc y los ejemplos
  pasan a usar el nombre canónico. Ver
  [ADR-0012](docs/adr/0012-rls-real-postgres-set-local-plus-policies.md).
- hooks (**breaking minor**, F5-4): `AfterCreate` / `AfterUpdate` /
  `AfterDelete` hooks invoked through a `Query[T]` bound to an
  explicit transaction (via `ForTx[T]` inside `Client.Tx`) now fire
  **after the transaction commits** instead of inline after the SQL
  statement. The non-transactional path (`For[T]` against a plain
  Client) is unchanged — hooks still fire inline. Callers that
  relied on inline post-INSERT timing inside `Client.Tx` should
  audit the change; see [`docs/MIGRATION_v0.9.0.md`](docs/MIGRATION_v0.9.0.md).
- events (**breaking minor**, F5-6): the v0.8.0 placeholder struct
  `EventBus` (a LISTEN/NOTIFY factory whose `CreateListener` only ever
  returned `ErrDialectNotSupported`) is renamed to `ListenerFactory`,
  and `NewEventBus` to `NewListenerFactory`, to free the `EventBus`
  name for the new CRUD-event interface. The struct was non-functional
  (always errored), so no working code path changes behaviour. See
  [`docs/MIGRATION_v0.9.0.md`](docs/MIGRATION_v0.9.0.md).

### Deprecated
- `quark.RowLevelSecurity` — usa `quark.RowLevelSecurityClient`. El
  alias se retira en v1.0. La nueva nomenclatura aclara que esta
  estrategia es WHERE-injection cliente; la modalidad de motor real
  (PostgreSQL `set_config('app.tenant_id', ...)` + `CREATE POLICY`)
  ya disponible como `RowLevelSecurityNative` (F5-2).

## [0.8.0] - 2026-05-15

Phase 4 release — observability, stampede-protected caché, and resilience.
Closes F4-1 through F4-7: OTel metrics + span redaction; structured
slow query log; deterministic cache key (the post-v0.7 fix that became
the F4-5 prerequisite); cache stampede protection (singleflight +
jitter + XFetch via `stampedeStore` wrapper, ADR-0011); per-row cache
invalidation + Redis tag-TTL fix; deadlock retry on `Client.Tx`. No
breaking changes; every new feature is opt-in. Detailed notes in
[`docs/RELEASE_NOTES_v0.8.0.md`](docs/RELEASE_NOTES_v0.8.0.md).

PRs included in this release:
[#67] (Phase 4 opening, ADR-0011),
[#68] (release-please Node 24),
[#69] (F4-4 cache key determinism — prerequisite, landed in 0.7.x but
foundational for Phase 4),
[#70] (F4-1 + F4-2 OTel metrics + span redaction),
[#71] (F4-3 slow query log),
[#72] + [#73] (F4-5 stampede protection + gofmt fix),
[#74] (F4-6 per-PK invalidation + Redis tag-TTL),
[#75] (F4-7 deadlock retry).

### Added

- **Deadlock retry on `Client.Tx` (F4-7)** — new
  `quark.WithDeadlockRetry(maxAttempts)` `Option`. When the
  transaction closure returns an error that `isDeadlock` recognises
  from the active driver — PG `40P01`, MySQL/MariaDB `1213`, MSSQL
  `1205`, Oracle `ORA-00060` — the runner sleeps with exponential
  backoff + ±50% jitter (10ms doubling, capped at 1s) and re-executes
  the closure against a fresh transaction. Non-deadlock errors
  propagate on the first attempt; a cancelled context aborts the
  backoff and surfaces `ctx.Err()`.

  The retry wraps the **entire** closure, never an individual query —
  a deadlock aborts the whole tx, so re-running a single statement
  inside a half-committed state would race. Disabled by default
  (`maxAttempts <= 1`): callers explicitly opt in. SQLite is
  single-writer and never raises a true deadlock; the option is a
  no-op there.

  New `isDeadlock(err)` helper in `db_errors.go` follows the same
  driver-shape pattern as the existing `isUniqueViolation` (P0-3),
  using `errors.As` against each driver's error type so wrapped errors
  classify correctly. With this, **Phase 4 is complete** — F4-1
  through F4-7 all closed.

- **Per-row cache invalidation + Redis tag-TTL fix (F4-6)** — two cache
  improvements that ship together:

  - `executeExec` now accepts an `extraTags ...string` variadic. When a
    mutation knows its affected primary key (`Update`, `UpdateFields`,
    `Tracked.Save`, soft / hard `Delete` by PK, `Create` after the new
    ID is populated), it passes `<table>:<pk>` so the single
    `InvalidateTags` call carries both the table tag (historical
    default — listings stay consistent) AND the row tag. Callers can
    now cache by-PK queries with the per-row tag and avoid the
    "every row write flushes the whole table" amplification documented
    in the cache playbook. Composite-PK models and mutations with
    unknown rows (`DeleteBatch` WHERE-complex, `UpdateBatch`, raw
    `Exec`) keep the table-only fallback.
  - `cache/redis/redis.go:Set` replaces the historical single
    `pipe.Expire(tag, ttl+24h)` with `pipe.ExpireNX(...)` followed by
    `pipe.ExpireGT(...)`. The first initialises the tag-set TTL when
    the SET was just created (no TTL); the second extends only when
    the new TTL is greater than the current one. The tag-set TTL is
    therefore the MAX across every key tagged with it — keys can no
    longer outlive their tag entry and become unreachable through
    `InvalidateTags`. Requires Redis 7.0+ (the `NX`/`GT` flags landed
    there); older servers fall back to the historical (broken)
    behaviour — documented gap.

  Tests: `cache_invalidation_test.go` — `TestRowTag_Format` (5 cases),
  `TestInvalidateRowTag_*` (4 cases), `TestExecuteExec_PassesRowTagAlongTable`
  (3 cases pinning the wire-up). The Redis tag-TTL behaviour is harder
  to unit-test without a live Redis 7+ server; the change is a 1-line
  pipeline command swap with a defensive comment trail.

- **Cache stampede protection (F4-5, [ADR-0011](docs/adr/0011-cache-stampede-protection-wrapper.md))**
  — every `CacheStore` installed via `WithCacheStore` is now wrapped
  automatically with three in-process protections:

  - **Singleflight** (via `golang.org/x/sync/singleflight`): `N`
    concurrent callers for the same cache key collapse to a single
    compute. A miss never produces a database stampede on a hot key.
  - **TTL jitter**: every `Set` randomises the TTL by `±jitterPct`
    (default `±10%`), so batch-warmed entries don't expire in lockstep.
  - **XFetch / probabilistic early refresh**: every entry carries
    metadata (compute delta + timestamps) embedded as a length-prefixed
    `xfetchEntry`. `Get` evaluates the Vattani probability threshold
    and signals early refresh near expiry, smoothing the load curve.

  Two new `Option`s tune the wrapper:

  - `quark.WithCacheJitter(pct float64)` — `0..1`, default `0.1`. `0`
    disables jitter; singleflight + XFetch stay on.
  - `quark.WithCacheXFetchBeta(beta float64)` — `β ≥ 0`, default `1.0`.
    `β = 0` disables XFetch; singleflight + jitter stay on.

  The wrapper implements the public `CacheStore` interface, so
  `memory.Store`, `redis.Store` and any third-party store keep working
  unchanged inside it. The query path uses a richer in-package
  `getOrCompute` shortcut when the wrapper is present (the default once
  `WithCacheStore` is configured); third-party stores still get the
  historical cache-aside dance. Known gap: singleflight is in-process
  only — cross-instance stampede is not covered (ADR successor if
  demand appears).

- **Slow query log (F4-3)** — new `quark.WithSlowQueryThreshold(d)`
  Client option. When set, every operation whose duration exceeds `d`
  emits a structured WARN through `Client.logger` (`*slog.Logger`)
  before any registered `QueryObserver` is notified. The line carries
  `duration_ms`, `threshold_ms`, `operation`, `table`, `rows` and `sql`
  (parameterised). Bind arguments are NOT included — the same redaction
  principle as F4-2 spans. Default threshold `0` (disabled); negative
  values are also treated as disabled. The check is a single comparison
  on the centralised `notifyObservers` path, so a Client with the
  feature off pays nothing.

- **OTel metrics (F4-1)** — the `quark/otel` middleware now emits three
  OpenTelemetry instruments alongside spans on the
  `github.com/jcsvwinston/quark` meter:
  - `quark.queries.total` — Int64 counter, every Quark operation
    increments.
  - `quark.queries.duration` — Float64 histogram in milliseconds,
    wall-clock time of the wrapped operation.
  - `quark.queries.rows` — Int64 histogram of `sql.Result.RowsAffected`,
    emitted only on Exec (`SELECT` / `SELECT_ROW` would require wrapping
    `*sql.Rows`; documented as future work).

  Every data point carries `db.operation` (`EXEC` / `SELECT` /
  `SELECT_ROW`) and, when set via `WithDBSystem`, `db.system`. The meter
  is resolved lazily from the OTel global `MeterProvider`, same panic-safe
  pattern as the tracer; tests use `sdkmetric.ManualReader` to verify
  emission.

- **Span argument redaction (F4-2)** — new `otel.WithSpanRedaction(mode)`
  option. Default `RedactArgs` keeps bind values out of every span (only
  the parameterised SQL reaches `db.statement`). Opt-in `IncludeArgs`
  attaches `db.statement.args` as a string slice — for local debugging
  only; a tracing backend MUST NOT see user values it has no authority to
  retain.

- **`otel.WithDBSystem(name)`** option — sets the `db.system` attribute
  on spans and metrics (e.g. `"postgres"`). The middleware does not
  introspect the Quark `Client`; callers pass the dialect name when
  constructing the middleware. Default: attribute omitted.

### Fixed

- **Cache key collisions (F4-4)** — `generateCacheKey` no longer encodes
  bind arguments with `fmt.Sprintf("%v", arg)`. The encoding is now
  type-tagged and length-prefixed, closing three collision classes a
  parameterised cached SELECT could hit: type collisions (`int64(1)` vs
  `string("1")`, also `uint64` / `float64` / `bool` / `nil`), boundary
  collisions (no separators meant tenant `"my"`+schema `"sql"` hashed
  the same stream as `"mysql"`+`""`, and args `"ab"`+`""` the same as
  `"a"`+`"b"`), and `nil` vs `""`. `time.Time` is keyed by `UnixNano()`
  so the same instant in different zones is one key (a legitimate hit).
  Unknown types fall back to `%#v` (includes the Go type, does not
  invoke a `Stringer`). Reflection-free (ADR-0002). Prerequisite for
  the F4-5/F4-6 cache work.

[#67]: https://github.com/jcsvwinston/quark/pull/67
[#68]: https://github.com/jcsvwinston/quark/pull/68
[#69]: https://github.com/jcsvwinston/quark/pull/69
[#70]: https://github.com/jcsvwinston/quark/pull/70
[#71]: https://github.com/jcsvwinston/quark/pull/71
[#72]: https://github.com/jcsvwinston/quark/pull/72
[#73]: https://github.com/jcsvwinston/quark/pull/73
[#74]: https://github.com/jcsvwinston/quark/pull/74
[#75]: https://github.com/jcsvwinston/quark/pull/75

## [0.7.0] - 2026-05-14

Minor release — per-column timezones. Closes the last deferred type
from Phase 1's Bloque B: `time.Time` columns can now declare a
timezone (`quark:"tz=Europe/Madrid"`) or inherit a Client-wide default
(`quark.WithDefaultTZ`), with a UTC-always wire contract. No breaking
changes; no migration guide. Fully additive — callers that don't use
the feature see no change from v0.6. Detailed notes in
[`docs/RELEASE_NOTES_v0.7.0.md`](docs/RELEASE_NOTES_v0.7.0.md).

PRs included in this release: [#63] (per-column timezone override).

### Added

- **Per-column timezone override** ([ADR-0010](docs/adr/0010-per-column-timezone-override.md)):
  closes the last deferred type from Phase 1's Bloque B. Two opt-in
  knobs control the timezone of `time.Time` columns:

  - `quark.WithDefaultTZ(loc *time.Location)` — a Client-wide fallback
    for `time.Time` columns without their own tag.
  - `quark:"tz=Europe/Madrid"` — a per-column override tag.

  Precedence is column tag → client default → pass-through. The wire
  contract is **UTC-always**: when a column resolves to a location, the
  `time.Time` is converted to UTC on the way to the driver (every
  dialect stores the same instant) and to the configured location in
  memory on scan. The tag is honoured on `time.Time`, `*time.Time` and
  `Nullable[time.Time]` fields, including through `Preload`. An invalid
  IANA name is rejected fail-fast by `Client.RegisterModel` and
  `Client.Migrate` with the new `ErrInvalidTimezone` sentinel. A column
  with neither a tag nor a client default passes through to the driver
  untouched — the feature is fully opt-in and changes nothing for
  callers that don't use it. The bind/scan hot paths gate on an O(1)
  flag so models and clients without timezones pay no overhead
  (ADR-0002 — no extra reflect in hot paths).

- **`ErrInvalidTimezone`** sentinel error — surfaced by
  `Client.RegisterModel` / `Client.Migrate` when a `quark:"tz=..."` tag
  carries an invalid IANA timezone name. The wrapped error names the
  field, the column and the offending string.

[#63]: https://github.com/jcsvwinston/quark/pull/63

## [0.6.0] - 2026-05-14

Phase 3 release — schema-as-code migrations. Closes F3-1 through F3-7:
distributed migration lock; neutral schema introspection across the
4 CI dialects + SQLite (columns, indexes, foreign keys, check
constraints); pure-Go schema diff; the models→Plan pipeline; transactional
and resumable `ApplyPlan`; `quarkmigrate` plan/verify/apply CLI
workflow; orchestrated `Backfill` with resume tokens; and per-Client
model registry. Also lands `Array[T]` — typed wrapper for list-shaped
columns, closing the Bloque B Arrays Postgres item from Phase 1
deferred work. No breaking changes; no migration guide. Detailed
notes in [`docs/RELEASE_NOTES_v0.6.0.md`](docs/RELEASE_NOTES_v0.6.0.md).

PRs included in this release:
[#42] (`Array[T]`),
[#43] (Phase 3 ADR-0009),
[#44] (F3-1 migration lock),
[#45] (F3-2 core: SQLite + PG),
[#47] (F3-2 MySQL + MariaDB),
[#48] (F3-2 MSSQL),
[#49] (F3-2 indexes),
[#50] (F3-2 FKs),
[#51] (F3-2 checks),
[#52] (F3-3 diff core),
[#53] (F3-3 plan + SQLite PK fix),
[#54] (F3-3 execute),
[#55] (F3-3 types + defaults normalisation),
[#56] (F3-4 transactional `ApplyPlan`),
[#57] (F3-4 resumable `ApplyPlan`),
[#58] (F3-5 `quarkmigrate` CLI),
[#59] (F3-6 `Backfill`),
[#60] (F3-7 per-Client model registry).

### Documentation

- **Phase 3 formally opened** ([ADR-0009](docs/adr/0009-migrations-introspection-diff-not-versioned-files.md))
  with the decomposition into F3-1..F3-7 in `TASKS.md`. Strategy:
  code-first + diff bidireccional (introspection-based diff against
  the live DB, not only versioned files). Phase 3 closes when the
  seven items land; that release becomes v0.6.0.

### Added

- **`Client.IntrospectSchema(ctx)` — neutral schema introspection
  (F3-2 core)**: returns the current database's schema as a
  dialect-neutral `Schema{Tables[]Table{Name, Columns[]Column}}`.
  Foundation for the F3-3 diff comparator. New optional
  `SchemaIntrospector` interface on Dialect (same opt-in pattern as
  `MigrationLocker`). Implementations land for
  **SQLite** (`sqlite_master` + `PRAGMA table_info`),
  **PostgreSQL** (`information_schema.tables` /
  `information_schema.columns` with `current_schema()` scoping +
  type-parameter reassembly for `varchar(N)` / `numeric(P,S)`),
  **MySQL / MariaDB** (`INFORMATION_SCHEMA.{TABLES,COLUMNS}` scoped
  to `DATABASE()`, using `COLUMN_TYPE` for the full parameterised
  type string), and
  **MSSQL** (`sys.tables` / `sys.columns` / `sys.types` /
  `sys.default_constraints` with type reassembly from
  `max_length`, `precision`, `scale`; nvarchar/nchar
  byte-to-char halving; `MAX` for `max_length = -1`).
  Oracle still returns `ErrUnsupportedFeature` until F3-2-oracle
  (deferred — no CI coverage until the `gvenzl/oracle-free`
  image issue resolves). Foreign keys and check constraints are
  deferred to F3-2-{fks, checks} — `Table` ships with column +
  index metadata for now.

- **Per-Client model registry (F3-7)**: closes Phase 3. Adds three
  methods on `*Client` for managing which models the Client is
  responsible for, with convenience wrappers for the F3-3/F3-5
  workflows:

  - `Client.RegisterModel(models ...any) error` — appends models
    to the per-Client registry. Validates every model up front
    (must be struct or `*struct`, no untyped nil) and refuses
    partial registration on failure. Safe for concurrent use.
  - `Client.RegisteredModels() []any` — returns a snapshot of
    registered models in registration order. Mutations to the
    returned slice don't affect the internal registry.
  - `Client.MigrateRegistered(ctx)` — convenience for
    `Migrate(ctx, c.RegisteredModels()...)`. No-op (returns nil)
    when nothing is registered.
  - `Client.PlanMigrationRegistered(ctx)` — convenience for
    `PlanMigration(ctx, c.RegisteredModels()...)`. Returns an
    empty `Plan` when nothing is registered.

  Intentionally additive — the global type-meta cache in
  `internal/schema` is unchanged because it's correct as global
  state (deterministic per `reflect.Type`). F3-7's per-Client
  registry is about "which models this Client manages", NOT about
  the meta-computation cache. Multi-tenant deployments with
  multiple Clients (per ADR-0007) can now each track their own
  model set without cross-contamination.

  Calling `RegisterModel` multiple times APPENDS — it does NOT
  dedupe. Documented and pinned by a test
  (`TestClient_RegisterModel_DoesNotDeduplicate`) so a future
  "smart dedup" doesn't silently change behaviour.

- **`Client.Backfill` — orchestrated table backfill with resume
  tokens (F3-6)**: the data-ops counterpart to F3-3..F3-5's schema
  story. Iterates a table by primary key in batches, invokes a
  user callback per batch with the PK list, and persists the
  highest PK seen in a `quark_backfill_state(name, last_pk,
  updated_at)` table keyed by spec name. A process kill / callback
  error / deliberate retry resumes at `WHERE pk > last_pk` rather
  than re-running the entire table.

  Idempotent on completion: a re-invocation with the same Name
  after all batches were processed finds nothing to do and
  returns nil immediately.

  API:

      type BackfillSpec struct {
          Name      string                                              // resume key
          Table     string                                              // source table
          PKColumn  string                                              // default "id"
          BatchSize int                                                 // default 1000
          Process   func(ctx context.Context, batchPKs []int64) error
      }
      func (c *Client) Backfill(ctx context.Context, spec BackfillSpec) error

  Why the callback receives PKs (not row contents): backfill SQL
  is rarely "SELECT * + transform"; it's "UPDATE ... WHERE id IN
  (...)" or "INSERT ... SELECT ... WHERE id IN (...)" where the
  user already knows the relevant columns. Passing PKs keeps the
  helper out of the way and avoids a generics-or-reflect API
  expansion.

  Limitations: integer PKs only (text PKs and composite PKs out
  of scope for F3-6-core); positive PKs assumed for the
  `last_pk=0` fresh-start case (negative-PK tables need pre-seeded
  state). Concurrency follows the same pattern as ApplyPlan's
  resumable path — wrap with `AcquireMigrationLock` if you need
  cross-process serialisation.

  Per-dialect catalog tables created via the same pattern as
  `quark_migration_state` (MSSQL sys.tables guard, Oracle
  swallows ORA-00955). Filtered out of `IntrospectSchema` by the
  existing `quark_*` exclusion so the state table doesn't surface
  in user plans.

- **`quarkmigrate` package — plan/verify/apply CLI workflow (F3-5)**:
  a thin library helper that turns a configured `quark.Client` plus
  a set of Go model values into a three-action CLI workflow
  designed to be embedded in a user-side `migrations/main.go`:

  - `plan`: print the plan, exit 0 (informational).
  - `verify`: print the plan, exit 1 if non-empty (CI gate use).
  - `apply`: print the plan, run it if non-empty, exit 0 on success.

  Operational error (PlanMigration / ApplyPlan failure, unknown
  action) is exit 2 across all three actions. Exit codes are
  exposed as constants `ExitSuccess` (0) / `ExitDriftDetected` (1)
  / `ExitError` (2) for callers that want to assert on them.

  `quarkmigrate.Run(ctx, action, client, models...)` is the public
  entry point; `RunWithOutput` is the test-friendly variant that
  takes explicit writers. `ParseAction(s)` accepts the literal
  strings `"plan"`, `"verify"`, `"apply"`, plus `""` (defaults to
  `plan`).

  Plan output is prefixed with the short Plan.Hash() so users can
  correlate runs against the `quark_migration_state` resumable
  table when running on MySQL / MariaDB / Oracle.

  Example wrapper in `examples/migrations/main.go` — a complete
  user-side `main.go` showing how to read DSN/dialect from env,
  pass models, and route exit codes. Adapt to a real project by
  swapping in the user's model package.

  Why a library and not a binary: Go has no runtime model
  registration (the binary would need to import the user's
  models package, which only their code can do). The thin
  wrapper pattern is the idiomatic answer — users own a tiny
  `main.go` that imports both quarkmigrate and their models.

- **Resumable `ApplyPlan` on non-transactional engines (F3-4-resumable)**:
  closes F3-4 entirely. On MySQL, MariaDB, and Oracle (where DDL
  implicitly commits and the F3-4-tx wrapper has no effect),
  `ApplyPlan` now records each successfully applied op in a
  `quark_migration_state` table keyed by `(plan_hash, op_index)`.
  A re-invocation against the same plan (same `Plan.Hash()`) skips
  ops that were already recorded.

  Workflow on a non-tx engine when something goes wrong mid-plan:

  1. `ApplyPlan` runs ops 0..N, op N+1 fails. Ops 0..N are
     implicitly committed; state table records each.
  2. User addresses the underlying problem (missing referenced
     table, unique constraint conflict, etc.).
  3. User calls `ApplyPlan` again with the same plan. Resume path
     reads the state, sees ops 0..N applied, starts from op N+1.
     No re-applying earlier ops — no duplicate-key, no idempotency
     guesswork.

  Drift detection: the `plan_hash` (SHA-256 of the concatenated
  `op.String()` outputs) means two plans differing in any way
  produce independent state. A user who edits their models between
  runs starts a fresh sequence — no false "resume from op 3"
  against a plan whose op 3 means something different.

  New `Plan.Hash() string` method exposes the hash for users who
  want to inspect it (e.g. log the plan ID in CI gates).

  Transactional engines (PG / MSSQL / SQLite) skip the resumable
  path entirely — rollback handles failure cleanly, no state
  table needed. The `quark_migration_state` table is filtered out
  of `IntrospectSchema` by the existing `quark_*` exclusion, so it
  doesn't surface in user plans.

- **Transactional `ApplyPlan` (F3-4-tx)**: on engines with
  transactional DDL — **PostgreSQL, MSSQL, SQLite** — `Client.ApplyPlan`
  now wraps the op loop in `BEGIN ... COMMIT`. A mid-plan failure
  rolls back the whole plan, leaving the schema in its pre-plan
  state. This is the safety net users should rely on when running
  migrations against production on these engines.

  **MySQL, MariaDB, Oracle**: DDL implicitly commits on every
  statement, so wrapping is pointless. ApplyPlan on these engines
  retains the original no-tx behaviour — a mid-plan failure leaves
  the schema partially applied. The eventual F3-4-resumable
  follow-up adds a `quark_migration_state` checkpoint table for
  these engines so a manual resume can pick up where the plan
  left off.

  Internal refactor: `Client.CreateIndex` and `Client.AddForeignKey`
  now wrap private `createIndexOn` / `addForeignKeyOn` helpers
  that take an `Executor`. Public API unchanged; the tx path
  routes its DDL through the underlying `*sql.Tx` while the public
  helpers continue to use `c.db`. All per-dialect drop / add /
  alter helpers in the executor follow the same pattern.

  Integration contract: new `ApplyPlan_TransactionalRollback`
  test in SharedSuite asserts the right behaviour per dialect
  (rollback erases the probe table on PG/MSSQL/SQLite; probe
  persists on MySQL/MariaDB because of implicit commits — the
  test pins both, so future improvements have a clear contract
  to flip).

- **Cross-dialect type + default normalisation (F3-3-types)**: the
  diff's `columnsEqual` now normalises both type strings AND
  default values before comparing, so the migrator's canonical forms
  compare equal to what each engine's catalog actually stores.

  Type normalisation (`normalizeType`):
  - Case-fold + trim.
  - PG alias `character varying` → `varchar` (PG's information_schema
    returns the SQL-standard form while the migrator emits the
    engine alias).
  - MySQL display-width strip (`int(11)` → `int`) for old MySQL 5.7 /
    mixed-version clusters.
  - `int` ≡ `integer` collapse. The migrator emits `INTEGER` (SQL
    standard); MySQL / MariaDB / MSSQL catalogs return `int`; PG
    catalog returns `integer`. Without this, an `int64` field on
    any of those engines produced a perpetual spurious
    `OpAlterColumn`.

  Default normalisation (`defaultsEqual`):
  - PG `nextval(...)` ≡ nil. PG SERIAL / IDENTITY columns expose
    their autoincrement sequence via the DEFAULT clause
    (`nextval('table_col_seq'::regclass)`); the Go-side desired
    Schema has `Default=nil` because models don't declare nextval
    as a default. Treating these as equal closes the loop for any
    PG model with an int PK. MySQL / MSSQL / SQLite use other
    mechanisms (EXTRA field, IDENTITY property, AUTOINCREMENT
    keyword) that don't produce a COLUMN_DEFAULT row, so they need
    no normalisation.

  Headline contract: **`PlanMigration` round-trip is now empty on
  all 5 motors** after `Migrate(model)`. Integration test
  `PlanMigration_RoundTripScopedToFixture` runs on PG / MySQL /
  MariaDB / MSSQL / SQLite via SharedSuite (scoped to its own
  fixture because the SharedSuite leaves unrelated tables behind
  that the diff legitimately wants to drop). The CLI plan command
  (F3-5) can now be built on this without producing noisy plans on
  production engines.

  Not yet normalised: PG `int8`/`int4`/`int2` ↔ `bigint`/`integer`/
  `smallint` (information_schema returns SQL-standard names so this
  never arises from introspection; only relevant for hand-constructed
  Schemas).

- **`Client.ApplyPlan(ctx, plan)` — Plan executor (F3-3-execute)**:
  walks the operations in a [Plan] in order and dispatches each to
  the appropriate per-dialect DDL. Closes the F3-3 trio: with
  `IntrospectSchema` + `Diff` + `PlanMigration` + `ApplyPlan`,
  users can now do the full round-trip (model → plan → apply →
  verify) without writing DDL by hand. Dispatch per op type:
  CreateTable rebuilds DDL from the neutral `Table` struct;
  DropTable / AddColumn / DropColumn / AlterColumn (type only)
  use the dialect helpers from F3-2; CreateIndex / AddForeignKey
  reuse the existing F2-era helpers; DropIndex / DropForeignKey /
  AddCheck / DropCheck have new per-dialect dispatch inline.

  Surface limitations documented:
  - **OpAlterColumn**: only emits DDL for type changes today.
    Nullable / Default deltas are no-ops (TODO F3-3-execute-alter).
  - **SQLite + DropForeignKey / DropCheck**: returns
    `ErrUnsupportedFeature` — SQLite has no `ALTER TABLE DROP
    CONSTRAINT`, the workaround is the 12-step table-rebuild
    procedure, which is its own follow-up (F3-3-execute-sqlite-
    rebuild).
  - **MySQL/MariaDB <8.0.16 / <10.2.1 + AddCheck**: same Error
    1146 path as F3-2-checks would surface; not specifically
    handled here since the catalog state would prevent the diff
    from emitting the AddCheck op in the first place.

  Not transactional in this PR — F3-4 (resumable migrations) adds
  the BEGIN/COMMIT wrapper. Today a mid-plan failure leaves the
  schema partially applied; the returned error carries the op
  index + the op's String() so the caller can identify the
  failure point.

- **`Client.PlanMigration(ctx, models...)` — models-to-plan
  pipeline (F3-3-plan)**: takes one or more Go model structs and
  returns a `Plan{Ops []Operation}` describing what the database
  would need to change to align with the models. The pipeline is
  models → desired Schema (reflect on the cached ModelMeta /
  FieldMeta, reusing the migrator's `SQLTypeWithOpts` for type
  strings) → `IntrospectSchema` for the current state →
  `Diff(desired, current)` → `Plan`. The Plan is **inert** — no
  side effects; F3-3-execute is the follow-up that adds Apply.
  `Plan.IsEmpty()` and `Plan.String()` make the result trivially
  consumable by health endpoints, CI checks, and the F3-5 CLI.

  Round-trip identity is the headline contract: after
  `Migrate(model)`, `PlanMigration(model)` returns an empty Plan
  on SQLite. The contract test is in `migrate_plan_test.go`.
  Cross-dialect type-string drift (PG `bigint` vs migrator
  `BIGINT`) is documented as a known gap with a normalisation
  follow-up planned; spurious OpAlterColumn ops on PG/MySQL/MSSQL
  are expected today.

  PlanMigration intentionally **copies** the index / FK / check
  surface from the current schema into the desired one before
  diffing, because struct tags don't yet declare schema-level
  objects beyond columns. That keeps the plan honest until
  F3-3-plan-indexes lets tags drive them.

- **SQLite introspector fix — PK columns now report Nullable=false**:
  the PRAGMA `notnull` field is 0 for `INTEGER PRIMARY KEY`
  columns even though they're implicitly NOT NULL in SQLite. The
  fix ORs in the PRAGMA's `pk` field so the introspector output
  is symmetric cross-dialect (PG/MySQL/MSSQL already report
  is_nullable=false for PKs via their catalog). Visible to F3-3-plan
  callers because without this fix the round-trip diff would emit
  a spurious `nullable true→false` alter on every PK column.

- **Pure-Go schema diff algorithm (F3-3-core)**: `Diff(desired,
  current Schema) []Operation` returns the ordered list of changes
  needed to bring `current` into alignment with `desired`. Operations
  are dialect-neutral sealed types (`OpCreateTable`, `OpDropTable`,
  `OpAddColumn`, `OpDropColumn`, `OpAlterColumn`, `OpCreateIndex`,
  `OpDropIndex`, `OpAddForeignKey`, `OpDropForeignKey`,
  `OpAddCheck`, `OpDropCheck`) — each carries the neutral shape
  needed to render DDL via the per-dialect helpers in F3-3-execute
  (follow-up PR). The diff is **pure and deterministic** (same
  input → same output, stable sort) and **conservatively-typed**
  (matches columns / indexes / checks by name; matches FKs by name
  or by composite `(columns, ref_table, ref_columns)` key when the
  catalog returned an empty name — the SQLite inline-FK case).

  Cross-dialect awareness baked into the equality functions:
  the MariaDB `RESTRICT` vs MySQL `NO ACTION` FK-action divergence
  (documented in `ForeignKey` godoc) is treated as semantically
  equivalent so no spurious DROP+ADD ops appear on every plan.
  SQLite's `Checks=nil` contract is respected: when either side
  has `Checks=nil` for a table, the check comparison is skipped
  rather than treating `nil` as "no checks" (which would emit
  DropCheck for every check on the other side).

  Op ordering follows dependency rules: CREATE TABLE first; per
  shared table, ADD COLUMN → ALTER COLUMN → DROP CHECK → DROP FK
  → DROP INDEX → DROP COLUMN → CREATE INDEX → ADD FK → ADD CHECK;
  DROP TABLE last. The full algorithm is documented on the [Diff]
  godoc. Index shape changes (columns or unique flag) are modelled
  as DROP+CREATE since no engine supports altering an index in
  place.

  Follow-up F3-3-plan PR will add `Client.PlanMigration(ctx, models...)`
  to drive this from Go-side model types.

- **CHECK constraint introspection on the 4 CI dialects (F3-2-checks)**:
  `Table.Checks` is now populated with `Check{Name, Expression}`.
  Per-dialect catalogs: **PostgreSQL** `pg_constraint` (contype='c')
  with `pg_get_constraintdef(oid, true)` for the canonical expression
  text (the leading `CHECK ` keyword is stripped so `Expression`
  carries the predicate only);
  **MySQL / MariaDB** `INFORMATION_SCHEMA.TABLE_CONSTRAINTS` joined
  with `INFORMATION_SCHEMA.CHECK_CONSTRAINTS` (MySQL 8.0.16+,
  MariaDB 10.2.1+). Older versions don't have the
  `CHECK_CONSTRAINTS` catalog at all — the query would return
  `Error 1146: Table … doesn't exist`. `mysqlListChecks` detects
  that specific error and degrades to an empty result, keeping
  `IntrospectSchema` usable on older engines (which never
  enforced CHECK anyway, so "empty" is semantically correct);
  **MSSQL** `sys.check_constraints` filtered by parent table
  `OBJECT_ID`. The expression is passed through raw per dialect
  (each engine has its own canonical form — `((age > 0))` on PG,
  `` (`age` > 0) `` on MariaDB, `([age]>(0))` on MSSQL); F3-3 handles
  expression equivalence at the AST level.
  **SQLite intentionally deferred**: SQLite has no catalog for CHECK
  constraints, the only path is parsing `sqlite_master.sql` DDL —
  brittle and out of scope for the catalog-reader layer.
  `Schema.Tables[i].Checks` is `nil` on SQLite (intentionally not
  populated, NOT "no CHECK constraints"); a future
  `F3-2-checks-sqlite` follow-up could add DDL parsing if user
  demand justifies it.

- **Foreign-key introspection across the 4 CI dialects + SQLite
  (F3-2-fks)**: `Table.ForeignKeys` is now populated with
  `ForeignKey{Name, Columns, RefTable, RefColumns, OnDelete, OnUpdate}`.
  Per-dialect catalogs: **SQLite** `PRAGMA foreign_key_list`
  (groups rows by synthetic `id`; constraint Name comes back `""`
  since the PRAGMA doesn't preserve names — the diff layer matches
  on column-tuple instead);
  **PostgreSQL** `pg_constraint` (contype='f') with
  `unnest(conkey/confkey) WITH ORDINALITY` for stable composite-FK
  column matching; `confdeltype`/`confupdtype` single-char codes
  translated to verbose form;
  **MySQL / MariaDB** `INFORMATION_SCHEMA.KEY_COLUMN_USAGE`
  joined with `REFERENTIAL_CONSTRAINTS` (UPDATE_RULE / DELETE_RULE
  passthrough);
  **MSSQL** `sys.foreign_keys` joined with `sys.foreign_key_columns`
  / `sys.tables` / `sys.columns` ×2; underscored
  `*_referential_action_desc` strings (`NO_ACTION`, `SET_NULL`,
  `SET_DEFAULT`) normalised to SQL-standard spaces.
  All dialects emit `OnDelete`/`OnUpdate` as the SQL-standard
  verbose form (`CASCADE`, `SET NULL`, `SET DEFAULT`, `RESTRICT`,
  `NO ACTION`).

- **Index introspection across the 4 CI dialects + SQLite
  (F3-2-indexes)**: `Table.Indexes` is now populated with
  non-primary-key indexes (`Index{Name, Columns, Unique}`).
  Per-dialect catalogs: **SQLite** `PRAGMA index_list` /
  `PRAGMA index_info` (origin=`pk` filtered);
  **PostgreSQL** `pg_index` / `pg_class` / `pg_attribute` with
  `unnest(indkey) WITH ORDINALITY` for stable column order
  (filter `NOT indisprimary`);
  **MySQL / MariaDB** `INFORMATION_SCHEMA.STATISTICS` grouped
  by `INDEX_NAME` ordered by `SEQ_IN_INDEX` (filter `INDEX_NAME
  != 'PRIMARY'`);
  **MSSQL** `sys.indexes` / `sys.index_columns` / `sys.columns`
  with `is_primary_key = 0 AND type > 0` and
  `is_included_column = 0` to exclude INCLUDE columns.
  Expression / functional indexes surface their expression
  slot as `""` — the diff layer (F3-3) decides whether to
  treat them as opaque.

- **`Client.AcquireMigrationLock(ctx, name, timeout)` — distributed
  migration lock (F3-1)**: cluster-wide advisory lock for migration
  operations. First caller wins; subsequent callers block up to
  `timeout` or receive `ErrLockTimeout`. The lock is held by a
  dedicated connection for its lifetime; `Release` returns it to the
  pool. New optional `MigrationLocker` interface on Dialect — kept
  optional so custom dialects don't break.
  Per-dialect implementation: PG uses session-level
  `pg_advisory_lock(hashtext)` + `SET lock_timeout` (SQLSTATE
  `55P03` → `ErrLockTimeout`); MySQL/MariaDB use `GET_LOCK` +
  `RELEASE_LOCK` (return 0 → `ErrLockTimeout`); MSSQL uses
  `sp_getapplock @LockOwner='Session'` (status -1 →
  `ErrLockTimeout`). SQLite and Oracle return
  `ErrUnsupportedFeature` — SQLite has no distributed primitive,
  Oracle's `DBMS_LOCK` needs PL/SQL plumbing tracked as F3-1
  follow-up. First F3 deliverable closed.

- **`Array[T]` generic** — typed wrapper for SQL columns holding a list of `T`.
  Round-trips through JSON regardless of dialect (same wire format as
  `JSON[T]`; migrate maps to the per-dialect JSON column type). Helpers
  `Len()` / `Slice()` over the underlying `[]T`. Semantically clearer than
  `JSON[[]T]` for list-shaped columns and gives the project a single
  upgrade path if PG-native `INT[]` / `TEXT[]` support lands later.
  Intentionally **not** tied to `pgx`/`pgtype` — neutral-wrapper design
  per TASKS § Bloque B. Inherits the MSSQL JSON Scan skip until the
  NVARCHAR(MAX) encoding bug (F0-8 followup E) is resolved.

[#42]: https://github.com/jcsvwinston/quark/pull/42
[#43]: https://github.com/jcsvwinston/quark/pull/43
[#44]: https://github.com/jcsvwinston/quark/pull/44
[#45]: https://github.com/jcsvwinston/quark/pull/45
[#47]: https://github.com/jcsvwinston/quark/pull/47
[#48]: https://github.com/jcsvwinston/quark/pull/48
[#49]: https://github.com/jcsvwinston/quark/pull/49
[#50]: https://github.com/jcsvwinston/quark/pull/50
[#51]: https://github.com/jcsvwinston/quark/pull/51
[#52]: https://github.com/jcsvwinston/quark/pull/52
[#53]: https://github.com/jcsvwinston/quark/pull/53
[#54]: https://github.com/jcsvwinston/quark/pull/54
[#55]: https://github.com/jcsvwinston/quark/pull/55
[#56]: https://github.com/jcsvwinston/quark/pull/56
[#57]: https://github.com/jcsvwinston/quark/pull/57
[#58]: https://github.com/jcsvwinston/quark/pull/58
[#59]: https://github.com/jcsvwinston/quark/pull/59
[#60]: https://github.com/jcsvwinston/quark/pull/60

## [0.5.0] - 2026-05-13

Phase 0 cleanup release. No new public API — every change is
infrastructure or test-side. Closes the F0-1 through F0-10 backlog
that had been carried since the project's first audit, including the
integration matrix that finally enforces the "tests pass on 6 engines
before merge" rule that was honor-system through v0.4. Full release
notes in [`docs/RELEASE_NOTES_v0.5.0.md`](docs/RELEASE_NOTES_v0.5.0.md).

### Added

- **Integration test matrix via testcontainers-go (F0-8)** — per-engine
  helpers in `containers_test.go` (gated `//go:build integration`) boot
  PostgreSQL, MySQL, MariaDB, MSSQL, and Oracle through testcontainers
  and resolve a DSN with the precedence env var → container. Each
  suite file delegates DSN resolution to `resolve<Engine>DSN(t)`
  instead of reading `os.Getenv` directly. Default
  (`go test -short`) path stays SQLite-only and doesn't import
  testcontainers-go. CI gains an `integration` job with a 4-engine
  matrix (PG / MySQL / MariaDB / MSSQL — Oracle excluded pending the
  image issue; the helper stays for local use) that runs in parallel
  to Lint + SQLite jobs. Docker is pre-installed on `ubuntu-latest`.
  Closes the honor-system state of the "6 motores verdes antes de
  mergear" hard rule — now enforced on 4/5 engines via CI. ([#28],
  [#36])
- **release-please workflow (F0-9)** —
  `.github/workflows/release-please.yml` runs on every push to `main`
  and keeps a rolling Release PR open with the next semver bump
  derived from Conventional Commits and the CHANGELOG entries since
  the last tag. Does NOT automate the Docusaurus `docs:version`
  snapshot — that stays manual via the `/release` slash command
  before merging the release PR. Config in
  `release-please-config.json` + manifest in
  `.release-please-manifest.json`. ([#38])
- **Docs linter (F0-10)** — `scripts/lint-docs.sh` runs in the
  `Lint` CI job. Three checks: anti-marketing language
  (`production-ready` / `enterprise-grade` / `battle-tested`
  rejected unless negated), `RELEASE_NOTES_V1` leak (the deleted
  file may not be referenced), and broken relative links in `*.md` /
  `*.mdx` (Docusaurus-aware: tries `<path>`, `<path>.md`,
  `<path>.mdx`, `<path>/index.{md,mdx}`, and resolves `/docs/...`
  baseUrl-rooted paths). Meta files (CLAUDE.md, TASKS, ADRs, blog,
  versioned_docs) exempt. ([#39])

### Fixed

- **MSSQL setop ORDER BY** — `List()` over a `Union` / `Intersect` /
  `Except` triggered MSSQL's "ORDER BY items must appear in the
  select list" because the auto-injected ORDER BY for OFFSET/FETCH
  referenced the PK column, which isn't in the operand SELECT. The
  fix is test-side: an explicit `OrderBy("email", "ASC")` on the
  base. The Quark SQL was always correct; the assertion was
  SQLite-biased. ([#35])
- **MSSQL JoinBuilder ambiguous id** — `List()` over a `Join`
  between two tables that both expose `id` triggered MSSQL's
  "Ambiguous column name 'id'" on the implicit `SELECT *`. Tests
  switched to `Count()`, which exercises the same ON-clause path
  without projection ambiguity. ([#30], [#35])
- **`having_aggregate` portable shape** — `SELECT * ... GROUP BY`
  is rejected by Postgres / MySQL strict / MSSQL when non-grouped
  columns aren't aggregated. Tests now use explicit
  `Select("status")` to match the GROUP BY clause. ([#30])
- **Float precision in nullable roundtrip** — Postgres maps Go
  `float64` to SQL `real` (32-bit) by default, so the 98.6 fixture
  round-trips to 98.5999984741211. Test switched to a
  `math.Abs(diff) > 1e-4` tolerance. ([#32])
- **Outdated `quark.New(db, ...)` examples on the docs site** —
  the verbose form never existed in the public API. All snippets
  migrated to the real `quark.New(driver, dsn, opts...)` signature
  across `website/docs/`. ([#27])

### Changed

- **CI matrix is now blocking on PG / MySQL / MariaDB / MSSQL** —
  `continue-on-error: true` removed after the F0-8 follow-ups
  closed the 11 test-side bugs the first cross-engine run
  surfaced. A red light on any of those 4 engines now fails the
  PR. Oracle remains excluded until the `gvenzl/oracle-free` image
  issue on hosted runners is resolved. ([#36])

### Documentation

- README cosmetic cleanup (F0-1 through F0-5): outdated
  `examples/blog-api/` references removed; `pkg/quark/examples/`
  heritage paths in `examples/README.md` fixed; duplicate Quick
  Start section deduplicated; coverage badge no longer hardcoded;
  versioned `RELEASE_NOTES_V1.md` no longer referenced. ([#37])
- TASKS header reconciled with the actual state of Phase 0
  (F0-1..F0-10 fully closed, not just the P0 subset). ([#40])

### Tests

- Dialect-aware quote assertions in `expr_ast` / `cte` / `window`
  integration tests via new `q(client, ident)` helper — replaces
  hardcoded `"col"` literals that match SQLite/Postgres quoting
  but not MySQL / MariaDB / MSSQL. ([#29])
- Dialect-skip + mirror-contract assertions in setop tests for
  MySQL / MariaDB where `Intersect` / `Except` return
  `ErrUnsupportedFeature` by design. ([#31])
- Interim skip of `JSON[T]` roundtrip on MSSQL with diagnosis —
  NVARCHAR(MAX) encoding bug; the fix (migrate to
  `VARCHAR(MAX)`) is deferred to a future PR with MSSQL local
  access. ([#33])

[#27]: https://github.com/jcsvwinston/quark/pull/27
[#28]: https://github.com/jcsvwinston/quark/pull/28
[#29]: https://github.com/jcsvwinston/quark/pull/29
[#30]: https://github.com/jcsvwinston/quark/pull/30
[#31]: https://github.com/jcsvwinston/quark/pull/31
[#32]: https://github.com/jcsvwinston/quark/pull/32
[#33]: https://github.com/jcsvwinston/quark/pull/33
[#35]: https://github.com/jcsvwinston/quark/pull/35
[#36]: https://github.com/jcsvwinston/quark/pull/36
[#37]: https://github.com/jcsvwinston/quark/pull/37
[#38]: https://github.com/jcsvwinston/quark/pull/38
[#39]: https://github.com/jcsvwinston/quark/pull/39
[#40]: https://github.com/jcsvwinston/quark/pull/40

## [0.4.0] - 2026-05-10

Phase 2 release: composable query builder. Introduces a typed expression
AST and the structured query primitives (subqueries, CTEs, window
functions, set operators) that build on it, plus a structured Join
builder that retires the v0.3.x string-raw deprecation. Full release
notes in [`docs/RELEASE_NOTES_v0.4.0.md`](docs/RELEASE_NOTES_v0.4.0.md);
breaking-change migration in
[`docs/MIGRATION_v0.4.0.md`](docs/MIGRATION_v0.4.0.md).

### Changed (BREAKING)

- **`Join` / `LeftJoin` / `RightJoin` now return a `*JoinBuilder[T]`**:
  the v0.3.x string-raw form `q.Join(table, onClause)` is replaced by
  the structured `q.Join(table).On(left, op, right)` (or
  `.OnRaw(onClause)` for compound ON clauses that need the legacy
  free-form). Both new methods route through the same
  `guard.ValidateJoinOn` grammar the old form used, so the validation
  surface is identical — only the call shape changed. See
  [`docs/MIGRATION_v0.4.0.md`](docs/MIGRATION_v0.4.0.md) for the
  mechanical rewrite (a `gofmt -r` rule covers it). Closes the v0.2
  deprecation notice.

### Added

- **Set operators via `Union` / `UnionAll` / `Intersect` / `Except`
  (Phase 2)**: any `Query[T]` can be combined with another `Query[T]`
  through the standard SQL compound-select form. Renders flat (no
  parens around operands) — `SELECT ... UNION ALL SELECT ...` — which
  is the only shape SQLite accepts and is portable across PG, MySQL,
  MariaDB, MSSQL, Oracle, SQLite. Dialect-keyword translation lives
  in a package-level `setOpKeyword` helper (kept out of the Dialect
  interface to avoid breaking custom implementations downstream):
  Oracle maps `EXCEPT` to `MINUS`; MySQL/MariaDB return
  `ErrUnsupportedFeature` for `INTERSECT`/`EXCEPT`; SQLite rejects
  `INTERSECT ALL`/`EXCEPT ALL`. Operand restrictions enforced at
  attach time (each surfaces as `ErrUnsupportedFeature`):
  - Operand cannot have `ORDER BY`, `LIMIT`, `OFFSET`, lock options,
    its own CTEs, or nested set-ops.
  - Base cannot have pessimistic locks (the dialect-specific lock
    suffix would bind to the combined result).
  Outer ORDER BY / LIMIT on the base apply to the combined result.

- **Window functions via `SelectExpr` + `Over` / `Window` / `RowNumber` /
  `Rank` / `DenseRank` / `Lag` / `Lead` (Phase 2)**: a typed surface for
  windowed projections that fits inside the AST. `Window` is a
  partition / order specification (`NewWindow().PartitionBy(Col("status")).
  OrderBy(Col("amount"), true)`) — immutable, chain-style. `Over(inner,
  w)` wraps any AST Expr with the OVER clause; the dedicated leaves
  `RowNumber`, `Rank`, `DenseRank`, `Lag(col, offset)`, and `Lead(col,
  offset)` cover the most-used window functions and bypass the function
  whitelist (their syntax is restricted to OVER (...) contexts the
  whitelist doesn't model). The Lag/Lead offset is bound as a parameter,
  not interpolated, so the bind path stays uniform.

  The new `Query[T].SelectExpr(alias, e)` method projects an arbitrary
  AST expression into the SELECT list aliased as `alias` (validated
  through `SQLGuard.ValidateIdentifier`):
  ```go
  q := quark.For[Sale](ctx, c).
      Select("id", "region", "amount").
      SelectExpr("rk", quark.Over(quark.Rank(),
          quark.NewWindow().
              PartitionBy(quark.Col("region")).
              OrderBy(quark.Col("amount"), true)))
  // SELECT "id", "region", "amount",
  //        RANK() OVER (PARTITION BY "region" ORDER BY "amount" DESC) AS "rk"
  // FROM "sales"
  ```
  AST projections compose with regular `Select(cols...)` (comma-joined
  in order). Their bind args land in the args slice between any CTE
  args and the WHERE args, matching the SQL-surface order.

- **CTE support via `With` / `WithRecursive` (Phase 2)**: any
  `*Subquery` can be attached to an outer query as a named CTE. The
  outer SELECT is prefixed with `WITH "name" AS (<inner>)` (or
  `WITH RECURSIVE ...` if any attached entry is recursive), the inner
  args are substituted and prepended to the args slice, and the outer
  WHERE / HAVING argIndex shifts accordingly so dialect placeholders
  ($N / @pN / :N) line up across the CTE-prefix → outer-WHERE
  boundary. The outer query references the CTE by name in JOIN
  clauses (the existing JoinOn grammar already accepts the
  `cte_name.col = parent.col` shape).

  ```go
  topOrders, _ := quark.For[Order](ctx, c).
      Where("amount", ">", 100).
      Select("user_id").
      AsSubquery()

  users, _ := quark.For[User](ctx, c).
      With("top_orders", topOrders).
      Join("top_orders", "users.id = top_orders.user_id").
      List()
  // WITH "top_orders" AS (SELECT "user_id" FROM "orders" WHERE "amount" > $1)
  // SELECT * FROM "users" INNER JOIN "top_orders" ON ...
  ```

  CTE names go through `SQLGuard.ValidateIdentifier`. Recursive CTEs
  emit the dialect-portable `WITH RECURSIVE` keyword; the recursive
  body itself currently requires the user to express the
  `UNION ALL`-shape — full UNION / INTERSECT / EXCEPT support arrives
  in F2-set.

- **Subqueries via `AsSubquery` + `Sub` / `Exists` / `NotExists` /
  `InSub` / `NotInSub` (Phase 2)**: any `Query[T]` can be captured as a
  `*Subquery` and embedded in the AST. The capture eagerly renders the
  inner SELECT (identifier validation, soft-delete predicate, JOINs,
  GROUP BY, HAVING, ORDER BY, LIMIT, lock suffix) using the active
  dialect's identifier quoting but with `?` as the bind marker, so the
  outer query's `buildWhereClause` swaps each `?` for the dialect's
  placeholder syntax at the correct argIndex when the wrapping Expr is
  rendered. Supports the canonical shapes:
  ```go
  // WHERE "id" IN (SELECT "user_id" FROM "orders" WHERE "amount" > ?)
  q.WhereExpr(quark.InSub(quark.Col("id"), sub))
  // WHERE "id" = (SELECT MAX("user_id") FROM "orders")
  q.WhereExpr(quark.Eq(quark.Col("id"), quark.Sub(sub)))
  // WHERE EXISTS (SELECT 1 FROM "orders" WHERE ...)
  q.WhereExpr(quark.Exists(sub))
  ```
  Internally the renderer wraps the active dialect in a `qmarkDialect`
  that delegates everything except `Placeholder`, which always returns
  `?`. So Quote, LimitOffset, and JSONExtract stay dialect-correct.
  Errors during `AsSubquery` (invalid identifier in the inner SELECT,
  or any pessimistic-lock option set on the inner query) propagate to
  the caller; `MustAsSubquery` is the panic-on-error variant for use
  inside expression composition. Pessimistic locks on the inner query
  are rejected with `ErrUnsupportedFeature` because MSSQL emits
  `WITH (UPDLOCK)` inline in the FROM clause — illegal inside an
  `IN (SELECT ...)` context — and the safe pattern is to acquire locks
  on the outer query.

- **Composable expression AST + `WhereExpr` / `HavingExpr` (Phase 2)**: a
  typed expression tree (`Expr` interface, `Col`, `Lit`, `And`, `Or`,
  `Not`, `Cmp`, `Eq`/`Ne`/`Lt`/`Gt`/`Lte`/`Gte`, `In`, `NotIn`, `Func`)
  rendered into the existing where-clause pipeline through `WhereExpr`
  and `HavingExpr`. Identifiers go through `SQLGuard.ValidateIdentifier`
  at every leaf, operators through `SQLGuard.ValidateOperator`, and
  function names against a conservative 10-name whitelist (`COUNT`,
  `SUM`, `AVG`, `MIN`, `MAX`, `LOWER`, `UPPER`, `LENGTH`, `COALESCE`,
  `ABS`). The AST emits `?` as a neutral bind marker; the existing
  `substitutePathMarkers` helper swaps each `?` for the dialect's
  placeholder syntax at render time, so the same AST renders correctly
  against PostgreSQL `$N`, MSSQL `@pN`, Oracle `:N`, MySQL/SQLite `?`
  without per-dialect indexing arithmetic in user code. Closes the gap
  where deep `(A OR (B AND C))` predicates required `RawQuery`.

- **Nested Preload via dotted paths (Phase 2)**: `Preload("Orders.Items.Product")`
  now walks the dotted path and loads each level in a single eager-loading
  pass. Multiple paths sharing a prefix are merged via `parsePreloads` so
  `Preload("Posts", "Posts.Comments")` only loads `Posts` once. Internally
  the per-relation loaders moved from `Query[T]` to `BaseQuery` and now
  accept the parent slice as a `reflect.Value`, so the recursive descent
  doesn't need a generic instantiation per level.

- **`HavingAggregate(fn, column, op, value)` (Phase 2)**: structured way to
  write `HAVING COUNT(*) > 5` / `HAVING SUM(amount) >= 100` / `HAVING
  AVG(price) < ?` etc. without falling back to `RawQuery`. Closes the
  historical limitation where the existing `Having(column, op, value)`
  validated `column` through `SQLGuard.ValidateIdentifier` and therefore
  rejected anything containing parentheses (i.e. every aggregate). The
  function name is whitelisted (`COUNT`, `SUM`, `AVG`, `MIN`, `MAX`,
  case-insensitive); the column is validated through the guard, except
  for `*` which is only allowed with `COUNT`. The fully composable form
  `Having(Func("count", Col("*")), ">", 5)` arrives with the rest of the
  Phase 2 AST.

- **Pessimistic locking (Phase 2)**: `Query[T].ForUpdate()`, `ForShare()`,
  `SkipLocked()`, `NoWait()` modifiers. The dialect emits the right shape:
  `FOR UPDATE [SKIP LOCKED|NOWAIT]` / `FOR SHARE` for PG, MySQL, MariaDB,
  and Oracle (Oracle has no `FOR SHARE` and returns `ErrUnsupportedFeature`
  for it); MSSQL emits table hints (`WITH (UPDLOCK, ROWLOCK [, READPAST])`)
  in the FROM clause; SQLite returns `ErrUnsupportedFeature` for any
  non-zero lock option (use `BEGIN IMMEDIATE` in the transaction instead).
  New error sentinel `ErrUnsupportedFeature` for these dialect-feature
  gates.

- **`Dialect.LockSuffix(LockOptions) (tableHint, suffix string, err error)`**:
  new interface method consumed by `buildSelect` to attach pessimistic-lock
  fragments to the SELECT in the right placement per dialect. Custom
  dialects must implement it.

### Fixed

- **Eager-loading paths now chunk parent keys (Phase 2)**: `Preload` over a
  large parent set used to assemble a single `IN(...)` clause with one
  bind per parent — silently broken on Oracle (1000-IN cap) and at risk on
  SQL Server (~2100 bind ceiling). The three relation loaders
  (`loadStandardRelation`, `loadM2MRelation`, `loadPolymorphicRelation`)
  now chunk at 1000 keys per query and aggregate results across chunks
  via a new internal `chunkParentKeys` helper. Tenant predicates and
  polymorphic-type discriminators are re-applied per chunk so the
  invariant survives the iteration.

## [0.3.0] - 2026-05-10

First proper tag since `v0.1.1`. Bundles Phase 0 P0 fixes (security, correctness)
with the Phase 1 deliverables (rich types, dirty tracking, optimistic locking,
soft-delete scopes). Full release notes in
[`docs/RELEASE_NOTES_v0.3.0.md`](docs/RELEASE_NOTES_v0.3.0.md). Migration
steps for breaking changes in
[`docs/MIGRATION_v0.3.0.md`](docs/MIGRATION_v0.3.0.md).

### Added

- **`JSON[T any]` generic + `[]byte` BLOB mapping (Phase 1 F1-2)**:
  `quark.JSON[T]` is a typed wrapper that round-trips a Go value through a
  SQL JSON column via `encoding/json`. It implements
  [`sql.Scanner`](https://pkg.go.dev/database/sql#Scanner) and
  [`driver.Valuer`](https://pkg.go.dev/database/sql/driver#Valuer) directly,
  so the round-trip uses the standard library's plumbing — no extra reflect
  in Quark's hot paths. The migrate layer detects `JSON[T]` and emits the
  dialect-native column type:
  Postgres `JSONB`; MySQL/MariaDB `JSON`; SQLite `TEXT` (with `json_*`
  functions still available); SQL Server `NVARCHAR(MAX)`; Oracle `CLOB`.
  Pair with `Nullable[JSON[T]]` when you need to distinguish SQL NULL from
  an empty payload. The migrate layer also learned to map `[]byte` to the
  dialect-native binary column (`BYTEA` on Postgres, `VARBINARY(MAX)` on
  SQL Server, `BLOB` elsewhere) instead of the previous `TEXT` fallback.

- **`Nullable[T]` generic (Phase 1 F1-3)**: re-export of `database/sql.Null[T]`
  under a Quark-friendly name, plus the constructors `SomeOf(v)` /
  `NullOf[T]()`. Replaces the long-standing `*time.Time` / `sql.NullString`
  pointer-as-nullable hacks with `Nullable[time.Time]` / `Nullable[string]`
  while keeping the same Scanner+Valuer round-trip plumbing the standard
  library already provides. The migrate layer detects `Nullable[T]` and
  emits T's SQL type for the column, so a model that previously needed a
  custom mapper now Just Works (`Nullable[int64]` → BIGINT,
  `Nullable[time.Time]` → TIMESTAMP / DATETIME / DATETIME2 per dialect).

- **Soft-delete scopes `WithTrashed()` / `OnlyTrashed()` and `Restore` (Phase 1 F1-5)**:
  the existing automatic `deleted_at IS NULL` filter now has two named
  escape hatches: `WithTrashed()` returns both live and trashed rows
  (alias of `Unscoped`, kept for backward compatibility), and
  `OnlyTrashed()` flips the predicate to `deleted_at IS NOT NULL` so
  callers can list only the trash. Both modifiers propagate through
  `clone()`. New `Query[T].Restore(entity)` method clears `deleted_at`
  on the row identified by the entity's PK; the SQL includes
  `AND deleted_at IS NOT NULL` so a Restore on a live row is a 0-row
  no-op (no stealth NULL write). Tenant predicate from the loading
  query is preserved on `Restore`. The default scope, `Count`, and
  aggregates all consult a new centralised `softDeletePredicate`
  helper so the three call sites stay in lock-step.

- **Optimistic locking via `quark:"version"` (Phase 1 F1-6)**: tagging a numeric
  field with `quark:"version"` enables row-level optimistic-locking on
  `Update` / `UpdateFields` / `Tracked.Save`. Each successful update emits
  `SET ..., version = version + 1 WHERE pk = ? AND version = <loaded>` and
  bumps the entity's in-memory version. When the predicate doesn't match
  (another writer already advanced the column) the call returns the new
  sentinel `ErrStaleEntity` without writing. Pairs naturally with the
  Phase-1 dirty-tracking pipeline: a `Tracked.Save` after a no-op mutation
  is still a no-op (the version is not bumped on its own).

- **`ErrStaleEntity`** sentinel for optimistic-locking conflicts (F1-6).

- **`RegisterTypeMapper(reflect.Type, TypeMapper)` (Phase 1 F1-4)**: extensible
  Go-type → SQL-type mapping for `client.Migrate` and `client.Sync`. Custom
  types (decimal.Decimal, uuid.UUID, IP addresses, vector types, …) can plug
  their own DDL emission without forking Quark. Pointer types are stripped
  before registration so registering for `time.Duration` also covers
  `*time.Duration`. The migrate layer also accepts new sizing options on the
  db tag — `db:"name,size=512"`, `db:"price,precision=18,scale=4"` — that
  flow into `TypeOptions` and are propagated to mappers and to the built-in
  VARCHAR/DECIMAL emitters. As the canonical example, Quark now ships with
  `time.Duration` registered to `BIGINT` (or `NUMBER(19)` on Oracle) so
  `Duration` columns stop falling back to `TEXT`.

- **Dirty tracking ligero (Phase 1)**: new `Query[T].Track()` modifier returns
  a `*TrackedQuery[T]` whose `Find` / `First` / `List` yield `*Tracked[T]`
  wrappers carrying a column-value snapshot taken at load time. Calling
  `Tracked.Save(ctx)` emits an UPDATE that touches only the columns whose
  values actually differ from the snapshot — and writes them whether they
  are zero or non-zero. This is the permanent fix for the P0-4 zero-value
  trap: `tracked.Entity.Active = false; tracked.Save(ctx)` writes `false`
  to the database without the caller resorting to `UpdateFields` or
  `UpdateMap`. `Tracked.Changed()` exposes the changed column list for
  tests and observability. The snapshot lives on the wrapper, not in the
  Client, so there is no shared map to grow or evict; tenant predicates
  from the loading query are propagated to Save's WHERE clause; the
  primary-key column and the configured tenant column are never written
  even if the caller mutates them on the entity.

### Security

- **`JOIN ... ON` clause concatenated raw (P0-5)**: `Join`/`LeftJoin`/
  `RightJoin` accepted the `on` argument as an opaque string and emitted it
  verbatim into the SELECT/Count SQL with no validation — an inconsistency
  with `WHERE` (which already validated identifiers) and an injection vector
  if the `on` came from dynamic input. Fixed: `internal/guard.ValidateJoinOn`
  enforces the minimal grammar `[ident.]ident OP [ident.]ident
  ((AND|OR) [ident.]ident OP [ident.]ident)*` (operators
  `=`, `!=`, `<>`, `<`, `<=`, `>`, `>=`; max 512 chars). Both call sites
  (`buildSelect` and `Count`) now reject malformed clauses with the new
  sentinel `ErrInvalidJoin`. The string-raw signature is marked deprecated
  in godoc; the structured `Join(table).On(col, op, otherCol)` builder is
  scheduled for v0.4 (Phase 2 AST). Regression: `testJoinOnSecurity` wired
  into the shared suite — valid identifier joins, valid AND-joined clauses,
  8 injection vectors rejected, and a Count-path check.

### Added

- **`ErrInvalidJoin`** sentinel for malformed `Join`/`LeftJoin`/`RightJoin`
  ON clauses (P0-5).

- **`UpdateFields(entity, fields ...string)` API (P0-4 escape hatch)**:
  explicit partial-update method that writes only the named fields, bypassing
  the zero-value filter `Update(entity)` applies. Recommended path for
  writing `false`, `0`, `""`, or `nil` until dirty tracking lands in Phase 1.
  Refuses to overwrite the primary key, errors on unknown field names or an
  empty list. Hooks `BeforeUpdate`/`AfterUpdate` still run.

### Changed

- **`Update(entity)` logs a WARN when it skips zero-value fields**, listing
  the skipped column names. Lets users see the P0-4 trap instead of having
  values silently disappear. The behaviour itself does not change.

### Fixed

- **Silent error swallowing in `linkM2M` (P0-3)**: when Quark inserted into a
  many-to-many join table, every driver error was returned as `nil` under the
  comment `// Ignore duplicate key errors - already linked`. The intent was
  to keep re-linking idempotent for unique-key violations, but the
  implementation masked foreign-key violations, missing tables, broken
  connections, and any other failure as success. Fixed: only real unique-key
  violations (PG SQLSTATE 23505, MySQL 1062, MSSQL 2627/2601, Oracle ORA-00001,
  SQLite extended codes 2067/1555 — both mattn and modernc drivers) are now
  treated as idempotent; everything else is wrapped with `wrapDBError` and
  propagated. Added `testM2MLinkErrors` to the shared suite (idempotent
  re-link + missing-join-table propagation). No public API change.

### Security

- **`WhereJSON` SQL injection via path interpolation (P0-2)**: every dialect's
  `JSONExtract` was building the SQL with `fmt.Sprintf("'%s'", path)` (or the
  Postgres `->>'%s'` equivalent), so a path containing a single quote either
  broke the SQL or could be weaponised when the path came from user input.
  Fixed in two layers: (1) the path is now bound as a parameter in every
  dialect — Postgres uses `jsonb_extract_path_text(col, VARIADIC text)` with
  one bind per segment, the rest use `JSON_EXTRACT`/`JSON_VALUE(col, ?)` with
  the `$.path` form; (2) `internal/guard.ValidateJSONPath` enforces the
  grammar `^[a-zA-Z_][a-zA-Z0-9_]*(\.[a-zA-Z_][a-zA-Z0-9_]*)*$` (max 256
  chars) and is called from each dialect before the bind. Invalid paths now
  return `ErrInvalidJSONPath` (new sentinel) at execution time.
  **Breaking**: `Dialect.JSONExtract` signature changed from
  `(column, path string) string` to
  `(column, path string) (sql string, args []any, err error)`. Custom
  dialects registered via `RegisterDialect` must update.
  Regression test `testJSONPathSecurity` wired into the shared suite covers
  valid paths (asserts the path is in bind args, never in the SQL surface)
  and 8 injection vectors (quotes, semicolons, comments, leading `$`, dashes,
  whitespace, empty).

- **Tenant isolation leak in `Or()` under `RowLevelSecurity` (P0-1)**: an `Or(...)`
  group used to be built on a fresh `BaseQuery` that did not carry the active
  `tenantID` / `tenantCol`. Combined with SQL operator precedence
  (`A AND B OR C` parses as `(A AND B) OR C`), the OR branch escaped the outer
  `tenant_id = ?` predicate and could return rows from other tenants. Fixed by
  introducing an internal `BaseQuery.cloneForGroup()` helper that propagates
  isolation/context state to the callback's blank query and pre-injects the
  tenant predicate into the OR group, so the rendered SQL becomes
  `WHERE tenant_id=? AND ... OR (tenant_id=? AND ...)`. Added a regression
  test (`testOrRLSLeak`) wired into the shared multi-engine suite that fails
  before the fix and passes after, including a nested-`Or` variant.
  No public API change.

### Changed

- **`Dialect.JSONExtract` signature** is now
  `(column, path string) (sql string, args []any, err error)` (was
  `(column, path string) string`). Required to bind the path as a parameter
  for P0-2. Custom dialects registered via `RegisterDialect` must update.

## [0.1.1] - 2026-05-06

### Breaking Changes

- **Client Creation API**: Changed `quark.New()` signature from `New(db *sql.DB, opts ...Option)` to `New(driverName, dataSource string, opts ...any)`
  - The function now accepts a driver name and data source string instead of a `*sql.DB` instance
  - `sql.Open()` is now called internally by `New()`
  - Dialect is now auto-detected from the driver name, removing the need for explicit `WithDialect()` option
  - Connection pool options (`WithMaxOpenConns`, `WithMaxIdleConns`, `WithConnMaxLifetime`, `WithConnMaxIdleTime`) are now applied during client creation

- **Removed Options**: 
  - `WithDialect()` option is no longer needed as dialect is auto-detected from driver name
  - Passing `*sql.DB` directly to `New()` is no longer supported

### Added

- **New Client Method**: Added `WithOptions(opts ...any) (*Client, error)` method to `Client` for recreating clients with different options without exposing the underlying `*sql.DB`
- **Connection Pool Options**: Added pool configuration options:
  - `WithMaxOpenConns(maxOpenConns int)` - Sets maximum number of open connections
  - `WithMaxIdleConns(maxIdleConns int)` - Sets maximum number of idle connections
  - `WithConnMaxLifetime(d time.Duration)` - Sets maximum connection lifetime
  - `WithConnMaxIdleTime(d time.Duration)` - Sets maximum idle connection time

### Migration Guide

**Old API:**
```go
db, err := sql.Open("sqlite", ":memory:")
if err != nil {
    log.Fatal(err)
}
defer db.Close()

client, err := quark.New(db, quark.WithDialect(quark.SQLite()))
```

**New API:**
```go
client, err := quark.New("sqlite", ":memory:")
if err != nil {
    log.Fatal(err)
}
defer client.Close()
```

**Recreating client with different options:**

**Old API:**
```go
newClient, err := quark.New(client.Raw(), quark.WithLimits(newLimits))
```

**New API:**
```go
newClient, err := client.WithOptions(quark.WithLimits(newLimits))
```

### Supported Drivers for Auto-Detection

- `"sqlite"`, `"sqlite3"`, `"modernc"` → SQLite
- `"postgres"`, `"pgx"`, `"pgx/v5"`, `"pq"` → PostgreSQL
- `"mysql"` → MySQL
- `"mariadb"` → MariaDB
- `"mssql"`, `"sqlserver"`, `"azuresql"` → MSSQL
- `"oracle"`, `"godror"`, `"oci8"` → Oracle

## [0.1.0] - Previous Release

Initial release
