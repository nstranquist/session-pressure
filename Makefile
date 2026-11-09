GO ?= go
PREFIX ?= $(CURDIR)/bin

.PHONY: test race vet fmt build desktop-test desktop-gpui publish-ready secret-scan init

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./...

test:
	$(GO) test ./... -count=1 -timeout 10m

race:
	$(GO) test -race ./... -count=1 -timeout 10m

desktop-test:
	$(MAKE) -C apps/SessionPressure test

desktop-gpui:
	$(MAKE) -C apps/pressure-ui core-test
	$(MAKE) -C apps/pressure-ui build

build:
	mkdir -p $(PREFIX)
	$(GO) build -o $(PREFIX)/session-pressure ./cmd/session-pressure
	$(GO) build -o $(PREFIX)/session-pressure-api ./cmd/session-pressure-api
	$(GO) build -o $(PREFIX)/session-pressure-helper ./cmd/session-pressure-helper
	$(GO) build -o $(PREFIX)/gocache-lru ./cmd/gocache-lru
	$(GO) build -o $(PREFIX)/gaterunner-fleet ./cmd/gaterunner-fleet
	$(GO) build -o $(PREFIX)/session-pressure-release ./scripts/release
	$(GO) build -o $(PREFIX)/session-pressure-release-cc ./scripts/release-cc

secret-scan:
	@if command -v gitleaks >/dev/null 2>&1; then gitleaks detect --source . --no-git --redact; else echo "gitleaks not installed; skipped"; fi

init: build
	@echo "init: built ./bin/session-pressure. Next: ./bin/session-pressure --json doctor"

publish-ready: fmt vet race desktop-test build secret-scan
	@echo "publish-ready: ok"
