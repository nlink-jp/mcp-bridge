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

.PHONY: build build-all package verify-release install uninstall test lint check clean help \
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
		if [ "$$os" = linux ]; then ( cd $$stage && COPYFILE_DISABLE=1 tar --no-xattrs -czf "../$$base.tar.gz" * ); \
		else ( cd $$stage && zip -q "../$$base.zip" * ); fi; \
		rm -rf $$stage; \
	done
	@scripts/notarize-darwin.sh dist/$(BINARY)-$(VERSION)-darwin-arm64.zip "$(NOTARY_PROFILE)"

## verify-release: refuse to release a zip that is un-notarized, stale, does
## not unpack, does not run, or holds a build from another tag, and a linux
## archive that carries macOS metadata or anything but its canonical files.
## Every step fails closed; only the spctl line is informational.
verify-release:
	@test -f "dist/$(BINARY)-$(VERSION)-darwin-arm64.zip.notarized" || { \
		echo "verify-release: FAIL — $(BINARY)-$(VERSION)-darwin-arm64.zip has no notarization marker."; \
		echo "  make package must end with '[notarize] ...: Accepted'. Do not upload this zip."; \
		exit 1; }
	@test "dist/$(BINARY)-$(VERSION)-darwin-arm64.zip.notarized" -nt "dist/$(BINARY)-$(VERSION)-darwin-arm64.zip" || { \
		echo "verify-release: FAIL — the zip was rebuilt after its marker (re-run make package)."; \
		exit 1; }
	@tmp=$$(mktemp -d); rc=0; \
		if ! unzip -oq "dist/$(BINARY)-$(VERSION)-darwin-arm64.zip" -d "$$tmp"; then \
			echo "verify-release: FAIL — the zip does not unpack. Do not upload it."; rc=1; \
		elif ! out=$$("$$tmp/$(BINARY)" --version 2>&1); then \
			echo "verify-release: FAIL — the packaged binary does not run:"; \
			echo "  $$out"; rc=1; \
		elif ! printf '%s\n' "$$out" | grep -qF "$(VERSION)"; then \
			echo "verify-release: FAIL — the packaged binary reports \"$$out\", not $(VERSION)."; \
			echo "  The zip holds a build from another tag (re-run make package)."; rc=1; \
		else \
			echo "  $$out"; \
			spctl -a -vv -t install "$$tmp/$(BINARY)" 2>&1 | head -2 || true; \
		fi; \
		rm -rf "$$tmp"; \
		exit $$rc
	@for p in $(PLATFORMS); do os=$${p%/*}; arch=$${p#*/}; \
		[ "$$os" = linux ] || continue; \
		f="dist/$(BINARY)-$(VERSION)-$$os-$$arch.tar.gz"; \
		names=$$(tar --options 'tar:!mac-ext' -tzf "$$f") || { echo "verify-release: FAIL — $$f does not list."; exit 1; }; \
		if printf '%s\n' "$$names" | grep -qE '(^|/)(\._|PaxHeader|__MACOSX)'; then \
			echo "verify-release: FAIL — $$f carries macOS metadata entries."; \
			echo "  macOS tar writes ._ members unless COPYFILE_DISABLE=1 is set, and lists them only with !mac-ext."; \
			exit 1; fi; \
		xh=$$(python3 -c 'import sys, tarfile; print(" ".join(m.name for m in tarfile.open(sys.argv[1]) if any(k.startswith(("LIBARCHIVE.xattr.", "SCHILY.xattr.")) for k in m.pax_headers)))' "$$f") || { \
			echo "verify-release: FAIL — $$f cannot be read for its pax headers (python3 tarfile)."; exit 1; }; \
		if [ -n "$$xh" ]; then \
			echo "verify-release: FAIL — $$f carries extended attributes as pax headers ($$xh)."; \
			echo "  macOS tar writes them unless called with --no-xattrs; COPYFILE_DISABLE alone does not."; \
			exit 1; fi; \
		got=$$(printf '%s\n' "$$names" | LC_ALL=C sort | tr '\n' ' '); \
		want=$$(printf '%s\n' "$(BINARY)" README.md LICENSE | LC_ALL=C sort | tr '\n' ' '); \
		if [ "$$got" != "$$want" ]; then \
			echo "verify-release: FAIL — $$f holds $$got; expected $$want"; exit 1; fi; \
	done
	@echo "verify-release: OK ($(VERSION), notarized, unpacks, runs, reports its version, clean linux archives)"

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

## test-linux: run the test suite inside a Linux container (podman/docker)
.PHONY: test-linux
test-linux:
	@scripts/test-linux.sh
