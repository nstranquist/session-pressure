GO ?= go
PREFIX ?= $(CURDIR)/bin

.PHONY: test vet fmt build desktop-test publish-ready secret-scan init

fmt:
	$(GO) fmt ./...

vet:
	$(GO) vet ./sessionpressure ./sessionpressurecmd ./sessionpressurecleanup ./sessionpressurecontrol ./cmd/session-pressure ./cmd/session-pressure-api ./cmd/session-pressure-helper

test:
	$(GO) test ./sessionpressure ./sessionpressurecmd ./sessionpressurecleanup ./sessionpressurecontrol ./internal/hostcleanup ./cmd/session-pressure ./internal/filelock ./internal/jsonl ./internal/notifyinbox ./internal/operationcontract ./packages/processtree -count=1 -timeout 10m

desktop-test:
	$(MAKE) -C apps/SessionPressure test

build:
	mkdir -p $(PREFIX)
	$(GO) build -o $(PREFIX)/session-pressure ./cmd/session-pressure
	$(GO) build -o $(PREFIX)/session-pressure-api ./cmd/session-pressure-api
	$(GO) build -o $(PREFIX)/session-pressure-helper ./cmd/session-pressure-helper

secret-scan:
	@if command -v gitleaks >/dev/null 2>&1; then gitleaks detect --source . --no-git --redact; else echo "gitleaks not installed; skipped"; fi

init: build
	@echo "init: built ./bin/session-pressure. Next: ./bin/session-pressure --json doctor"

publish-ready: fmt vet test desktop-test build secret-scan
	@echo "publish-ready: ok"
