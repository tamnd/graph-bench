# gr-bolt: the gr engine driven over the Bolt wire, the gr-bolt benchmark target.
#
# This builds the gr server from source and runs it with the Bolt listener on,
# so the bolt-tagged graph-bench binary can point at bolt://host:7689 and drive
# gr the same way it drives Neo4j and Memgraph: same protocol, same dialect.
#
# gr is a public Go module. If you build behind credentials for a private mirror,
# pass GOPRIVATE and the netrc as build args or mount them; the default path
# fetches the public module.
FROM golang:1.26-bookworm AS build
ARG GR_VERSION=latest
RUN go install github.com/tamnd/gr/cmd/gr@${GR_VERSION}

FROM debian:bookworm-slim
RUN useradd --system --create-home gr
COPY --from=build /go/bin/gr /usr/local/bin/gr
USER gr
# The database lives on a volume so a load survives a container restart, which
# the native-loader load path (GR_BOLT_DB_PATH) needs.
VOLUME ["/data"]
# 7689 is the GR_BOLT_URI default the bolt adapter expects; 7475 is the HTTP API.
EXPOSE 7689 7475
ENTRYPOINT ["gr", "serve", "--bolt", "--bolt-addr", ":7689", "--addr", ":7475", "/data/bench.gr"]
