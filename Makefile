# Makefile de quark — el punto de entrada único que CONTRIBUTING promete.
# `make check` reproduce las lanes baratas de CI en local; las caras (matriz
# de motores, superapp all-engines) tienen target propio con su coste dicho.
#
# El árbol son varios módulos de Go (ADR-0023, ADR-0024): la biblioteca en la
# raíz, el CLI en cmd/quark, los cinco drivers, las suites por motor en
# internal/enginesuite, las fixtures de integración en internal/integrations,
# la plantilla de driver en internal/drivertemplate y los arneses. `go build
# ./...` desde la raíz sólo ve el primero, así que los targets de aquí entran
# en cada módulo.

.DEFAULT_GOAL := help

# Los módulos que se construyen y testean junto a la raíz.
NESTED_MODULES := cmd/quark internal/enginesuite acceptance

# Las fixtures de integración (chi, Echo, Gin, gRPC, Nucleus) se comprueban SIN workspace,
# como las corre la lane "Integration fixtures" de CI y como las mide el banco
# de extensibilidad: su go.mod reemplaza quark por este árbol, y meterlas en el
# go.work subiría las dependencias de los frameworks a todos los módulos de él.
# La plantilla de driver (internal/drivertemplate, A11 Q5) va igual: se construye
# como la construiría un tercero, sin workspace, y su path no es el del repo.
FIXTURE_MODULES := internal/integrations internal/drivertemplate

.PHONY: help check lint test test-race test-all fuzz docs-guards regen superapp oracle-up workspace

help: ## Lista los targets con su descripción
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

workspace: ## Escribe el go.work local (ignorado por git) que enlaza el CLI con este árbol
	@# El CLI requiere la biblioteca y los drivers POR VERSIÓN y no puede
	@# llevar un replace: `go install` rechaza un módulo publicado que lo
	@# tenga (ADR-0024). El workspace es lo que lo apunta a este árbol, aquí
	@# y en CI. Los demás módulos anidados sí llevan replace y no lo
	@# necesitan; entran igual para que un `go build ./...` desde cualquiera
	@# vea lo mismo. El fichero se DERIVA de los require, así que se
	@# reescribe en cada invocación en vez de respetar el que hubiera.
	@bash scripts/ci/link_workspace.sh . $(NESTED_MODULES) \
		drivers/postgres drivers/mysql drivers/sqlite drivers/mssql drivers/oracle
	@echo "go.work listo (local, gitignored)"

check: workspace lint docs-guards ## Las lanes baratas de CI: vet+gofmt, guards de docs, coherencia, pines de acciones, superficie fresca, builds estáticos, tests unit
	bash scripts/check-version-coherence.sh
	bash scripts/check-version-coherence.sh --self-test
	bash scripts/release/gen_release_notes_skeleton.sh --self-test
	bash scripts/ci/check_action_pins.sh
	cd acceptance && go run ./cmd/gen-apisurface && go run ./cmd/gen-allowlist
	@git diff --quiet acceptance/apisurface.json acceptance/allowlist.json || \
		{ git --no-pager diff -U3 acceptance/apisurface.json acceptance/allowlist.json | head -n 400; \
		  echo "apisurface/allowlist rancios: commitea la regeneración (make regen)"; exit 1; }
	CGO_ENABLED=0 go build ./...
	GOOS=linux GOARCH=arm64 go build ./...
	cd cmd/quark && CGO_ENABLED=0 go build ./...
	cd cmd/quark && GOOS=linux GOARCH=arm64 go build ./...
	go test ./... -count=1 -timeout 5m
	@for m in $(NESTED_MODULES); do echo "== go test $$m"; (cd $$m && go test ./... -count=1 -timeout 15m) || exit 1; done
	@for m in $(FIXTURE_MODULES); do echo "== go test $$m (sin workspace)"; (cd $$m && GOWORK=off go mod tidy -diff && GOWORK=off go test ./... -count=1 -timeout 10m) || exit 1; done
	@echo "check OK — lanes caras aparte: make test-race, make test-all, make superapp"

lint: workspace ## go vet + gofmt (lo que corre la lane Lint de CI)
	go vet ./...
	@for m in $(NESTED_MODULES); do echo "== go vet $$m"; (cd $$m && go vet ./...) || exit 1; done
	@for m in $(FIXTURE_MODULES); do echo "== go vet $$m (sin workspace)"; (cd $$m && GOWORK=off go vet ./...) || exit 1; done
	@fmt=$$(gofmt -l .); if [ -n "$$fmt" ]; then echo "gofmt:"; echo "$$fmt"; exit 1; fi

docs-guards: ## Los guards de docs de CI (voz de producto, deriva, archivo, marcadores, lint)
	bash scripts/ci/check_docs_product_voice.sh
	bash scripts/ci/check_internal_docs_drift.sh
	bash scripts/ci/check_docs_archive_freshness.sh
	bash scripts/ci/check_versioned_docs_markers.sh
	bash scripts/lint-docs.sh

test: workspace ## Tests de la biblioteca, de las suites por motor, de las fixtures de integración y de la plantilla de driver (los de Redis se saltan sin QUARK_TEST_REDIS_ADDR)
	go test ./... -count=1 -timeout 5m
	cd internal/enginesuite && go test ./... -count=1 -timeout 15m
	cd internal/integrations && GOWORK=off go test ./... -count=1 -timeout 10m
	cd internal/drivertemplate && GOWORK=off go test ./... -count=1 -timeout 10m

test-race: workspace ## La lane -race de CI (~5 min)
	go test -race -short -count=1 -timeout 15m ./...
	cd internal/enginesuite && go test -race -short -count=1 -timeout 20m ./...

fuzz: ## La lane de fuzzing corta de CI sobre las superficies de parseo (~45 s). FUZZTIME=2m para una tanda larga
	bash scripts/ci/fuzz-short.sh $(FUZZTIME)

test-all: workspace ## Matriz completa: exporta los QUARK_TEST_*_DSN de los motores que tengas (Oracle: make oracle-up). Sin DSN, esa lane se salta.
	go test ./... -count=1 -timeout 25m
	cd internal/enginesuite && go test -tags=integration ./... -count=1 -timeout 25m

superapp: workspace ## Aceptación del superapp con gate estricto en sqlite (all-engines: ~45 min con 6 contenedores, ver ci.yml)
	cd acceptance && go run . -engines=sqlite -gate=strict

regen: workspace ## Regenera apisurface.json y allowlist.json EN ESTE ORDEN (allowlist lee apisurface) y las copias de las fixtures que escribe `quark init --with`
	cd acceptance && go run ./cmd/gen-apisurface
	cd acceptance && go run ./cmd/gen-allowlist
	@# `quark init --with` escribe copias byte a byte de internal/integrations
	@# (cmd/quark/commands/templates/integrations); TestInitWithTemplatesAreTheFixtures
	@# falla en `make check` cuando una copia y su fixture difieren.
	cd cmd/quark && QUARK_SYNC_TEMPLATES=1 go test ./commands -run '^TestInitWithTemplatesAreTheFixtures$$' -count=1

oracle-up: ## Arranca el Oracle de la matriz con el mismo bootstrap que CI (readiness + GRANT DBMS_LOCK)
	bash scripts/ci/oracle-up.sh
