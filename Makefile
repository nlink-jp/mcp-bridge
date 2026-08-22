BINARY  := mcp-bridge
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
GOFLAGS := -ldflags "-X main.version=$(VERSION)"
PREFIX  ?= /usr/local
DESTDIR ?=

# Developer ID Application identity matched against the keychain. Defaults to
# the generic prefix so any single Developer ID Application cert in the user's
# keychain is picked up automatically. Override when more than one is present:
#   make build CODESIGN_IDENTITY="Developer ID Application: ... (TEAMID)"
# Builds without a matching identity fall back to the Go-linker ad-hoc
# signature with a warning — see scripts/codesign-darwin.sh.
CODESIGN_IDENTITY ?= Developer ID Application

# Notarization keychain profile name. Store credentials once per machine via
#   xcrun notarytool store-credentials nlink-jp-notary \
#     --key <p8> --key-id <id> --issuer <uuid>
# Builds without the profile skip notarization with a warning — see
# scripts/notarize-darwin.sh.
NOTARY_PROFILE ?= nlink-jp-notary

# darwin ships arm64 only (no amd64, no universal). linux/windows keep their matrix.
PLATFORMS := darwin/arm64 linux/amd64 linux/arm64 windows/amd64

.PHONY: build build-all package install uninstall test lint check clean help \
	docs-mirror-check

## build: Build the binary to dist/
build:
	@mkdir -p dist
	go build $(GOFLAGS) -o dist/$(BINARY) .
	@scripts/codesign-darwin.sh dist/$(BINARY) "$(CODESIGN_IDENTITY)"

## build-all: Cross-compile for all platforms
build-all:
	@mkdir -p dist
	@for p in $(PLATFORMS); do os=$${p%/*}; arch=$${p#*/}; \
		ext=""; [ "$$os" = windows ] && ext=".exe"; \
		CGO_ENABLED=0 GOOS=$$os GOARCH=$$arch go build $(GOFLAGS) -o dist/$(BINARY)-$$os-$$arch$$ext . ; \
	done
	@scripts/codesign-darwin.sh dist/$(BINARY)-darwin-arm64 "$(CODESIGN_IDENTITY)" "$(BINARY)"

## package: Build all platforms, archive with the version suffix (zip for
## darwin/windows, tar.gz for linux), bundle the canonical binary + README.md
## + LICENSE, and notarize the darwin build. Asset naming follows the org
## Release Archive Standard — version BEFORE os/arch.
package: build-all
	@cd dist && for p in $(PLATFORMS); do os=$${p%/*}; arch=$${p#*/}; \
		ext=""; [ "$$os" = windows ] && ext=".exe"; \
		stage=_pkg; rm -rf $$stage; mkdir -p $$stage; \
		cp "$(BINARY)-$$os-$$arch$$ext" "$$stage/$(BINARY)$$ext"; \
		cp ../README.md ../LICENSE $$stage/; \
		base="$(BINARY)-$(VERSION)-$$os-$$arch"; \
		if [ "$$os" = linux ]; then ( cd $$stage && tar -czf "../$$base.tar.gz" * ); \
		else ( cd $$stage && zip -q "../$$base.zip" * ); fi; \
		rm -rf $$stage; \
	done
	@scripts/notarize-darwin.sh dist/$(BINARY)-$(VERSION)-darwin-arm64.zip "$(NOTARY_PROFILE)"

## install: Install to $(DESTDIR)$(PREFIX)/bin
install: build
	install -d $(DESTDIR)$(PREFIX)/bin
	install -m 755 dist/$(BINARY) $(DESTDIR)$(PREFIX)/bin/$(BINARY)
	@printf 'Installed %s to %s%s/bin/%s\n' "$(BINARY)" "$(DESTDIR)" "$(PREFIX)" "$(BINARY)"

## uninstall: Remove the installed binary
uninstall:
	rm -f $(DESTDIR)$(PREFIX)/bin/$(BINARY)
	@printf 'Removed %s%s/bin/%s\n' "$(DESTDIR)" "$(PREFIX)" "$(BINARY)"

## test: Run all tests
test:
	go test ./...

## lint: go vet + gofmt
lint:
	go vet ./...
	@test -z "$$(gofmt -l . 2>/dev/null)" || { echo "gofmt needed:"; gofmt -l .; exit 1; }

## docs-mirror-check: Verify docs/en and docs/ja are full structural mirrors
docs-mirror-check:
	@bash scripts/docs-mirror-check.sh

## check: lint + test + docs-mirror-check
check: lint test docs-mirror-check

## clean: Remove build artifacts
clean:
	rm -rf dist/

## help: Show this help
help:
	@grep -hE '^## ' $(MAKEFILE_LIST) | sed 's/^## /  /'

# Homebrew tap generation (see scripts/release-brew.mk). After `make package`,
# `make brew` generates the formula from the built darwin-arm64 zip into the
# local nlink-jp/homebrew-tap checkout. The package target is unchanged.
BREW_KIND := formula
BREW_DESC := Bridge stdio MCP clients to HTTP servers needing a pre-registered OAuth client
include scripts/release-brew.mk
