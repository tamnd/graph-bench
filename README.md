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

Every number in the laptop tables came out of this harness on 2026-08-17, and the desktop tables date from 2026-08-12. Each one is service-time latency at the stated percentile, after a fixed 2 second warmup, with no engine tuning (`tuned=false` in the result files). Nothing is timed before it is verified: the harness computes the answer itself from the canonical CSV and compares it to what the engine returned, and a query that fails verification is reported as a failure instead of a latency. The verification dialect is recorded per query, so a fast number can always be traced to the text the engine actually ran.

The result JSON for each table is under `results/<workload>/<scale>/`, named by timestamp, engine, plane, and dataset checksum.

### Which engine ran where

| Engine | Plane | Version | Machine | Scale | Workloads measured |
| --- | --- | --- | --- | --- | --- |
| zu | in-process, cgo | 0.0.1 | laptop | smoke | micro-read, micro-er, micro-powerlaw, micro-write |
| ladybug | in-process, cgo | 0.19.1 | laptop | smoke | micro-read, micro-er, micro-powerlaw, micro-write |
| Neo4j | Bolt | 2026.06.0 | desktop | sf1 | micro-read, micro-er |
| Memgraph | Bolt | not measured | desktop | sf1 | none, see below |

zu and ladybug both run in-process here. zu links libzu and calls it directly, ladybug links liblbug and calls it directly, and neither pays anything to reach the engine. That is the whole point of the pairing: the difference between the two columns is the engine and the query plan, not the transport.

The laptop tables and the desktop tables are different machines at different scales. Compare engines within one table. Do not read a zu number from one table against a zu number from the other, and do not put ladybug's laptop numbers next to Neo4j's desktop numbers, because nothing in that comparison is held constant.

### How to read the tables

The `zu speedup` column is the other engine's p50 divided by zu's p50. Above 1.0 means zu is faster by that factor, below 1.0 means zu is slower. Rows in **bold** are the ones where zu loses. Ratios are computed from the nanosecond figures in the result files, not from the rounded cells.

The laptop was not a quiet machine on 2026-08-17: it carried a load average near 15 from unrelated work. Both engines ran inside one invocation of the harness, alternating over the same datasets in the same process, so the comparison is still matched, but the absolute microseconds would be lower on an idle machine. Repeated runs of each engine agreed within about 15 percent, which is far inside the ratios below.

### Apple silicon laptop, zu 0.0.1 against ladybug 0.19.1

Smoke scale, macOS, both columns from one run of the harness on the same machine, the same datasets, and the same process.

**micro-read**, dataset `grid-30x30` (checksum `eb8d5d60`), fidelity harness-native. ladybug answered seven of the nine queries through its kuzu dialect and the two scans through Cypher; zu answered all nine through zuQL.

| Query | zu p50 | zu p99 | ladybug p50 | ladybug p99 | zu speedup |
| --- | --- | --- | --- | --- | --- |
| micro-point | 6.2µs | 6.9µs | 107.8µs | 174.5µs | 17.5x |
| micro-point-miss | 3.4µs | 3.8µs | 105.2µs | 696.9µs | 30.8x |
| micro-edge | 9.7µs | 22.2µs | 593.5µs | 1.04ms | 61.4x |
| micro-khop1 | 6.7µs | 20.1µs | 414.7µs | 654.0µs | 61.8x |
| micro-khop2 | 11.0µs | 31.5µs | 1.35ms | 2.41ms | 123.5x |
| micro-khop3 | 11.5µs | 12.0µs | 2.27ms | 3.79ms | 196.9x |
| micro-varlen | 8.7µs | 10.5µs | 843.1µs | 7.21ms | 97.3x |
| micro-scan-count | 5.4µs | 7.5µs | 491.5µs | 2.56ms | 91.4x |
| micro-scan-stats | 11.9µs | 16.7µs | 471.5µs | 1.81ms | 39.7x |

**micro-er**, dataset `er-n1000-p0.01` (checksum `d3c97598`, 1000 nodes, 10219 edges), fidelity harness-native. Both triangle counts verified against the harness's own counting oracle, zu through zuQL and ladybug through Cypher.

| Query | zu p50 | zu p99 | ladybug p50 | ladybug p99 | zu speedup |
| --- | --- | --- | --- | --- | --- |
| micro-triangle | 953.8µs | 2.92ms | 8.63ms | 13.08ms | 9.1x |
| micro-triangle-undirected | 2.87ms | 5.33ms | 20.99ms | 28.40ms | 7.3x |

**micro-powerlaw**, dataset `powerlaw-n1000-g2.5` (checksum `82425f5a`), fidelity harness-native. The two shortest-path queries used to SKIP on zu and now run, so this is the first table where every query in the workload has a zu number.

| Query | zu p50 | zu p99 | ladybug p50 | ladybug p99 | zu speedup |
| --- | --- | --- | --- | --- | --- |
| micro-point | 6.8µs | 8.2µs | 107.5µs | 179.2µs | 15.9x |
| micro-point-miss | 6.4µs | 23.6µs | 101.5µs | 140.4µs | 15.8x |
| micro-khop1 | 10.8µs | 23.2µs | 516.1µs | 3.23ms | 47.8x |
| micro-khop2 | 12.1µs | 35.3µs | 1.25ms | 3.06ms | 103.4x |
| micro-khop3 | 13.0µs | 19.8µs | 2.05ms | 3.36ms | 157.1x |
| micro-varlen | 9.0µs | 72.7µs | 631.5µs | 1.29ms | 70.5x |
| micro-sp | 464.5µs | 895.5µs | 917.2µs | 2.04ms | 2.0x |
| micro-sp-bidir | 830.0µs | 1.32ms | 1.55ms | 2.66ms | 1.9x |
| micro-triangle | 240.3µs | 297.1µs | 2.60ms | 4.75ms | 10.8x |
| micro-triangle-undirected | 758.6µs | 1.21ms | 11.40ms | 17.14ms | 15.0x |

**micro-write**, dataset `lb-1k` (checksum `b974efcf`), fidelity harness-native. One property update per repetition, verified by reading the row back.

| Query | zu p50 | zu p99 | ladybug p50 | ladybug p99 | zu speedup |
| --- | --- | --- | --- | --- | --- |
| **micro-set** | 14.17ms | 21.95ms | 4.00ms | 6.41ms | 0.3x |

This is the one row where zu loses, and it loses by 3.5x. The reason is in the engine, not in the harness: zu's reader only reads the sealed file, so every committed write is followed by a fold, and a fold is three more fsyncs plus a rewrite of the columns the write touched. zu's own write bench measures the same thing from the inside, four fdatasyncs and about 1.5 MB written per single-cell SET on a 10k row table. The read rows above are what the same design buys.

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
| peak rss | 29.8 MiB | 197.8 MiB |
| cpu user | 1.004s | 2.467s |
| cpu sys | 36.3ms | 1.716s |
| minor faults | 1795 | 15739 |
| major faults | 115 | 401 |
| involuntary switches | 6279 | 228615 |
| store after load | 4.0 MiB | 2.0 MiB |
| store growth over the run | 0 B | 0 B |

**micro-write** on `lb-1k`, the write microscope:

| Resource | zu 0.0.1 | ladybug 0.19.1 |
| --- | --- | --- |
| peak rss | 36.1 MiB | 196.7 MiB |
| cpu user | 159.1ms | 289.4ms |
| cpu sys | 228.9ms | 359.2ms |
| minor faults | 2457 | 14233 |
| major faults | 111 | 47 |
| involuntary switches | 8458 | 43863 |
| store after load | 5.5 MiB | 2.9 MiB |
| store growth over the run | 512.0 KiB | 0 B |

zu holds the graph in a sixth of the memory, takes an eighth of the minor faults, and spends a fortieth of the system time on the read workload, where ladybug's thread pool is most of the difference in both the sys time and the involuntary switches. It also stores the graph in twice the bytes and grows its store by two blocks over 200 self-assignments, which is the fold again. Those are the two numbers to watch as the read path learns to consult overlays.

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

`micro-triangle-undirected` on the ER graph was the only read query in these tables where zu lost, 22.45ms against ladybug's 13.69ms. It now runs 2.87ms against 20.99ms. The query is the undirected triangle with an ordering predicate on the `id` property and a `count(DISTINCT [a.id, b.id, c.id])` on top.

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
