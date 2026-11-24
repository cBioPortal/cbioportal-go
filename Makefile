# Common tasks. Everything here is plain go/bash; see README.md for the stack.

CBIOPORTAL_DB ?= cbioportal
PORT ?= 8080
API ?= http://localhost:18090

.PHONY: build local local-macos macos-app test fmt vet lint contract e2e run frontend testdb oncotree clean help

build: ## build the programs into bin/
	go build -o bin/cbioportal-import ./cmd/cbioportal-import
	go build -o bin/cbioportal-api ./cmd/cbioportal-api
	go build -o bin/cbioportal-ops ./ops/cbioportal.org/cmd/cbioportal-ops
	go build -o bin/cbioportal-rewrite-test ./rewrite/cmd/cbioportal-rewrite-test
	CGO_ENABLED=0 go build -o bin/cbioportal-local ./cmd/cbioportal-local

local: ## bin/cbioportal-local with the website built in (needs frontend/dist)
	test -f frontend/dist/index.html || { echo "build the frontend first (make frontend)"; exit 1; }
	tar -czf cmd/cbioportal-local/frontend.tar.gz -C frontend/dist .
	CGO_ENABLED=0 go build -tags embedfrontend -o bin/cbioportal-local ./cmd/cbioportal-local

# A macOS app per processor: the program, the script that starts it, and
# the app's Info.plist, zipped. Opened without a folder, the program shows
# its import page in the browser.
local-macos: ## bin/cBioPortal-Local-macos-{arm64,amd64}.zip: double-clickable macOS apps (needs frontend/dist)
	test -f frontend/dist/index.html || { echo "build the frontend first (make frontend)"; exit 1; }
	tar -czf cmd/cbioportal-local/frontend.tar.gz -C frontend/dist .
	$(MAKE) macos-app ARCH=arm64
	$(MAKE) macos-app ARCH=amd64

macos-app:
	mkdir -p "bin/macos-$(ARCH)/cBioPortal Local.app/Contents/MacOS"
	GOOS=darwin GOARCH=$(ARCH) CGO_ENABLED=0 go build -tags embedfrontend -o "bin/macos-$(ARCH)/cBioPortal Local.app/Contents/MacOS/cbioportal-local" ./cmd/cbioportal-local
	cp cmd/cbioportal-local/macos/launch "bin/macos-$(ARCH)/cBioPortal Local.app/Contents/MacOS/launch"
	sed "s/VERSION/$$(git describe --tags --always)/" cmd/cbioportal-local/macos/Info.plist > "bin/macos-$(ARCH)/cBioPortal Local.app/Contents/Info.plist"
	cd "bin/macos-$(ARCH)" && zip -qryFS "../cBioPortal-Local-macos-$(ARCH).zip" "cBioPortal Local.app"

test: ## unit tests (no services needed)
	go test ./internal/... ./cmd/... ./ops/... ./rewrite/...

fmt: ## format the code
	gofmt -w cmd internal ops rewrite test

vet: ## go vet every package
	go vet ./...

lint: vet ## gofmt check + go vet (what CI runs)
	@out=$$(gofmt -l cmd internal ops rewrite test); if [ -n "$$out" ]; then echo "gofmt needed:"; echo "$$out"; exit 1; fi

contract: ## API contract tests against a running API (API=...)
	API=$(API) go test ./test/

e2e: ## frontend Playwright suite against the API on :18090 (WORKERS=3)
	test/run_e2e.sh $(or $(WORKERS),3)

testdb: ## build the test databases: make testdb FIXTURES=<cbioportal-test> SEED=<seed.sql.gz>
	test/setup_test_db.sh $(FIXTURES) $(SEED)

oncotree: ## refresh the OncoTree copy the import uses for CANCER_TYPE
	curl -fsS 'https://oncotree.mskcc.org/api/tumorTypes?version=oncotree_latest_stable' | \
		jq -r '"# OncoTree oncotree_latest_stable, fetched \(now | strftime("%Y-%m-%d")): code, name, main type.", (sort_by(.code)[] | [.code, .name, .mainType] | @tsv)' \
		> internal/transform/oncotree.tsv

frontend: ## build the frontend submodule into frontend/dist
	git submodule update --init
	cd frontend && pnpm install --frozen-lockfile && pnpm run buildAll

run: build ## run the portal (UI + API) on $(PORT) against $(CBIOPORTAL_DB)
	FRONTEND_DIR=frontend/dist PORT=$(PORT) CBIOPORTAL_DB=$(CBIOPORTAL_DB) bin/cbioportal-api

clean:
	rm -rf bin

help: ## list targets
	@grep -E '^[a-z0-9]+:.*## ' $(MAKEFILE_LIST) | sed 's/:.*## /\t/'
