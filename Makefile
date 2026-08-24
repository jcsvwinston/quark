# Makefile de quark — el punto de entrada único que CONTRIBUTING promete.
# `make check` reproduce las lanes baratas de CI en local; las caras (matriz
# de motores, superapp all-engines) tienen target propio con su coste dicho.

.DEFAULT_GOAL := help

.PHONY: help check lint test test-race test-all docs-guards regen superapp oracle-up

help: ## Lista los targets con su descripción
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-12s\033[0m %s\n", $$1, $$2}'

check: lint docs-guards ## Las lanes baratas de CI: vet+gofmt, guards de docs, coherencia, superficie fresca, builds estáticos, tests unit
	bash scripts/check-version-coherence.sh
	go run ./examples/superapp/cmd/gen-apisurface && go run ./examples/superapp/cmd/gen-allowlist
	@git diff --quiet examples/superapp/apisurface.json examples/superapp/allowlist.json || \
		{ echo "apisurface/allowlist rancios: commitea la regeneración (make regen)"; exit 1; }
	CGO_ENABLED=0 go build ./...
	GOOS=linux GOARCH=arm64 go build ./...
	go test ./... -count=1 -timeout 5m
	@echo "check OK — lanes caras aparte: make test-race, make test-all, make superapp"

lint: ## go vet + gofmt (lo que corre la lane Lint de CI)
	go vet ./...
	@fmt=$$(gofmt -l .); if [ -n "$$fmt" ]; then echo "gofmt:"; echo "$$fmt"; exit 1; fi

docs-guards: ## Los tres guards de docs de CI (voz de producto, lint, roadmap)
	bash scripts/ci/check_docs_product_voice.sh
	bash scripts/ci/check_internal_docs_drift.sh
	bash scripts/ci/check_docs_archive_freshness.sh
	bash scripts/lint-docs.sh

test: ## Tests del módulo raíz (los de Redis se saltan sin QUARK_TEST_REDIS_ADDR)
	go test ./... -count=1 -timeout 5m

test-race: ## La lane -race de CI (~5 min)
	go test -race -short -count=1 -timeout 15m ./...

test-all: ## Matriz completa: exporta los QUARK_TEST_*_DSN de los motores que tengas (Oracle: make oracle-up). Sin DSN, esa lane se salta.
	go test ./... -count=1 -timeout 25m

superapp: ## Aceptación del superapp con gate estricto en sqlite (all-engines: ~45 min con 6 contenedores, ver ci.yml)
	go run ./examples/superapp -engines=sqlite -gate=strict

regen: ## Regenera apisurface.json y allowlist.json EN ESTE ORDEN (allowlist lee apisurface)
	go run ./examples/superapp/cmd/gen-apisurface
	go run ./examples/superapp/cmd/gen-allowlist

oracle-up: ## Arranca el Oracle de la matriz con el mismo bootstrap que CI (readiness + GRANT DBMS_LOCK)
	bash scripts/ci/oracle-up.sh
