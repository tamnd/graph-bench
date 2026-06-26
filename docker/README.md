# Bolt-plane targets in containers

The in-process engines (gr, ladybug) run inside the benchmark binary with nothing to stand up.
The Bolt-plane engines (gr-bolt, neo4j, memgraph) need a server answering on a port before the bolt-tagged binary can drive them.
This directory holds one Dockerfile per engine plus a compose file that brings all three up at once, each on its own Bolt port.

## Ports

Each server gets a distinct host port so they run together:

| target   | Bolt URI                  | env var the adapter reads |
| -------- | ------------------------- | ------------------------- |
| gr-bolt  | bolt://127.0.0.1:7689     | GR_BOLT_URI               |
| neo4j    | bolt://127.0.0.1:7687     | NEO4J_URI                 |
| memgraph | bolt://127.0.0.1:7688     | MEMGRAPH_URI              |

Memgraph's container listens on 7687 like Neo4j, so compose maps it to host 7688 to avoid the collision.

## Bring them up

From the repo root:

```
docker compose -f docker/docker-compose.yml up -d --build
```

The first build compiles gr from source and pulls the Neo4j and Memgraph images, so it takes a few minutes.
Watch the health checks settle before you run the bench:

```
docker compose -f docker/docker-compose.yml ps
```

## Run the bench against them

The Bolt adapters register under `-tags bolt`. Neo4j Community refuses a password shorter than eight characters, so the compose file sets `neo4j/benchbench` and you pass the same password through `NEO4J_PASS`:

```
NEO4J_PASS=benchbench go run -tags bolt ./cmd/graph-bench \
    run --workload micro-grid --engines gr-bolt,neo4j,memgraph --count 30
```

gr-bolt and memgraph need no auth, so their user and password default to empty.
Point an adapter somewhere else by overriding its env var, for example `GR_BOLT_URI=bolt://otherhost:7689`.

## Loading gr-bolt

gr-bolt has three load modes, selected by environment variable, because pushing a large dataset over `UNWIND` plus `MERGE` is slow:

- `GR_BOLT_PRELOADED=1` skips the load and reports the counts already in the server, for when you loaded the database out of band.
- `GR_BOLT_DB_PATH=/data/bench.gr` uses gr's native bulk loader against the file the server opened, then has the server reopen it. This is the fast path for anything past a few thousand edges.
- the default pushes the dataset over Bolt with `UNWIND` plus `MERGE`, which is fine for the small micro grids and impractical past ~10k edges.

For a real run on a large dataset, load the file with gr's native loader and start the server on it, or set `GR_BOLT_DB_PATH` so the adapter does it for you.

## Tear down

```
docker compose -f docker/docker-compose.yml down -v
```

The `-v` drops the named volumes (`gr-data`, `neo4j-data`, `memgraph-data`) so the next run starts clean.
