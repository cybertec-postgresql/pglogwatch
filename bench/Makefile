GO      ?= go
RECORDS ?= 200000
SEED    ?= 20260830
CORPUS  ?= corpus

.PHONY: corpus compare clean

## corpus: regenerate the benchmark corpus from its seed (TST-003, DAT-001).
## The payload is not committed; the manifest is.
corpus:
	$(GO) run ./cmd/corpus -dir $(CORPUS) -records $(RECORDS) -seed $(SEED) \
		-manifest corpus-v1.manifest

## compare: run pglogwatch against pgbadger and pgweasel (TST-011, §6.4).
##
## RUNS defaults to 10, which is TST-011's minimum. WARMUP runs are discarded
## so the first measured run is not the one that faults the corpus into page
## cache.
RUNS   ?= 10
WARMUP ?= 3

## GATE=-gate makes an unmet or unmeasured threshold exit 2, which is what a
## release job wants and a developer running this locally does not.
GATE   ?=

compare:
	$(GO) run ./cmd/compare -corpus $(CORPUS) -runs $(RUNS) -warmup $(WARMUP) -out RESULTS.md $(GATE)

clean:
	rm -rf $(CORPUS)
