# graph-bench

A fair, reproducible benchmark for graph databases.

`graph-bench` measures graph databases against each other on the same data, the same queries, and the same machine, and reports the result without spin. It treats [`gr`](https://github.com/tamnd/gr) as one target among many, held to the same rules as every other engine, so the numbers `gr` publishes about itself come from a harness that has no reason to flatter it.

It is the benchmarking sibling of `gr` the way `githome-bench` is to `githome`: a standalone harness that drives the system under test from the outside, defines its objectives as code, and turns raw measurements into pass/fail gates that run in CI.

## What it is

- **A cross-engine harness.** One program loads a dataset into many graph databases, runs a workload against each, and collects latency and throughput per query, per engine, per scale. Engines plug in behind a single Target interface, so adding a database is one adapter, not a fork of the harness.
- **Multi-plane.** It drives engines three ways: in-process for embedded engines with a Go API (`gr` itself), over the Bolt wire protocol with openCypher for the server engines that speak it (Neo4j, Memgraph, FalkorDB, `gr serve`), and over each remaining engine's native protocol or language where Bolt does not reach (DuckPGQ's SQL/PGQ, Apache AGE over Postgres).
- **Standards-anchored.** The workloads are drawn from the recognized graph benchmarks (LDBC SNB Interactive and BI, LDBC Graphalytics, LSQB, Graph500), plus a layer of focused micro-benchmarks. The data is generated deterministically so a run reproduces.
- **Honest by construction.** Every number carries its conditions: engine version, hardware, dataset scale and checksum, cold or warm cache, configuration, and seed. The headline metric is a high percentile, never a mean. Results are validated for correctness before they are timed.
- **A regression gate.** A small subset runs in GitHub Actions on every change to catch `gr` slowing down against a stored baseline. The full cross-engine comparison runs on a controlled machine.

## What it is not

It is not a leaderboard that crowns a winner, not a vendor benchmark, and not a correctness suite. It reports a matrix and lets the reader draw conclusions.

## Measured results

Every number below came out of this harness on 2026-08-12. Each one is service-time latency at the stated percentile, after a fixed 2 second warmup, with no engine tuning (`tuned=false` in the result files). Nothing is timed before it is verified: the harness computes the answer itself from the canonical CSV and compares it to what the engine returned, and a query that fails verification is reported as a failure instead of a latency. The verification dialect is recorded per query, so a fast number can always be traced to the text the engine actually ran.

The result JSON for each table is under `results/<workload>/<scale>/`, named by timestamp, engine, plane, and dataset checksum.

### Which engine ran where

| Engine | Plane | Version | Machine | Scale | Workloads measured |
| --- | --- | --- | --- | --- | --- |
| zu | subprocess | 0.0.1 | laptop and desktop | smoke and sf1 | micro-read, micro-er, micro-powerlaw |
| zu-capi | in-process, cgo | 0.0.1 | laptop | smoke | micro-read, micro-er, micro-powerlaw |
| ladybug | in-process, cgo | 0.19.1 | laptop | smoke | micro-read, micro-er, micro-powerlaw |
| Neo4j | Bolt | 2026.06.0 | desktop | sf1 | micro-read, micro-er |
| Memgraph | Bolt | not measured | desktop | sf1 | none, see below |

zu and zu-capi are the same engine, the same build, and the same query texts. The only difference is how the harness reaches it: `zu` drives the CLI over a pipe, one frame per query, and `zu-capi` links libzu and calls it directly, which is the plane ladybug runs on. Having both in one table is what makes the ladybug comparison an engine comparison instead of a transport comparison.

zu is the only engine measured on both machines, because it is the only one that needs no server and no cgo. ladybug is not in the Neo4j tables and Neo4j is not in the ladybug tables: the laptop had no Docker running for a Neo4j server, and the desktop had no liblbug built for the cgo adapter. Neo4j also has no micro-powerlaw numbers, since that workload was only run on the laptop.

The laptop tables and the desktop tables are different machines at different scales. Compare engines within one table. Do not read a zu number from one table against a zu number from the other, and do not put ladybug's laptop numbers next to Neo4j's desktop numbers, because nothing in that comparison is held constant.

### How to read the tables

The `zu speedup` column is the other engine's p50 divided by zu's p50 on the same plane: against ladybug it uses the in-process zu number, against Neo4j it uses the subprocess zu number, since that is the only zu plane measured on the desktop. Above 1.0 means zu is faster by that factor, below 1.0 means zu is slower. Rows in **bold** are the ones where zu loses, and there are none left in the laptop tables. The class rows (point-read, traversal, aggregation, subgraph) are the harness's own rollups over the queries in that class and are reported as the harness renders them, not recomputed here. Where a p50 renders in milliseconds with two decimals, the ratio inherits that rounding and is marked as approximate.

The plane matters, and on the laptop it is now measured instead of argued about. ladybug runs in-process through cgo and pays nothing to reach the engine. Neo4j runs over Bolt and pays a socket round trip per query. zu is in the laptop tables twice, once over the subprocess plane and once in-process, so the transport cost is a column you can read rather than a caveat you have to trust. On the desktop only the subprocess plane was measured, so every zu number in the Neo4j tables still carries a frame cost of roughly 13µs that Neo4j's numbers do not have.

### Apple silicon laptop, zu 0.0.1 against ladybug 0.19.1

Smoke scale, macOS, all three columns from one run of the harness on the same machine, the same datasets, and the same process. `zu` is the subprocess plane and `zu-capi` is the same build linked in-process through libzu, which is the plane ladybug runs on. The speedup column compares ladybug against `zu-capi`, so both sides pay the same transport, which is none.

**micro-read**, dataset `grid-30x30` (checksum `eb8d5d60`), fidelity harness-native. ladybug answered seven of the nine queries through its kuzu dialect and the two scans through Cypher; both zu columns answered all nine through zuQL.

| Query | ladybug p50 | ladybug p99 | zu p50 | zu p99 | zu-capi p50 | zu-capi p99 | zu-capi speedup |
| --- | --- | --- | --- | --- | --- | --- | --- |
| point-read (class) | 59.0µs | 353.9µs | 35.7µs | 46.6µs | 23.2µs | 33.1µs | 2.5x |
| traversal (class) | 594.9µs | 1779.2µs | 19.3µs | 36.5µs | 5.5µs | 26.0µs | 108.2x |
| aggregation (class) | 152.9µs | 222.1µs | 28.2µs | 69.6µs | 11.9µs | 30.8µs | 12.8x |
| micro-point | 51.1µs | 80.4µs | 42.2µs | 48.6µs | 29.1µs | 33.8µs | 1.8x |
| micro-point-miss | 50.4µs | 132.3µs | 35.7µs | 39.5µs | 23.2µs | 28.0µs | 2.2x |
| micro-edge | 231.9µs | 429.8µs | 26.6µs | 30.1µs | 14.5µs | 19.8µs | 16.0x |
| micro-khop1 | 187.0µs | 349.2µs | 33.2µs | 37.9µs | 20.6µs | 26.0µs | 9.1x |
| micro-khop2 | 619.2µs | 918.5µs | 17.7µs | 23.0µs | 4.8µs | 9.1µs | 129.0x |
| micro-khop3 | 1276.0µs | 1876.1µs | 18.7µs | 25.8µs | 5.6µs | 6.3µs | 227.9x |
| micro-varlen | 535.1µs | 1129.7µs | 18.5µs | 21.7µs | 5.2µs | 36.1µs | 102.9x |
| micro-scan-count | 136.8µs | 210.8µs | 15.6µs | 69.6µs | 3.6µs | 11.9µs | 38.0x |
| micro-scan-stats | 166.2µs | 222.1µs | 29.6µs | 53.2µs | 18.2µs | 30.8µs | 9.1x |

**micro-er**, dataset `er-n1000-p0.01` (checksum `d3c97598`, 1000 nodes, 10219 edges), fidelity harness-native. Both triangle counts verified against the harness's own counting oracle, zu through zuQL and ladybug through Cypher.

| Query | ladybug p50 | ladybug p99 | zu p50 | zu p99 | zu-capi p50 | zu-capi p99 | zu-capi speedup |
| --- | --- | --- | --- | --- | --- | --- | --- |
| subgraph (class) | 8.02ms | 16.15ms | 1.25ms | 6.01ms | 3.14ms | 6.28ms | ~2.6x |
| micro-triangle | 5.67ms | 8.02ms | 1.01ms | 1.25ms | 0.97ms | 3.14ms | ~5.8x |
| micro-triangle-undirected | 14.56ms | 19.63ms | 5.79ms | 6.10ms | 5.72ms | 6.66ms | ~2.5x |

**micro-powerlaw**, dataset `powerlaw-n1000-g2.5` (checksum `82425f5a`), fidelity harness-native.

| Query | ladybug p50 | ladybug p99 | zu p50 | zu p99 | zu-capi p50 | zu-capi p99 | zu-capi speedup |
| --- | --- | --- | --- | --- | --- | --- | --- |
| point-read (class) | 52.5µs | 77.4µs | 39.2µs | 64.5µs | 25.2µs | 31.8µs | 2.1x |
| traversal (class) | 584.2µs | 1511.8µs | 19.0µs | 75.5µs | 5.8µs | 29.2µs | 100.7x |
| subgraph (class) | 1.49ms | 7.98ms | 0.18ms | 0.84ms | 0.16ms | 0.79ms | ~9.3x |
| micro-point | 52.4µs | 83.2µs | 42.5µs | 83.4µs | 29.1µs | 34.0µs | 1.8x |
| micro-point-miss | 52.5µs | 73.0µs | 35.6µs | 39.9µs | 23.3µs | 24.3µs | 2.3x |
| micro-khop1 | 185.1µs | 243.8µs | 32.9µs | 87.4µs | 19.8µs | 24.1µs | 9.3x |
| micro-khop2 | 681.3µs | 1118.7µs | 17.5µs | 29.0µs | 4.5µs | 13.2µs | 151.4x |
| micro-khop3 | 1206.0µs | 1729.1µs | 18.2µs | 28.7µs | 5.5µs | 29.3µs | 219.3x |
| micro-varlen | 515.8µs | 784.6µs | 18.3µs | 79.8µs | 4.9µs | 45.6µs | 105.3x |
| micro-triangle | 1493.0µs | 2121.7µs | 151.0µs | 175.3µs | 139.2µs | 162.0µs | 10.7x |
| micro-triangle-undirected | 6.58ms | 7.99ms | 0.73ms | 0.85ms | 0.69ms | 0.88ms | ~9.5x |
| micro-sp | 553.9µs | 1038.5µs | SKIP | SKIP | SKIP | SKIP | no-shortest-paths |
| micro-sp-bidir | 686.3µs | 1196.4µs | SKIP | SKIP | SKIP | SKIP | no-shortest-paths |

### What the subprocess plane costs

The two zu columns above are the same engine, so the difference between them is transport and nothing else. On micro-read, per query, p50:

| Query | zu p50 | zu-capi p50 | frame cost |
| --- | --- | --- | --- |
| micro-point | 42.2µs | 29.1µs | 13.1µs |
| micro-point-miss | 35.7µs | 23.2µs | 12.5µs |
| micro-edge | 26.6µs | 14.5µs | 12.1µs |
| micro-khop1 | 33.2µs | 20.6µs | 12.6µs |
| micro-khop2 | 17.7µs | 4.8µs | 12.9µs |
| micro-khop3 | 18.7µs | 5.6µs | 13.1µs |
| micro-varlen | 18.5µs | 5.2µs | 13.3µs |
| micro-scan-count | 15.6µs | 3.6µs | 12.0µs |
| micro-scan-stats | 29.6µs | 18.2µs | 11.4µs |

It is a flat 11µs to 13µs, which is what a JSON frame down a pipe and back costs, and it does not scale with the query. That is most of the answer on the cheap reads: `micro-scan-count` spends 3.6µs in the engine and 12.0µs in the pipe, so over the subprocess plane it looks four times slower than it is. On anything that takes a millisecond the frame disappears into the noise, which is why the triangle rows barely move between the two columns.

The in-process adapter does not change any conclusion against ladybug, it sharpens them. Every win gets larger and no result flips.

### 32 core desktop, zu 0.0.1 against Neo4j 2026.06.0

sf1 scale, Ubuntu under WSL2, Neo4j in Docker on the same machine reached over Bolt at `bolt://127.0.0.1:7687`.

**micro-read**, dataset `grid-100x100` (checksum `fd000c2a`), fidelity harness-native. Neo4j answered every query through Cypher.

| Query | neo4j p50 | neo4j p99 | zu p50 | zu p99 | zu speedup |
| --- | --- | --- | --- | --- | --- |
| point-read (class) | 592.6µs | 1044.9µs | 80.2µs | 287.7µs | 7.4x |
| traversal (class) | 612.3µs | 1126.5µs | 59.1µs | 175.8µs | 10.4x |
| aggregation (class) | 0.68ms | 9.59ms | 0.08ms | 0.40ms | ~8.5x |
| micro-point | 608.4µs | 973.5µs | 98.4µs | 323.0µs | 6.2x |
| micro-point-miss | 539.6µs | 734.3µs | 75.2µs | 178.0µs | 7.2x |
| micro-edge | 631.0µs | 1083.3µs | 70.8µs | 242.2µs | 8.9x |
| micro-khop1 | 601.0µs | 965.6µs | 80.8µs | 234.9µs | 7.4x |
| micro-khop2 | 627.1µs | 1070.3µs | 54.7µs | 248.0µs | 11.5x |
| micro-khop3 | 672.1µs | 1311.9µs | 53.0µs | 115.3µs | 12.7x |
| micro-varlen | 583.5µs | 1074.1µs | 54.2µs | 162.2µs | 10.8x |
| micro-scan-count | 519.7µs | 684.2µs | 49.3µs | 83.8µs | 10.5x |
| micro-scan-stats | 1.38ms | 9.59ms | 0.11ms | 0.40ms | ~12.5x |

**micro-er**, dataset `er-n10000-p0.001` (checksum `ba0384b0`, 10000 nodes, 99770 edges), fidelity harness-native.

| Query | neo4j p50 | neo4j p99 | zu p50 | zu p99 | zu speedup |
| --- | --- | --- | --- | --- | --- |
| subgraph (class) | 143.79ms | 1119.85ms | 3.24ms | 89.61ms | 44.4x |
| micro-triangle | 143.33ms | 182.61ms | 3.17ms | 3.90ms | 45.2x |
| micro-triangle-undirected | 1100.43ms | 1119.85ms | 78.47ms | 89.61ms | 14.0x |

Memgraph was in the same run and never came up. Its managed container reported no 7687/tcp binding and the fallback URI at 7688 refused the connection, so it produced no numbers and the run exited with an engine failure. That is a harness bug on this host, not a Memgraph result, and it is tracked as such.

### What does not run yet

A workload only produces numbers for an engine that has text in that engine's dialect chain. There is no silent fallback: if the chain has nothing, the query reports SKIP with a reason, and the reason is in the result file. The table below is the same for `zu` and `zu-capi`, since coverage is a property of the engine and its dialect, not of the plane it is reached over.

| Workload | zu | reason |
| --- | --- | --- |
| micro-read, micro-uniform, micro-er, micro-powerlaw, micro-mix | runs | zuQL text present |
| micro-write | runs | zuQL text present, one property update per repetition, on the id column because `zu copy` loads no other node column |
| micro-sp, micro-sp-bidir | SKIP | `no-shortest-paths` |
| lsqb (q1 to q9) | SKIP | `no-dialect-text` |
| snb-short, snb-complex, snb-bi, snb-mix, snb-update | SKIP | `no-dialect-text` |
| linkbench, fb-read, fb-write | SKIP | `no-dialect-text` |
| galytics, galytics-w, gap, g500 | SKIP | `missing-algorithm:*` |

The labelled workloads are the larger of the two gaps. zu's loader takes a two column edge list, so a dataset with several node labels and several relationship types flattens into one node table and one edge table, and the LSQB and SNB texts have nothing to bind to. Covering them needs a labelled load path first and the dialect texts second.

### The gap that closed

`micro-triangle-undirected` on the ER graph was the only query in the tables above where zu lost, 22.45ms in-process against ladybug's 13.69ms. On the same harness and the same machine it now runs 5.72ms against 14.56ms. The query is the undirected triangle with an ordering predicate on the `id` property and a `count(DISTINCT [a.id, b.id, c.id])` on top.

The first reading of it was wrong and worth writing down. zu's own `explain_analyze` on the 1k ER graph put 16.27ms of the 29.79ms total in one place, the `b.id < c.id` filter over 217664 rows at about 75ns each, and that looked like a property read in the wrong loop. The property read was never the story. Those 217664 rows were, and they existed because the engine never intersected the closing edge: an undirected end reads two stored lists, and both of zu's planners took that as a reason to leave the close as a storage probe per candidate row, so the query enumerated every 2-path in the graph and then threw most of them away one predicate at a time.

Two changes in zu, [tamnd/zu#101](https://github.com/tamnd/zu/pull/101), fixed it. The closing edge now takes the intersection with both stored lists on each end, and the ordering predicate that filter placement had left sitting between the second hop and the close moves above it so the two can fuse. The same `explain_analyze` now totals 9.18ms, the close emits 4188 rows instead of 217664, and the filter above it costs 446.8µs instead of 16.27ms.

The in-process column said all along that transport had nothing to do with it, and it still does: 5.72ms in-process against 5.79ms over the subprocess plane is the same number twice, because a 13µs frame is nothing next to a millisecond query.

The desktop micro-er table further up predates both changes, so its zu numbers on the 10k ER graph are the old plan. That run needs a Neo4j server and has not been repeated yet.

### Reproducing

The two comparison sets above:

```
cargo build --release -p zu-capi        # in the zu repo, builds libzu
go build -tags "ladybug zuinproc" -o gb ./cmd/graph-bench
./gb run --workload micro-read --engines zu,zu-capi,ladybug
./gb run --workload micro-er --engines zu,zu-capi,ladybug
./gb run --workload micro-powerlaw --engines zu,zu-capi,ladybug
```

```
go build -tags bolt -o gb ./cmd/graph-bench
docker compose -f docker/docker-compose.yml up -d neo4j
NEO4J_URI=bolt://127.0.0.1:7687 ./gb run --workload micro-read --engines zu,neo4j
NEO4J_URI=bolt://127.0.0.1:7687 ./gb run --workload micro-er --engines zu,neo4j
```

Setting `NEO4J_URI` is not optional today. Without it the harness starts its own managed container, and on both hosts tried that container came up on a mapped port the harness then could not reach, which fails the run before any query is measured.

Any saved result re-renders without re-running:

```
./gb report --file results/micro-er/smoke/20260812T031425Z-ladybug-inproc-d3c97598.json
./gb compare --files results/micro-er/smoke/20260812T031420Z-zu-subprocess-d3c97598.json,results/micro-er/smoke/20260812T031425Z-ladybug-inproc-d3c97598.json
```

## Status

The core is in place. Milestones M1-M7 are merged; M8 (first published cross-engine result) is in progress.

What works today:

- `generate` -- materializes any of five synthetic graph types (uniform, power-law, ER, grid, RMAT) to the canonical CSV layout with a content-verified manifest.
- `list workloads` -- shows all registered workloads across the micro, lsqb, snb, linkbench, finbench, galytics, gap, and g500 families.
- `list engines` -- shows the registered engine adapters and their build tags.
- `run --workload micro-er --engines zu,ladybug` -- loads, verifies, and measures, then writes the result JSON. This produced every number in the results section above.
- `report --file result.json` -- re-renders any saved JSON result in table, Markdown, CSV, or JSON.
- `compare --files a.json,b.json` -- puts two or more result sets side by side with optional Bolt plane-overhead section.
- `gate --file result.json --point-read-budget 1ms` -- checks p99 against per-class budgets and exits 2 on violations.
- `noise --results results --engine zu --workload micro-read --scale smoke` -- reads repeated runs of one unchanged binary and reports how much they disagreed, per query and per metric, widest first. It prints a suggested `--noise-floor` for `gate`.

### Noise, and what a regression number is worth

A regression gate compares two numbers and calls the difference a change in the code. That is only true when the machine would have produced the same number twice, and a developer laptop often will not. Run `noise` before trusting a failed gate: it measures the spread across repeated runs of a single binary, which is the floor under every regression the gate can report. If the floor is at or above the regression factor, the gate cannot tell a slower engine from a busier machine, and it says so instead of guessing.

Passing that measured floor to `gate --noise-floor` moves differences inside it out of the violations list and into a separate section, reported and not ruled on. They are not excused: a finding inside the floor is the harness saying this run cannot answer the question, which is a different thing from saying the answer is no. Budget checks and verification integrity are unaffected, because neither is a comparison between two runs. The default is zero, no floor, which is the right setting for the controlled machine the full matrix runs on.

What is not yet wired:

- Managed containers -- the Bolt engines only work against a server the caller points them at with `NEO4J_URI` or `MEMGRAPH_URI`. Left to start their own container, both fail to bind a reachable port.
- Dialect coverage -- zu has text for the micro family only, so the labelled workloads skip. See the coverage table above.
- LDBC SNB SF1 pin -- the URL and checksums in `dataset/ldbc/pins/snb-sf1.json` are placeholders until the first verified dataset run.
- First published cross-engine result in the lineage. The tables above are a working run on developer machines, not a controlled-machine publication.

The spec roadmap is at `notes/Spec/2060/bench/10-roadmap.md`.

## Install

Homebrew (macOS/Linux):

```
brew install tamnd/tap/graph-bench
```

Pre-built binaries for linux/darwin/windows amd64+arm64 are on the [releases page](https://github.com/tamnd/graph-bench/releases).

OCI image (no shell, distroless):

```
docker pull ghcr.io/tamnd/graph-bench:latest
```

## Build from source

```
go build ./...
go test ./...
```

The default build is pure Go with no cgo and no dependency beyond the standard library and the CLI framework. It drives zu over the subprocess plane. Adapters that need a driver or cgo sit behind build tags so they never enter the default binary.

To include the Bolt adapters (Neo4j, Memgraph):

```
go build -tags bolt ./...
```

To include the in-process ladybug adapter, which links liblbug through cgo:

```
go build -tags ladybug ./...
```

To include the in-process zu adapter, which links libzu through cgo and registers as `zu-capi`:

```
go build -tags zuinproc ./...
```

It expects a sibling zu checkout built with `cargo build --release -p zu-capi`. Anywhere else, point the flags at the header and the library:

```
CGO_CFLAGS="-I$ZU_INCLUDE" CGO_LDFLAGS="-L$ZU_LIB -lzu -Wl,-rpath,$ZU_LIB" go build -tags zuinproc ./...
```

## Spec

The complete design lives at `notes/Spec/2060/bench/`. Start with `00-overview.md` for the mission and the fairness contract, `02-architecture.md` for the layout, and `10-roadmap.md` for what ships when.

## License

Apache-2.0.
