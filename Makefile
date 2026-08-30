GO      ?= go
MODULES := . ./compress ./pgremote ./bench ./cmd/pglogwatch

.PHONY: all test test-race cover fuzz bench bench-compare corpus lint tidy

all: lint test

## test: run the test suite of every module in the repository.
test:
	@for m in $(MODULES); do echo "==> $$m"; (cd $$m && $(GO) test ./...) || exit 1; done

## test-race: same, under the race detector (TST-006).
test-race:
	@for m in $(MODULES); do echo "==> $$m"; (cd $$m && $(GO) test -race ./...) || exit 1; done

## cover: statement coverage; VAL-006 wants >= 90% root, >= 80% overall.
cover:
	$(GO) test -coverprofile=coverage.out ./...
	$(GO) tool cover -func=coverage.out | tail -1

## fuzz: short local run of every fuzz target. CI runs these for 30m (TST-007).
fuzz:
	$(GO) test -run '^$$' -fuzz FuzzParseRecord -fuzztime 30s .
	$(GO) test -run '^$$' -fuzz FuzzPrefixTemplate -fuzztime 30s .
	$(GO) test -run '^$$' -fuzz FuzzUnquote -fuzztime 30s .

## bench: parser benchmarks with allocation accounting (AC-012).
bench:
	$(GO) test -run '^$$' -bench . -benchmem ./...

## corpus: regenerate the versioned benchmark corpus from its seed (TST-003).
## The manifest is committed; the payload is not (DAT-001).
corpus:
	@$(MAKE) -C bench corpus

## bench-compare: pglogwatch vs pgbadger vs pgweasel (§6.4, TST-011).
##
## Every performance claim in the README or the release notes must be
## reproducible by this target, citing the corpus version and the reference
## machine (GUD-006, VAL-004). It runs with whatever is installed and reports
## the rest as not measured, so it is useful before the pinned runner exists.
bench-compare: corpus
	@$(MAKE) -C bench compare
	@echo
	@echo "Results written to bench/RESULTS.md."
	@echo "Any published figure must cite the corpus version and bench/MACHINE.md."

## lint: go vet plus golangci-lint across every module.
lint:
	@for m in $(MODULES); do echo "==> $$m"; (cd $$m && $(GO) vet -stdmethods=false ./...) || exit 1; done
	@command -v golangci-lint >/dev/null 2>&1 && golangci-lint run ./... || echo "golangci-lint not installed, skipped"

## tidy: tidy every module.
tidy:
	@for m in $(MODULES); do (cd $$m && $(GO) mod tidy); done
