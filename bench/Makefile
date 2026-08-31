GO      ?= go
RECORDS ?= 200000
SEED    ?= 20260830
CORPUS  ?= corpus

.PHONY: corpus compare compare-docker clean

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

## compare-docker: run the comparison in a throwaway container.
##
## The baselines are not installable everywhere -- pgbadger is Perl, pgweasel
## needs a Rust toolchain for the current release and a Go one for the last Go
## build -- and PERF-027 is stated in peak RSS, which Go reads through
## ru_maxrss and Windows does not provide. The container supplies all three and
## a platform that can measure them.
##
## It is NOT the pinned runner (INF-002), so figures from it may not be
## published as meeting a threshold (VAL-004).
IMAGE ?= pglogwatch-bench

compare-docker:
	docker build -t $(IMAGE) .
	docker run --rm -v "$(CURDIR)/..:/src:ro" $(IMAGE) sh -c '\
		cp -r /src/. /work/ && cd /work && rm -rf .git && \
		(cd cmd/pglogwatch && go build -o /usr/local/bin/pglogwatch .) && \
		cd bench && make corpus RECORDS=$(RECORDS) && \
		BENCH_VERSION_PGWEASEL_GO="Go build @$$(cat /pgweasel-go.commit)" \
		make compare RUNS=$(RUNS) WARMUP=$(WARMUP) && cat RESULTS.md'
