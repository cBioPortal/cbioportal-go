# Common tasks. Everything here is plain go/bash; see README.md for the stack.

CBIOPORTAL_DB ?= cbioportal
PORT ?= 8080
API ?= http://localhost:18090

.PHONY: build test fmt vet lint contract e2e run frontend testdb clean

build: ## build bin/cbioportal-import and bin/cbioportal-api
	go build -o bin/cbioportal-import ./cmd/cbioportal-import
	go build -o bin/cbioportal-api ./cmd/cbioportal-api

test: ## unit tests (no services needed)
	go test ./internal/...

fmt: ## format the code
	gofmt -w cmd internal test

vet:
	go vet ./...

lint: vet ## gofmt check + go vet (what CI runs)
	@out=$$(gofmt -l cmd internal test); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

contract: ## API contract tests against a running API (API=...)
	API=$(API) go test ./test/

e2e: ## frontend Playwright suite against the API on :18090 (WORKERS=3)
	test/run_e2e.sh $(or $(WORKERS),3)

testdb: ## build the test databases: make testdb FIXTURES=<cbioportal-test> SEED=<seed.sql.gz>
	test/setup_test_db.sh $(FIXTURES) $(SEED)

frontend: ## build the frontend submodule into frontend/dist
	git submodule update --init
	cd frontend && pnpm install --frozen-lockfile && pnpm run buildAll

run: build ## run the portal (UI + API) on $(PORT) against $(CBIOPORTAL_DB)
	FRONTEND_DIR=frontend/dist PORT=$(PORT) CBIOPORTAL_DB=$(CBIOPORTAL_DB) bin/cbioportal-api

clean:
	rm -rf bin

help: ## list targets
	@grep -E '^[a-z0-9]+:.*## ' $(MAKEFILE_LIST) | sed 's/:.*## /\t/'
