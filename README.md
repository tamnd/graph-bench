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

## Status

Scaffold. The command tree, the package layout, and the smoke CI are in place; the engine adapters, datasets, workloads, measurement, and gates land over the milestones in the spec roadmap. The verbs (`generate`, `run`, `compare`, `report`, `gate`) are present but not yet wired up.

## Build

```
go build ./...
go test ./...
```

The default build is pure Go with no cgo and no dependency beyond the standard library, the CLI framework, and (once the adapter lands) `gr`. Adapters for other engines sit behind build tags (`bolt`, `kuzu`, `duckpgq`, `age`) so they never enter the default build.

## Spec

The complete design lives at `notes/Spec/2060/bench/`. Start with `00-overview.md` for the mission and the fairness contract, `02-architecture.md` for the layout, and `10-roadmap.md` for what ships when.

## License

Apache-2.0.
