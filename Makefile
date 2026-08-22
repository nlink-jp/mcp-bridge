BINARY   := mcp-bridge
VERSION  ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
LDFLAGS  := -ldflags "-X main.version=$(VERSION)"
DIST_DIR := dist

.PHONY: build build-all test lint check docs-mirror-check clean

build:
	@mkdir -p $(DIST_DIR)
	go build $(LDFLAGS) -o $(DIST_DIR)/$(BINARY) .

build-all:
	@mkdir -p $(DIST_DIR)
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build $(LDFLAGS) -o $(DIST_DIR)/$(BINARY)-linux-amd64   .
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build $(LDFLAGS) -o $(DIST_DIR)/$(BINARY)-linux-arm64   .
	# darwin is arm64-only (no amd64, no universal — see §Release Archive Standard)
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build $(LDFLAGS) -o $(DIST_DIR)/$(BINARY)-darwin-arm64  .
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build $(LDFLAGS) -o $(DIST_DIR)/$(BINARY)-windows-amd64.exe .

test:
	go test ./...

lint:
	go vet ./...
	@test -z "$$(gofmt -l . 2>/dev/null)" || { echo "gofmt needed:"; gofmt -l .; exit 1; }

docs-mirror-check:
	@scripts/docs-mirror-check.sh

check: lint test docs-mirror-check

clean:
	rm -rf $(DIST_DIR)
