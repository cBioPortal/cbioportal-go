# Common tasks. Everything here is plain go/bash; see README.md for the stack.

CBIOPORTAL_DB ?= cbioportal
PORT ?= 8080
API ?= http://localhost:18090

.PHONY: build curate curate-macos curate-macos-app curate-app curate-app-zip curate-app-icon test fmt vet lint contract e2e run frontend testdb oncotree clean help

build: ## build the programs into bin/
	go build -o bin/cbioportal-import ./cmd/cbioportal-import
	go build -o bin/cbioportal-api ./cmd/cbioportal-api
	go build -o bin/cbioportal-ops ./ops/cbioportal.org/cmd/cbioportal-ops
	go build -o bin/cbioportal-rewrite-test ./rewrite/cmd/cbioportal-rewrite-test
	CGO_ENABLED=0 go build -o bin/cbioportal-curate ./cmd/cbioportal-curate

curate: ## bin/cbioportal-curate with the website built in (needs frontend/dist)
	test -f frontend/dist/index.html || { echo "build the frontend first (make frontend)"; exit 1; }
	tar -czf cmd/cbioportal-curate/frontend.tar.gz -C frontend/dist .
	CGO_ENABLED=0 go build -tags embedfrontend -o bin/cbioportal-curate ./cmd/cbioportal-curate

# A macOS app per processor: the program, the script that starts it, and
# the app's Info.plist, zipped. Opened without a folder, the program shows
# its import page in the browser.
curate-macos: ## bin/cBioPortal-Curate-macos-{arm64,amd64}.zip: double-clickable macOS apps (needs frontend/dist)
	test -f frontend/dist/index.html || { echo "build the frontend first (make frontend)"; exit 1; }
	tar -czf cmd/cbioportal-curate/frontend.tar.gz -C frontend/dist .
	$(MAKE) curate-macos-app ARCH=arm64
	$(MAKE) curate-macos-app ARCH=amd64

curate-macos-app:
	mkdir -p "bin/macos-$(ARCH)/cBioPortal Curate.app/Contents/MacOS"
	GOOS=darwin GOARCH=$(ARCH) CGO_ENABLED=0 go build -tags embedfrontend -o "bin/macos-$(ARCH)/cBioPortal Curate.app/Contents/MacOS/cbioportal-curate" ./cmd/cbioportal-curate
	cp cmd/cbioportal-curate/macos/launch "bin/macos-$(ARCH)/cBioPortal Curate.app/Contents/MacOS/launch"
	sed "s/VERSION/$$(git describe --tags --always)/" cmd/cbioportal-curate/macos/Info.plist > "bin/macos-$(ARCH)/cBioPortal Curate.app/Contents/Info.plist"
	cd "bin/macos-$(ARCH)" && zip -qryFS "../cBioPortal-Curate-macos-$(ARCH).zip" "cBioPortal Curate.app"

# The macOS app with a window of its own (Wails; cmd/cbioportal-curate/app.go).
# Unlike the rest it needs cgo, so it builds on a Mac only. ARCH is
# universal (one app for Apple silicon and Intel, joined with lipo), arm64
# or amd64. SIGN_IDENTITY signs it for distribution ("Developer ID
# Application: ..."), with the hardened runtime; without it the app is
# signed ad hoc, enough to open it on the Mac it was built on. PUREGO=1
# builds it without cgo instead (Wails's purego backend, newer), which any
# computer can build; signing still wants a Mac.
comma := ,
ARCH ?= universal
APP_DIR = bin/app-$(ARCH)
APP = $(APP_DIR)/cBioPortal Curate.app
APP_ARCHS = $(if $(filter universal,$(ARCH)),arm64 amd64,$(ARCH))
curate-app: ## bin/app-universal/cBioPortal Curate.app: the macOS app in a window (needs a Mac, Xcode tools, frontend/dist)
	if test -f frontend/dist/index.html; then tar -czf cmd/cbioportal-curate/frontend.tar.gz -C frontend/dist .; \
	elif ! test -f cmd/cbioportal-curate/frontend.tar.gz; then echo "build the frontend first (make frontend)"; exit 1; fi
	mkdir -p "$(APP)/Contents/MacOS" "$(APP)/Contents/Resources"
	for a in $(APP_ARCHS); do \
		CGO_ENABLED=$(if $(PUREGO),0,1) GOOS=darwin GOARCH=$$a CC="clang -arch $$(test $$a = amd64 && echo x86_64 || echo arm64)" MACOSX_DEPLOYMENT_TARGET=13.0 \
		go build -tags wails,production,embedfrontend$(if $(PUREGO),$(comma)purego) -trimpath -ldflags="-s -w -extldflags=-mmacosx-version-min=13.0" -o "$(APP_DIR)/cbioportal-curate-$$a" ./cmd/cbioportal-curate || exit 1; \
	done
	lipo -create $(foreach a,$(APP_ARCHS),"$(APP_DIR)/cbioportal-curate-$(a)") -output "$(APP)/Contents/MacOS/cbioportal-curate"
	cp cmd/cbioportal-curate/macos/icon.icns "$(APP)/Contents/Resources/icon.icns"
	sed "s/VERSION/$$(git describe --tags --always | sed 's/^v//')/" cmd/cbioportal-curate/macos/App-Info.plist > "$(APP)/Contents/Info.plist"
	codesign --force --options runtime --timestamp$(if $(SIGN_IDENTITY),,=none) \
		--entitlements cmd/cbioportal-curate/macos/app.entitlements -s "$(or $(SIGN_IDENTITY),-)" "$(APP)"

curate-app-zip: curate-app ## bin/cBioPortal-Curate-app-$(ARCH).zip, ready for notarization
	cd "$(APP_DIR)" && ditto -c -k --keepParent "cBioPortal Curate.app" "../cBioPortal-Curate-app-$(ARCH).zip"

# The app's icon, from icon.svg (needs rsvg-convert: brew install librsvg).
curate-app-icon:
	d="$$(mktemp -d)/icon.iconset" && mkdir -p "$$d" && for n in 16 32 128 256 512; do \
		rsvg-convert -w $$n cmd/cbioportal-curate/macos/icon.svg -o "$$d/icon_$${n}x$${n}.png" && \
		rsvg-convert -w $$((n*2)) cmd/cbioportal-curate/macos/icon.svg -o "$$d/icon_$${n}x$${n}@2x.png" || exit 1; done && \
	iconutil -c icns "$$d" -o cmd/cbioportal-curate/macos/icon.icns

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
