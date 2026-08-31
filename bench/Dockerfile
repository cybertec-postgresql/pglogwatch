# Throwaway image for the §6.4 comparative benchmark.
#
# Exists because the comparison cannot run on the development machine: no
# baseline is installed there, and PERF-027 is stated in peak RSS, which Go
# reads through ru_maxrss -- a Unix facility Windows does not provide. Linux
# makes all the thresholds measurable at once.
#
# pgweasel is built TWICE. It was a Go program until late 2025 and is Rust now,
# and the two are different enough to change the answer, so both are measured
# and the results table says which is which.

FROM rust:1-bookworm AS weasel-rs
RUN git clone --depth 1 https://github.com/kmoppel/pgweasel /src
WORKDIR /src
# Record the exact commit: INF-003 wants pinned baselines, and "whatever main
# was that day" makes a change in the ratio unattributable.
RUN git rev-parse HEAD > /commit && cargo build --release --locked

FROM golang:1.24-bookworm AS weasel-go
# The last commit that touched the Go sources. NOT the commit that deleted
# them (6475ff03), which is a tree with the Rust program already in place.
ARG WEASEL_GO_REV=231132a5d5175cfc00434a25b8f6a5772307399e
RUN git clone https://github.com/kmoppel/pgweasel /src
WORKDIR /src
RUN git checkout -q $WEASEL_GO_REV && go build -o /pgweasel-go . && echo $WEASEL_GO_REV > /commit

FROM golang:1.24-bookworm
RUN apt-get update \
 && apt-get install -y --no-install-recommends pgbadger time \
 && rm -rf /var/lib/apt/lists/*
COPY --from=weasel-rs /src/target/release/pgweasel /usr/local/bin/pgweasel
COPY --from=weasel-rs /commit /pgweasel.commit
COPY --from=weasel-go /pgweasel-go /usr/local/bin/pgweasel-go
COPY --from=weasel-go /commit /pgweasel-go.commit
WORKDIR /work
