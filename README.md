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

The seven engine table below and the five machine table under it both date from 2026-08-19. Every number in the zu-against-ladybug laptop tables came out of this harness on 2026-08-17, and the desktop tables date from 2026-08-12. Each one is service-time latency at the stated percentile, after a fixed 2 second warmup, with no engine tuning (`tuned=false` in the result files). Nothing is timed before it is verified: the harness computes the answer itself from the canonical CSV and compares it to what the engine returned, and a query that fails verification is reported as a failure instead of a latency. The verification dialect is recorded per query, so a fast number can always be traced to the text the engine actually ran.

The result JSON for each table is under `results/<workload>/<scale>/`, named by timestamp, engine, plane, and dataset checksum.

### Which engine ran where

| Engine | Plane | Version | Machine | Scale | Workloads measured |
| --- | --- | --- | --- | --- | --- |
| zu | in-process, cgo | 0.0.1 | laptop | smoke | micro-read, micro-er, micro-powerlaw, micro-write |
| zu2 | in-process, cgo | 0.0.1 | laptop | sf1 | micro-read |
| ladybug | in-process, cgo | 0.19.1 | laptop | smoke, sf1 | micro-read, micro-er, micro-powerlaw, micro-write |
| SQLite | in-process, cgo | 3.53.4 | laptop | sf1 | micro-read |
| DuckDB | in-process, cgo | 1.4.1 | laptop | sf1 | micro-read |
| PostgreSQL | driver, pgx | 18.6 | laptop | sf1 | micro-read |
| MongoDB | driver | 8.3.7 | laptop | sf1 | micro-read |
| Neo4j | Bolt | 2026.06.0 | desktop, server3 | sf1 | micro-read, micro-er |
| Memgraph | Bolt | 3.10.0 | server3 | sf1 | micro-read |
| zu2 | in-process, cgo | 0.0.1 | server1, server2, server3, gamingpc | sf1 | micro-read |
| SQLite | in-process, cgo | 3.53.4 | server1, server2, server3, gamingpc | sf1 | micro-read |
| DuckDB | in-process, cgo | 1.4.1 | server1, server2, server3, gamingpc | sf1 | micro-read |
| PostgreSQL | driver, pgx | 18.6 | server3 | sf1 | micro-read |
| MongoDB | driver | 8.3.8 | server3 | sf1 | micro-read |

zu and ladybug both run in-process here. zu links libzu and calls it directly, ladybug links liblbug and calls it directly, and neither pays anything to reach the engine. That is the whole point of the pairing: the difference between the two columns is the engine and the query plan, not the transport.

The laptop tables and the desktop tables are different machines at different scales. Compare engines within one table. Do not read a zu number from one table against a zu number from the other, and do not put ladybug's laptop numbers next to Neo4j's desktop numbers, because nothing in that comparison is held constant.

### Seven engines, one invocation

A graph engine, a document engine, a relational engine on a wire, two relational engines in the process, and two graph engines in the process, all loaded from the same CSV and asked the same nine questions in one run of the harness. Apple silicon laptop, `micro-read` at sf1 on `grid-100x100` (checksum `fd000c2a`, 10000 nodes, 19800 edges), one worker, service-time p50, nothing tuned. Every engine's nine answers verified against the harness oracle before anything was timed.

| Query | zu2 | sqlite | sqlite-mem | duckdb | ladybug | postgres | mongodb |
| --- | --- | --- | --- | --- | --- | --- | --- |
| micro-point | 333ns | 2.6µs | 1.3µs | 57.6µs | 54.2µs | 160.2µs | 119.5µs |
| micro-point-miss | 209ns | 2.2µs | 1.0µs | 60.0µs | 56.7µs | 166.5µs | 109.8µs |
| micro-edge | 625ns | 2.9µs | 1.9µs | 114.3µs | 234.0µs | 155.8µs | 144.7µs |
| micro-khop1 | 375ns | 2.6µs | 1.6µs | 79.5µs | 189.5µs | 157.8µs | 136.3µs |
| micro-khop2 | 333ns | 4.5µs | 3.5µs | 268.0µs | 619.6µs | 151.8µs | 232.1µs |
| micro-khop3 | 375ns | 5.5µs | 4.4µs | 387.2µs | 1.06ms | 170.8µs | 393.8µs |
| micro-varlen | 292ns | 9.8µs | 8.7µs | 515.8µs | 744.6µs | 154.1µs | 161.5µs |
| micro-scan-count | 125ns | 2.7µs | 1.4µs | 42.8µs | 179.5µs | 297.2µs | 710.9µs |
| micro-scan-stats | SKIP | 155.3µs | 155.5µs | 55.0µs | 210.8µs | 408.0µs | 1.32ms |

Load time and footprint from the same run, against 392.4 KiB of source CSV:

| Engine | Load | After load |
| --- | --- | --- |
| sqlite | 41ms | 508.0 KiB |
| sqlite-mem | 39ms | 508.0 KiB |
| postgres | 42ms | 2776.0 KiB |
| duckdb | 54ms | 2560.0 KiB |
| ladybug | 101ms | 2348.0 KiB |
| mongodb | 137ms | 1264.0 KiB |
| zu2 | 575ms | 1320.0 KiB |

The zu2 load row is kept as it was measured and it is no longer current. zu2's durability setting applies to every append and this adapter loaded at the configured setting, which defaults to durable, so it waited for the device once per record where sqlite's loader commits once for the whole load. It now loads async and syncs once before anything measures the file, which puts the same load at 6.9ms here and at 12.5ms to 143.8ms on the other four machines, 3.0x to 11.4x faster than sqlite instead of slower. Nothing else in the table moved: the footprint is the same and no query path changed.

The two engines on a wire are close to flat across the small reads, because a round trip costs what it costs and it is more than any of these queries. That is the honest reading of those columns and not a criticism of either query planner: PostgreSQL answers a three hop expansion in about what it needs for a point read. The in-process engines are where the query work is visible, and there the shape is the usual one, a point read cheap and a k-hop growing with the fan-out.

Two rows are worth stopping on. MongoDB's `micro-varlen` at 161.5µs beats its own three hop join at 393.8µs, because `$graphLookup` runs the whole bounded walk server side while `micro-khop3` is three nested `$lookup` stages. Give a document store a graph operator and it uses it properly. And ladybug, the only other engine here that was built for graphs, is the fastest of the four non-zu engines on a point read and the slowest of them on three hops, which is a planner shape rather than a storage one.

The footprint table is not flattering to zu2. SQLite holds this graph in 508 KiB, MongoDB holds it as 19800 BSON documents that repeat their field names plus two compound indexes and still lands at 1264 KiB, and zu2's hybrid log needs 1320 KiB because nothing reclaims a superseded version until there is a checkpoint.

### The same nine questions on five machines

The table above is one machine, which is the weakest thing about it. The same workload then ran on the other four: three Linux servers and a Windows desktop, same dataset checksum, same verification before timing, no failures. zu2 on the left of each cell, SQLite on the right.

| Query | laptop | server1 | server2 | server3 | gamingpc |
| --- | --- | --- | --- | --- | --- |
| micro-point | 333ns / 2.6µs | 1.4µs / 7.4µs | 6.9µs / 12.7µs | 5.1µs / 10.5µs | 0.7µs / 6.7µs |
| micro-point-miss | 209ns / 2.2µs | 801ns / 6.9µs | 1.0µs / 8.3µs | 1.0µs / 7.9µs | 0.5µs / 6.5µs |
| micro-edge | 625ns / 2.9µs | 2.0µs / 8.2µs | 5.9µs / 11.3µs | 3.0µs / 10.3µs | 1.2µs / 8.2µs |
| micro-khop1 | 375ns / 2.6µs | 1.2µs / 6.9µs | 1.7µs / 11.3µs | 2.1µs / 9.6µs | 0.8µs / 7.3µs |
| micro-khop2 | 333ns / 4.5µs | 1.2µs / 9.8µs | 1.5µs / 16.5µs | 2.3µs / 19.1µs | 0.8µs / 10.4µs |
| micro-khop3 | 375ns / 5.5µs | 1.3µs / 11.4µs | 1.8µs / 20.0µs | 2.5µs / 22.1µs | 0.9µs / 11.4µs |
| micro-varlen | 292ns / 9.8µs | 1.3µs / 19.8µs | 1.5µs / 34.4µs | 1.7µs / 38.8µs | 0.8µs / 17.5µs |
| micro-scan-count | 125ns / 2.7µs | 461ns / 7.2µs | 601ns / 9.3µs | 631ns / 11.2µs | 0.4µs / 6.5µs |
| micro-scan-stats | SKIP / 155.3µs | SKIP / 618.5µs | SKIP / 1.99ms | SKIP / 1.05ms | SKIP / 218.9µs |

Read the servers for shape and the laptop and gamingpc for microseconds. All three Linux boxes were carrying unrelated work at load averages between 8 and 72, and at these latencies a busy scheduler is a large part of what gets measured. gamingpc was idle at 1 percent. server1 and gamingpc have no Docker available to the harness, so they ran the four in-process engines only, and server2 has neither Go nor Rust installed, so its binary and libzu2 were built on server1 and copied to it, same distribution and same glibc, every answer still verified on the machine that ran it.

Neo4j 2026.06.0 and Memgraph 3.10.0 ran on server3 alongside zu2 and SQLite, in one invocation, all thirty six answers verified. Neo4j answers a point read in 4.33ms and a three hop expansion in 2.46ms, Memgraph 528.7µs and 776.5µs. Both are on Bolt over loopback on a host at a load average of 37, so what those columns mostly measure is a round trip, a driver and a planner rather than either engine's storage, and the one shape worth keeping is that neither one costs meaningfully more for three hops than for one at this size.

### How to read the tables

The `zu speedup` column is the other engine's p50 divided by zu's p50. Above 1.0 means zu is faster by that factor, below 1.0 means zu is slower. A row where zu loses is set in bold, and as of the 2026-08-22 rerun there are none left in the laptop tables. Ratios are computed from the nanosecond figures in the result files, not from the rounded cells.

The laptop tables below were remeasured on 2026-08-22 on an idle machine. The first set of them was taken on 2026-08-17 under a load average near 15 from unrelated work, and every absolute figure moved down when the machine went quiet, ladybug's by 15 to 25 percent and zu's by more on the shapes it has since changed. Both engines ran inside one invocation of the harness, alternating over the same datasets in the same process, so either set is matched against itself. Repeated runs of each engine agreed within about 15 percent, which is far inside the ratios below.

### Apple silicon laptop, zu 0.0.1 against ladybug 0.19.1

Smoke scale, macOS, both columns from one run of the harness on the same machine, the same datasets, and the same process.

**micro-read**, dataset `grid-30x30` (checksum `eb8d5d60`), fidelity harness-native. ladybug answered seven of the nine queries through its kuzu dialect and the two scans through Cypher; zu answered all nine through zuQL.

| Query | zu p50 | zu p99 | ladybug p50 | ladybug p99 | zu speedup |
| --- | --- | --- | --- | --- | --- |
| micro-point | 5.5µs | 6.4µs | 92.6µs | 150.4µs | 16.8x |
| micro-point-miss | 3.2µs | 3.5µs | 90.5µs | 121.1µs | 28.6x |
| micro-edge | 8.0µs | 9.8µs | 462.6µs | 844.6µs | 57.5x |
| micro-khop1 | 6.2µs | 12.3µs | 303.1µs | 955.5µs | 49.1x |
| micro-khop2 | 9.1µs | 10.0µs | 1.05ms | 2.02ms | 115.5x |
| micro-khop3 | 9.8µs | 13.9µs | 1.87ms | 2.41ms | 189.7x |
| micro-varlen | 6.8µs | 8.3µs | 880.2µs | 1.40ms | 129.6x |
| micro-scan-count | 5.1µs | 7.4µs | 215.0µs | 324.8µs | 42.3x |
| micro-scan-stats | 8.0µs | 9.3µs | 236.2µs | 300.9µs | 29.5x |

**micro-er**, dataset `er-n1000-p0.01` (checksum `d3c97598`, 1000 nodes, 10219 edges), fidelity harness-native. Both triangle counts verified against the harness's own counting oracle, zu through zuQL and ladybug through Cypher.

| Query | zu p50 | zu p99 | ladybug p50 | ladybug p99 | zu speedup |
| --- | --- | --- | --- | --- | --- |
| micro-triangle | 346.5µs | 414.6µs | 6.09ms | 13.63ms | 17.6x |
| micro-triangle-undirected | 1.49ms | 1.59ms | 13.54ms | 14.93ms | 9.1x |

**micro-powerlaw**, dataset `powerlaw-n1000-g2.5` (checksum `82425f5a`), fidelity harness-native. Every query in the workload has a zu number.

| Query | zu p50 | zu p99 | ladybug p50 | ladybug p99 | zu speedup |
| --- | --- | --- | --- | --- | --- |
| micro-point | 6.4µs | 10.7µs | 51.5µs | 78.0µs | 8.1x |
| micro-point-miss | 3.8µs | 4.2µs | 51.1µs | 74.1µs | 13.6x |
| micro-khop1 | 6.8µs | 7.8µs | 185.4µs | 245.1µs | 27.3x |
| micro-khop2 | 10.8µs | 14.0µs | 594.6µs | 917.2µs | 54.9x |
| micro-khop3 | 11.3µs | 15.2µs | 1.10ms | 1.55ms | 97.3x |
| micro-varlen | 7.9µs | 24.1µs | 493.0µs | 713.8µs | 62.3x |
| micro-sp | 19.2µs | 38.2µs | 567.0µs | 1.02ms | 29.5x |
| micro-sp-bidir | 20.0µs | 45.8µs | 687.0µs | 3.05ms | 34.3x |
| micro-triangle | 137.8µs | 153.4µs | 1.44ms | 2.01ms | 10.4x |
| micro-triangle-undirected | 352.1µs | 403.3µs | 6.20ms | 6.65ms | 17.6x |

The two shortest-path rows are the ones that moved most between the two measurement dates, from 2.0x and 1.9x ahead to 29.5x and 34.3x. Only part of that is the quieter machine: micro-sp went from 464.5µs to 19.2µs while ladybug's went from 917.2µs to 567.0µs, so the shape got about 20x faster on zu against ladybug's 1.6x, and the rest is the search itself rather than the scheduler.

**micro-write**, dataset `lb-1k` (checksum `b974efcf`), fidelity harness-native. One property update per repetition, verified by reading the row back.

| Query | zu p50 | zu p99 | ladybug p50 | ladybug p99 | zu speedup |
| --- | --- | --- | --- | --- | --- |
| micro-set | 3.01ms | 4.03ms | 3.97ms | 8.21ms | 1.3x |

This used to be the one row where zu lost, at 14.17ms against 4.00ms, and it is now level. Four repeats of this workload put zu between 3.01ms and 3.93ms and ladybug between 3.93ms and 4.15ms, which is a spread wide enough that the honest reading is a tie rather than the 1.3x the table's own run shows. What is no longer true is the 3.5x loss.

The shape of the cost has not changed, only its size. zu's reader still only reads the sealed file, so every committed write is followed by a fold, and a fold is three more fsyncs plus a rewrite of the columns the write touched. zu's own write bench measures the same thing from the inside. The read rows above are what the same design buys, and the write row is now what it costs.

### LDBC Graphalytics kernels, sf1, zu 0.0.1 against ladybug 0.19.1

The tables above are read and write shapes. This one is the whole-graph algorithms, which is a different question: not how fast a neighbourhood comes back but how fast an engine sweeps every node it has. Both columns are one invocation of the harness on the same machine, the same datasets, and the same process, and every zu answer was validated row by row against the harness oracle before it was timed, which for PageRank is a float comparison at the Graphalytics tolerance of 1e-4 and for the label kernels is exact.

**galytics**, dataset `rmat-s14-e16` (checksum `0b9ec5e5`, 16384 nodes, 259.6K edges), fidelity spec-following. zu answered all five through zuQL, ladybug answered three through its kuzu dialect.

| Query | zu p50 | zu p99 | ladybug p50 | ladybug p99 | zu speedup |
| --- | --- | --- | --- | --- | --- |
| ga-bfs | 1.05ms | 1.26ms | 32.51ms | 33.20ms | 31.0x |
| ga-pr | 8.18ms | 8.99ms | 2.540s | 2.555s | 310.4x |
| ga-wcc | 2.02ms | 2.13ms | 110.70ms | 112.04ms | 54.9x |
| ga-cdlp | 18.09ms | 18.15ms | SKIP | SKIP | n/a |
| ga-lcc | 195.08ms | 195.12ms | SKIP | SKIP | n/a |

**galytics-w**, dataset `rmat-s14-e16-w` (checksum `a45cbe9c`), the same edge set with the uniform 1..255 weights the weighted SSSP oracle needs.

| Query | zu p50 | zu p99 | ladybug p50 | ladybug p99 | zu speedup |
| --- | --- | --- | --- | --- | --- |
| ga-sssp | 9.68ms | 10.20ms | SKIP | SKIP | n/a |

On BFS that is 225.18 M edges per second against 7.90 M, harmonic mean over the timed repetitions from the one source the kernel drew. The three skips are capability skips, not failures: ladybug ships no CDLP, no LCC and no SSSP procedure, so the harness withholds the text rather than emulate the kernel in Cypher and measure the emulation. Neo4j is absent from this table for the same kind of reason and one level up, because these kernels live in GDS and the community image the harness manages does not carry it, so the neo4j adapter declares no algorithms at all and skips the whole family.

PageRank is the widest gap and it is worth saying why, because 310x is the sort of number that usually means the two engines answered different questions. They did not. Both run to a convergence criterion rather than a fixed round count, both are checked against the same oracle at the same tolerance, and ladybug's ranks are sum-normalized in the query text because its kernel drops the mass on dangling nodes instead of redistributing it. What is left is that zu iterates over a CSR it already has in the file and ladybug builds a named projection first.

The resource figures below come from two more invocations, one engine each, since a peak resident figure only belongs to one engine when only one engine ran.

| Resource | zu 0.0.1 | ladybug 0.19.1 |
| --- | --- | --- |
| peak rss | 101.8 MiB | 233.2 MiB |
| cpu user | 2.181s | 17.644s |
| cpu sys | 74.7ms | 29.695s |
| minor faults | 6969 | 17116 |
| major faults | 203 | 401 |
| involuntary switches | 6523 | 3332059 |
| store after load | 2.8 MiB | 4.7 MiB |
| store growth over the run | 0 B | 0 B |

The system time and the involuntary switches are the same story the read tables tell, ladybug's thread pool against zu's single sweep, and here the ratio is at its largest anywhere in this file: 397x the system time and 511x the involuntary switches for a run that produced three answers to zu's five. zu spends more on the Go side than ladybug does, 362.7 MiB allocated against 220.8 MiB, because the harness materializes one row per node for five kernels rather than three.

### Why there is only one zu plane

There used to be a second zu adapter that drove the zu CLI over a pipe, one JSON frame per query, and both adapters appeared in these tables so the frame cost could be read off as a column. It measured a flat 11µs to 13µs per query, which does not scale with the query and is therefore most of the cost of the cheap reads: `micro-scan-count` spends about 5µs in the engine, so over a pipe it looked four times slower than it is.

That adapter is gone. It could only ever understate zu against an in-process engine, keeping it invited the accidental run that compares a pipe against a direct call, and every conclusion it supported is preserved above in the plane that ladybug runs on. The zu binary is still required, because libzu has no bulk-load entry point and `Load` shells out to `zu copy --reorder degree` once, outside every timed region.

### 32 core desktop, zu 0.0.1 against Neo4j 2026.06.0

sf1 scale, Ubuntu under WSL2, Neo4j in Docker on the same machine reached over Bolt at `bolt://127.0.0.1:7687`. These are historical: they were measured over the subprocess adapter that has since been removed, so every zu number here carries a frame cost of roughly 13µs that the Neo4j numbers do not have, and they predate the plan fixes in the micro-er section below. They stay because they are the only numbers against Neo4j so far, and they will be replaced by an in-process rerun.

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

### What a run costs

Every `run` prints a resource table under the latency matrix, and every result document carries the same figures. Latency answers how fast, this answers at what price: memory the engine settled at and peaked at, CPU it burned, the kernel work behind the tail, and the bytes it left on disk. Two engines with the same p99 are not equal if one of them spends four times the CPU or six times the memory getting there.

The scope of each figure is the harness process and the children it reaped, so an in-process engine reports itself in the process rows, a load helper shows up in the children rows, and a Bolt engine reports its driver only, because the server it talks to was never forked by the harness. Every counter is a delta over that engine's own run. The peak resident rows are not deltas, since the kernel keeps one high-water mark per process and never resets it, so a peak belongs to one engine only when one engine ran in that invocation, which is what the table's own footer says.

Both engines below therefore ran alone, one invocation each, same machine and same datasets as the latency tables. The CPU rows cover the whole run including the fixed 2 second warmup, so they say what each engine spent inside a fixed window rather than what one query costs.

**micro-read** on `grid-30x30`:

| Resource | zu 0.0.1 | ladybug 0.19.1 |
| --- | --- | --- |
| peak rss | 28.5 MiB | 201.3 MiB |
| cpu user | 745.9ms | 2.939s |
| cpu sys | 16.2ms | 2.409s |
| minor faults | 1389 | 12619 |
| major faults | 194 | 396 |
| involuntary switches | 3255 | 466831 |
| store after load | 2.2 MiB | 2.0 MiB |
| store growth over the run | 0 B | 0 B |

**micro-write** on `lb-1k`, the write microscope:

| Resource | zu 0.0.1 | ladybug 0.19.1 |
| --- | --- | --- |
| peak rss | 30.7 MiB | 204.1 MiB |
| cpu user | 34.4ms | 176.9ms |
| cpu sys | 60.0ms | 329.0ms |
| minor faults | 1656 | 14017 |
| major faults | 16 | 24 |
| involuntary switches | 4603 | 36510 |
| store after load | 5.8 MiB | 2.9 MiB |
| store growth over the run | 2.6 MiB | 0 B |

zu holds the graph in a seventh of the memory, takes a ninth of the minor faults, and spends a hundred and fiftieth of the system time on the read workload, where ladybug's thread pool is most of the difference in both the sys time and the involuntary switches. It also stores the graph in twice the bytes and grows its store by 2.6 MiB over 200 self-assignments, which is the fold again. Those are the two numbers to watch as the read path learns to consult overlays.

Disk read and write bytes are per-process kernel counters read from `/proc/self/io`, so they are present on Linux and absent on macOS, where the equivalent lives behind libproc and this harness stays cgo-free by default. A figure a platform cannot answer is -1 in the document and `n/a` in the table, and a row no engine could answer is dropped rather than printed as a column of `n/a`.

### What does not run yet, and what now does

A workload only produces numbers for an engine that has text in that engine's dialect chain. There is no silent fallback: if the chain has nothing, the query reports SKIP with a reason, and the reason is in the result file.

This table used to say that no labelled workload ran on zu at all, because the loader took a two column edge list and flattened every label into one table. The labelled load path landed, the dialect texts followed, and what is left is seven queries out of the whole suite.

| Workload | zu | reason |
| --- | --- | --- |
| micro-read, micro-uniform, micro-er, micro-powerlaw, micro-mix, micro-write | runs | every query |
| micro-sp, micro-sp-bidir | runs | every query |
| galytics, galytics-w, gap, g500 | runs | every query |
| snb-short | runs | all seven |
| snb-complex | runs | all six |
| snb-mix | runs | every query it draws from the three SNB families |
| snb-update | runs | all six |
| fb-read, fb-write | runs | all twelve |
| snb-bi | 4 of 5 | bi1 buckets by content length with `CASE`, which zuQL does not parse ([tamnd/zu#303](https://github.com/tamnd/zu/issues/303)) |
| linkbench | 8 of 10 | `lb-update-node` and `lb-delete-node` need a scratch object at a chosen id, and `INSERT` cannot choose one ([tamnd/zu#293](https://github.com/tamnd/zu/issues/293)) |
| lsqb | runs | all nine |

Every skip above is a text that was withheld on purpose, not a text that failed. One FAIL discards the measurement for the whole workload, so a query zu answers wrongly is left without a text and the reason is written down next to it rather than in a result file nobody reads.

### Where zu wins and where it loses

The picture across the labelled families is consistent enough to state plainly. On reads, zu is ahead almost everywhere, by 2x to 42x, and the wins are largest on the shapes that touch one neighbourhood and stop. On writes it is behind by 4x to 80x, and on multi hop expansion it is behind by up to 3x.

Read families at sf1, fast profile, both engines in process, p50 against p50:

| Family | zu range against ladybug |
| --- | --- |
| snb-short, all seven | 2.0x to 23.2x ahead |
| snb-complex, four of six | 7.2x to 41.8x ahead; ic1 3.0x behind, ic9 1.3x behind |
| snb-bi, three of four | 9.2x to 33.8x ahead; bi18 2.4x behind |
| lsqb, eight of nine | 3.3x to 54.1x ahead; q7 3.7x behind |

The places it loses have one cause each and all of them are filed. The lsqb four-cycle, q7, runs 45.49s against ladybug's 12.44s, and it is the only lsqb shape that carries a predicate over bound relationships, six pairwise inequalities between the four legs. Every other cyclic shape in the family, q5 q6 and q9, is ahead by 6.3x to 54.1x, so the cycle itself is not what costs: the predicates are read one row at a time over an intermediate the close should have cut down first ([tamnd/zu#425](https://github.com/tamnd/zu/issues/425)). Every shape that walks a bounded variable length step comes in behind: snb-ic1 at one to three hops, snb-ic9 and snb-bi18 at one to two. The unbounded shortest path shape, snb-ic13, is 41.8x ahead, which says the selector stops at the first meeting and the bounded range enumerates paths the query then discards ([tamnd/zu#302](https://github.com/tamnd/zu/issues/302)). On writes, snb-update runs 15.89ms against 3.93ms and fb-write 145.83ms against 3.97ms, because every commit folds the whole overlay and a small write pays for the size of the table it lands in ([tamnd/zu#292](https://github.com/tamnd/zu/issues/292)).

### The gap that closed

`micro-triangle-undirected` on the ER graph was the only read query in these tables where zu lost, 22.45ms against ladybug's 13.69ms. It now runs 1.49ms against 13.54ms. The query is the undirected triangle with an ordering predicate on the `id` property and a `count(DISTINCT [a.id, b.id, c.id])` on top.

The first reading of it was wrong and worth writing down. zu's own `explain_analyze` on the 1k ER graph put 16.27ms of the 29.79ms total in one place, the `b.id < c.id` filter over 217664 rows at about 75ns each, and that looked like a property read in the wrong loop. The property read was never the story. Those 217664 rows were, and they existed because the engine never intersected the closing edge: an undirected end reads two stored lists, and both of zu's planners took that as a reason to leave the close as a storage probe per candidate row, so the query enumerated every 2-path in the graph and then threw most of them away one predicate at a time.

Two changes in zu, [tamnd/zu#101](https://github.com/tamnd/zu/pull/101), fixed it. The closing edge now takes the intersection with both stored lists on each end, and the ordering predicate that filter placement had left sitting between the second hop and the close moves above it so the two can fuse. The same `explain_analyze` now totals 9.18ms, the close emits 4188 rows instead of 217664, and the filter above it costs 446.8µs instead of 16.27ms.

### Reproducing

The laptop comparison:

```
cargo build --release -p zu-cli -p zu-capi   # in the zu repo, builds the CLI and libzu
go build -tags "ladybug zuinproc" -o gb ./cmd/graph-bench
./gb run --workload micro-read --engines zu,ladybug
./gb run --workload micro-er --engines zu,ladybug
./gb run --workload micro-powerlaw --engines zu,ladybug
./gb run --workload micro-write --engines zu,ladybug
```

For the resource tables, run one engine per invocation instead, because peak resident set is a process high-water mark the kernel never resets:

```
./gb run --workload micro-read --engines zu
./gb run --workload micro-read --engines ladybug
```

The desktop comparison:

```
go build -tags bolt -o gb ./cmd/graph-bench
docker compose -f docker/docker-compose.yml up -d neo4j
NEO4J_URI=bolt://127.0.0.1:7687 ./gb run --workload micro-read --engines zu,neo4j
NEO4J_URI=bolt://127.0.0.1:7687 ./gb run --workload micro-er --engines zu,neo4j
```

Setting `NEO4J_URI` is not optional today. Without it the harness starts its own managed container, and on both hosts tried that container came up on a mapped port the harness then could not reach, which fails the run before any query is measured.

Any saved result re-renders without re-running:

```
./gb report --file results/micro-er/smoke/20260817T093018Z-ladybug-inproc-d3c97598.json
./gb compare --files results/micro-er/smoke/20260817T093016Z-zu-inproc-d3c97598.json,results/micro-er/smoke/20260817T093018Z-ladybug-inproc-d3c97598.json
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
- `ab --before before/ --after after/ --engine zu` -- compares two builds of one engine that were run against each other on the same machine, best of N per side, and exits 2 when a query is at or over `--factor`.

### Noise, and what a regression number is worth

A regression gate compares two numbers and calls the difference a change in the code. That is only true when the machine would have produced the same number twice, and a developer laptop often will not. Run `noise` before trusting a failed gate: it measures the spread across repeated runs of a single binary, which is the floor under every regression the gate can report. If the floor is at or above the regression factor, the gate cannot tell a slower engine from a busier machine, and it says so instead of guessing.

Passing that measured floor to `gate --noise-floor` moves differences inside it out of the violations list and into a separate section, reported and not ruled on. They are not excused: a finding inside the floor is the harness saying this run cannot answer the question, which is a different thing from saying the answer is no. Budget checks and verification integrity are unaffected, because neither is a comparison between two runs. The default is zero, no floor, which is the right setting for the controlled machine the full matrix runs on.

### A number the run can hold

A p99 over a whole run is one number for a thing that changed while it was being measured. A run that is fast for ten seconds and slower for the next fifty publishes a p99 somewhere in between, and nothing in the report says the engine was never doing it. A store that fragments, a cache that fills, a compaction backlog that builds all look like that.

So a run long enough to hold two ten-second windows is cut into them and reported per class as the p99 of the first window, of the worst, and the trend from the run's first half to its second. The trend is the one to gate on, and `gate --drift-factor` fails a run whose second half is more than a tenth slower than its first. The worst window is for reading, not for gating: it is the largest of however many windows the run held, so it beats the first window even on a run that never changed, and a check built on it would get stricter every time the run got longer. The full per-window series is in the result JSON, which is what separates a run that drifted from a run that wobbled.

### Two builds, one machine

`gate --baseline` compares today's run against a run recorded on some other day. That holds while the machine is the same machine, and stops holding the moment something else is compiling on it: every query drifts by the same tenth at once and the gate reads the load as a regression in code that never touched the read path. `ab` is the answer to that. It takes two lineages, one per build, and compares them query by query, so whatever the machine was doing lands on both sides of the comparison instead of on one.

Two things are the caller's to get right. The first is that the two lineages differ in the build and in nothing else: same harness binary, same workload, same scale, same dataset. The second is the ordering. Run the two sides alternately and swap which one goes first every round, because load that climbs through a round otherwise lands entirely on whichever side ran second, and that ordering alone can manufacture a ten percent regression.

Each side is reduced to its best value per query rather than its average. The default metric is `min`, the fastest single call, because load only ever adds time to a call: the smallest observation is the one closest to the work itself, and it is the one statistic a busy machine cannot inflate. `--metric p50` and `--metric p99` are there when the question is about the typical call or the tail, and both are noisier for the same reason.

Under the latency table comes a cost table: CPU, peak resident set, store bytes and store growth, best of N per side on the same terms. Those rows are reported and not gated, so the exit code stays a statement about latency, and they are there for two reasons. One is that a change which buys a tenth of a millisecond with a third more CPU should have to show it. The other is that on a machine which is doing something else, the counters are steadier than the clock, and they are often the row that settles the question.

A worked example, six rounds with the order swapped every round, and the same libzu built from two commits:

```
for r in 1 2 3 4 5 6; do
  for w in micro-read micro-uniform snb-short; do
    if [ $((r % 2)) -eq 0 ]; then
      ./gb-new run --workload $w --engines zu --out after
      ./gb-old run --workload $w --engines zu --out before
    else
      ./gb-old run --workload $w --engines zu --out before
      ./gb-new run --workload $w --engines zu --out after
    fi
  done
done
./gb ab --before before --after after --engine zu
```

What is not yet wired:

- Managed containers -- the Bolt engines only work against a server the caller points them at with `NEO4J_URI` or `MEMGRAPH_URI`. Left to start their own container, both fail to bind a reachable port.
- Dialect coverage -- zu has text for the micro family only, so the labelled workloads skip. See the coverage table above.
- Neo4j on the in-process plane -- the desktop tables were measured over the subprocess adapter that has since been removed, so they need a rerun before they can sit next to the laptop tables.
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

The default build is pure Go with no cgo and no dependency beyond the standard library and the CLI framework. Every engine adapter needs either a driver or cgo, so all of them sit behind build tags and none enters the default binary. zu is still registered there, and a run against it fails at Start with the build line to use, which is a clearer answer than an unknown engine name.

To include the Bolt adapters (Neo4j, Memgraph):

```
go build -tags bolt ./...
```

To include the in-process ladybug adapter, which links liblbug through cgo:

```
go build -tags ladybug ./...
```

Anywhere liblbug is not under `/opt/homebrew`, name the header and the library file:

```
CGO_CFLAGS="-I$LBUG_INCLUDE" CGO_LDFLAGS="$LBUG_LIB/liblbug.dylib" go build -tags ladybug ./...
```

The library is named by path rather than through `-L` and `-llbug` because a `-L` is global to the link and not local to the package that asked for it. Homebrew ships its own DuckDB in the same directory, go-duckdb ships a different one inside its module, and with both tags on the Homebrew copy won and the link failed. Naming the file keeps `-tags duckdb,ladybug` building, which is what a run that compares the two in one invocation needs.

To make zu runnable, which links libzu through cgo:

```
go build -tags zuinproc ./...
```

It expects a sibling zu checkout built with `cargo build --release -p zu-cli -p zu-capi`, the CLI for the bulk load and libzu for every query. Anywhere else, point the flags at the header and the library:

```
CGO_CFLAGS="-I$ZU_INCLUDE" CGO_LDFLAGS="-L$ZU_LIB -lzu -Wl,-rpath,$ZU_LIB" go build -tags zuinproc ./...
```

To make zu2 runnable, which links libzu2 through cgo and expects the same sibling checkout built with `cargo build --release -p zu2-capi`:

```
go build -tags zu2inproc ./...
```

To include the relational adapters, each of which links its engine's C library through cgo:

```
go build -tags sqlite ./...
go build -tags duckdb ./...
```

SQLite registers three engines (`sqlite`, `sqlite-sync`, `sqlite-mem`) and DuckDB two (`duckdb`, `duckdb-mem`), because the durability setting and where the database lives move the numbers by more than most engines differ from each other and a report column should say which one it is. Both carry their engine's library rather than linking a system one, so the pinned version is the driver's.

PostgreSQL needs no tag. Its driver, pgx, is pure Go, so `postgres` is in every build and what it needs instead is a server:

```
GRAPH_BENCH_PG_DSN='postgres://bench:bench@127.0.0.1:5432/bench?sslmode=disable' ./gb run --workload micro-read --engines postgres
```

Without a DSN in `GRAPH_BENCH_PG_DSN` or `DATABASE_URL`, `run` starts the pinned container itself unless `--no-docker` is set. It is the only relational engine here that is not in the harness process, and the round trip is most of what its small-read numbers say.

MongoDB is the same story with a different wire:

```
GRAPH_BENCH_MONGO_URI=mongodb://127.0.0.1:27017 ./gb run --workload micro-read --engines mongodb
```

It answers through the `mongo` dialect, which is an aggregation pipeline rather than a query language, so a hop is a correlated `$lookup` and a bounded walk is `$graphLookup`. Nine of the thirteen micro queries have one; the rest SKIP.

Tags combine, and a head-to-head build is the union of the ones it needs:

```
go build -tags 'zu2inproc sqlite duckdb' -o gb ./cmd/graph-bench
```

## Spec

The complete design lives at `notes/Spec/2060/bench/`. Start with `00-overview.md` for the mission and the fairness contract, `02-architecture.md` for the layout, and `10-roadmap.md` for what ships when.

## License

Apache-2.0.
