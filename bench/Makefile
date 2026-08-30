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
compare:
	$(GO) run ./cmd/compare -corpus $(CORPUS)

clean:
	rm -rf $(CORPUS)
