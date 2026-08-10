# Bolt-plane targets in containers

zu runs as a local subprocess and ladybug runs in-process through CGo, so neither needs a container.
The Bolt-plane engines (neo4j, memgraph) need a server answering on a port before the bolt-tagged binary can drive them.
This directory holds one Dockerfile per engine plus a compose file that brings both up at once, each on its own Bolt port.

Image tags here are pinned in lockstep with `engine/pins.go`, the single pin table (spec 01 §3). Bump them together or the condition stamp will disagree with the container.

## Ports

Each server gets a distinct host port so they run together:

| engine   | Bolt URI                  | env var the adapter reads |
| -------- | ------------------------- | ------------------------- |
| neo4j    | bolt://127.0.0.1:7687     | NEO4J_URI                 |
| memgraph | bolt://127.0.0.1:7688     | MEMGRAPH_URI              |

Memgraph's container listens on 7687 like Neo4j, so compose maps it to host 7688 to avoid the collision.

## Bring them up

From the repo root:

```
docker compose -f docker/docker-compose.yml up -d --build
```

The first run pulls the Neo4j and Memgraph images. Watch the health checks settle before you run the bench:

```
docker compose -f docker/docker-compose.yml ps
```

## Run the bench against them

The Bolt adapters register under `-tags bolt`. Neo4j Community refuses a password shorter than eight characters, so the compose file sets `neo4j/benchbench` and you pass the same password through `NEO4J_PASS`:

```
NEO4J_PASS=benchbench go run -tags bolt ./cmd/graph-bench \
    run --workload micro-grid --engines neo4j,memgraph --count 30
```

Memgraph ships with auth off, so its user and password default to empty.
Point an adapter somewhere else by overriding its env var, for example `NEO4J_URI=bolt://otherhost:7687`.

Note on load fairness (F2): when Neo4j runs in this container, the harness cannot reach the server's import directory from the host, so the adapter falls back to batched `UNWIND` loading. That is fine for micro scales; for SNB SF1+ mount a host directory at `/var/lib/neo4j/import` and pass it as the adapter's `import_dir` so the `LOAD CSV` path is available, or run Neo4j on the host.

## Tear down

```
docker compose -f docker/docker-compose.yml down -v
```

The `-v` drops the named volumes (`neo4j-data`, `memgraph-data`) so the next run starts clean.
