# Changelog

## 1.0.0 (2026-09-10)


### Added

* **cli:** quark init --with nucleus writes the Nucleus module and nucleus.yml, with chi, echo and gin examples ([3bc866c](https://github.com/jcsvwinston/quark/commit/3bc866c746b16295b99785b924282cd4fe2d11c7))
* **codegen:** generated INSERT binder on the write path (F6-3a) ([550c13f](https://github.com/jcsvwinston/quark/commit/550c13f875d529227d2f364d590d7f931a1b8319))
* **codegen:** generated typed scanners on the read path (F6-2) ([9fcc3db](https://github.com/jcsvwinston/quark/commit/9fcc3dbd681ec4ff9e98a361a64c1b9b9e7c1302))
* **codegen:** quark gen + typed-registry contract (F6-1) ([#99](https://github.com/jcsvwinston/quark/issues/99)) ([ce85abc](https://github.com/jcsvwinston/quark/commit/ce85abc94fc68f61d9661f80d724f6815e8a19f0))
* **codegen:** typed compile-time column accessors (F6-4) ([#105](https://github.com/jcsvwinston/quark/issues/105)) ([34ea945](https://github.com/jcsvwinston/quark/commit/34ea945e70a0be5f417bf247e08e73fca2f2bd40))
* **drivers:** los drivers salen a módulos propios y Quark deja de cargar sus tipos de error ([#312](https://github.com/jcsvwinston/quark/issues/312)) ([31a2052](https://github.com/jcsvwinston/quark/commit/31a2052bf451ff943ed8b221e56152fcbf2b1d4f))
* **dx:** arco DX de quark P0+P1 — build estático, tags fail-fast, PK accionable, runner en init (DX-1/5/6/7/8/9/10) ([#276](https://github.com/jcsvwinston/quark/issues/276)) ([044820d](https://github.com/jcsvwinston/quark/commit/044820d3a8f2f5d6248d25400260f5d59286cd8a))
* **migrate:** the migrator takes the schema lock by default, logs through the client and runs transactional migrations; the listener contract is public ([#350](https://github.com/jcsvwinston/quark/issues/350)) ([e99d9be](https://github.com/jcsvwinston/quark/commit/e99d9beda5339cbaf5c0242ce1410ee646a5983e))
* **packaging:** the CLI, the acceptance harness and the engine suites move to modules of their own ([#377](https://github.com/jcsvwinston/quark/issues/377)) ([86b0c90](https://github.com/jcsvwinston/quark/commit/86b0c903bcc75223e4541c822465f41ec036f6b6))


### Fixed

* **cli:** commands.Main() y receta de embebido que propaga errores (QCD-CLI-2) ([#271](https://github.com/jcsvwinston/quark/issues/271)) ([fdedd2f](https://github.com/jcsvwinston/quark/commit/fdedd2f4a49d905f019620aa5f25fb2324c3f687))
* **cli:** migrate up sin --steps aplicaba solo la primera migración pendiente ([#281](https://github.com/jcsvwinston/quark/issues/281)) ([c29abf4](https://github.com/jcsvwinston/quark/commit/c29abf4b81932cd3f68ed1f476c8cd089d65edfd))
* **cli:** model generate --fields compila y declara PK (QCD-CLI-1) ([#270](https://github.com/jcsvwinston/quark/issues/270)) ([37d0179](https://github.com/jcsvwinston/quark/commit/37d017955017eb8c2e9deec39640fa523473934e))
* **cli:** model generate creates --out and reports failures as non-zero exit ([002d996](https://github.com/jcsvwinston/quark/commit/002d996f5b1c9088a08a8134f0bc5f6769fd9d90))
* **cli:** no-ops honestos, validate real, migración por tenant y flags fantasma fuera (QK-P1-1/2/3/5, QK-P2-1/4/5) ([#245](https://github.com/jcsvwinston/quark/issues/245)) ([03850aa](https://github.com/jcsvwinston/quark/commit/03850aa5b0ff6a530cc40839c8f28f4ecee0bfde))
* **cli:** papercuts v1.4.1 — status con pendientes, seeders en orden, init lee go.mod, RLS re-ejecutable ([#274](https://github.com/jcsvwinston/quark/issues/274)) ([9856348](https://github.com/jcsvwinston/quark/commit/9856348fcfba9005985644b72bb65819f32ef2c2))
* **cli:** sanea el primer contacto del CLI y valida identificadores ([#309](https://github.com/jcsvwinston/quark/issues/309)) ([cb5a5ad](https://github.com/jcsvwinston/quark/commit/cb5a5ad3d9f4b76105cd555621ac4316db28e9d1))
* **cli:** tenant provision completa bajo schema_per_tenant e idempotente (QCD-CLI-3) ([#272](https://github.com/jcsvwinston/quark/issues/272)) ([adde5e3](https://github.com/jcsvwinston/quark/commit/adde5e3a8d076c07d06ba45fd0556a7819541d3b))
* **cli:** working init defaults, real exit codes, honest sync, version cmd ([#237](https://github.com/jcsvwinston/quark/issues/237)) ([4afdd65](https://github.com/jcsvwinston/quark/commit/4afdd65855ebe2b927a6256c9d131fd5f1f42ccf))
* **deps:** raise the sibling floors, and name in the CLI the root this train cuts ([#382](https://github.com/jcsvwinston/quark/issues/382)) ([834f5b9](https://github.com/jcsvwinston/quark/commit/834f5b935a2e48258c8290e2ea099a9ee1873867))
* P0 backlog — tenant-provision SQL injection, compound Count, empty-conflict Upsert, offset-only LIMIT (QK-P0-1..4) ([#242](https://github.com/jcsvwinston/quark/issues/242)) ([d779e2b](https://github.com/jcsvwinston/quark/commit/d779e2b5794e563d810a9af4dde95d64d0edea03))
* **quarkdriver:** accept drivers registered under their alias, warn without a classifier, and align docs with driver modules ([#338](https://github.com/jcsvwinston/quark/issues/338)) ([0b9dc6d](https://github.com/jcsvwinston/quark/commit/0b9dc6d9d63d33626a955d365e4431eeb67f3c8e))
