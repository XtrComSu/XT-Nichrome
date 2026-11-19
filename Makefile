# XT-Nichrome
#
# Every target runs against the real pinned Chromium. There is no mock browser
# mode: the stealth guarantees are properties of the launched process, so a
# fake would only test the fake.

GO        ?= go
BROWSER   ?= $(shell ls -d $(HOME)/.cache/ms-playwright/chromium-*/chrome-linux/chrome 2>/dev/null | sort -t- -k2 -n | tail -1)
STATE     ?= $(HOME)/.local/share/xt-nichrome
FONTS     ?= $(STATE)/fonts/w11
LISTEN    ?= 127.0.0.1:7788
FIXTURE   ?= 127.0.0.1:7788
DETECT    ?= 127.0.0.1:7799

.PHONY: all build cli mcp fixture detect test unit e2e bench race fmt vet lint fonts run clean help

all: fmt vet test

help:
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  %-12s %s\n", $$1, $$2}'

build: ## Build the daemon
	$(GO) build -trimpath -o bin/nichrome ./cmd/nichrome

cli: ## Build the CLI client
	$(GO) build -trimpath -o bin/nichrome-cli ./cmd/nichrome-cli

mcp: ## Build the standalone stdio MCP server
	$(GO) build -trimpath -o bin/nichrome-mcp ./cmd/nichrome-mcp

fixture: ## Build the fixture server
	$(GO) build -trimpath -o bin/fixture-server ./cmd/fixture-server

detect: ## Build the adversarial detection probe server
	$(GO) build -trimpath -o bin/detect-server ./cmd/detect-server

fonts: ## Build the Windows-shaped font pack from OFL sources
	python3 deploy/build-fonts.py --out $(FONTS)

vet: ## Static analysis
	$(GO) vet ./...

fmt: ## Check formatting
	@out=$$(gofmt -l .); if [ -n "$$out" ]; then echo "unformatted:"; echo "$$out"; exit 1; fi

unit: ## Unit tests (no browser)
	$(GO) test ./internal/... -count=1

e2e: build fixture detect ## End-to-end tests against real Chromium
	XT_NICHROME_BROWSER=$(BROWSER) $(GO) test ./tests/ -count=1 -v -timeout 20m

race: ## Race detector over the package tests and the daemon package
	$(GO) test -race ./internal/... -count=1

bench: ## Benchmark against direct CDP
	XT_BENCH_SERVICE=1 $(GO) test ./bench/ -count=1 -v -timeout 20m

test: unit e2e ## Everything

run: build ## Run the daemon in the foreground
	./bin/nichrome -listen $(LISTEN) -browser $(BROWSER) -fonts-dir $(FONTS) -allow "http://$(FIXTURE)"

clean: ## Remove build output
	rm -rf bin
