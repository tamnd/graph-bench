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

The core is in place. Milestones M1-M7 are merged; M8 (first published cross-engine result) is in progress.

What works today:

- `generate` -- materializes any of five synthetic graph types (uniform, power-law, ER, grid, RMAT) to the canonical CSV layout with a content-verified manifest.
- `list workloads` -- shows all registered workloads (micro, lsqb, snb-short, snb-complex, snb-write, snb-mix).
- `list engines` -- shows the registered engine adapters and their build tags.
- `report --file result.json` -- re-renders any saved JSON result in table, Markdown, CSV, or JSON.
- `compare --files a.json,b.json` -- puts two or more result sets side by side with optional Bolt plane-overhead section.
- `gate --file result.json --point-read-budget 1ms` -- checks p99 against per-class budgets and exits 2 on violations.

What is not yet wired:

- `run` -- flag surface is complete; engine execution requires an adapter import. The gr in-process adapter exists (`adapter/gr`); the run command stub will be connected in the next slice.
- LDBC SNB SF1 pin -- the URL and checksums in `dataset/ldbc/pins/snb-sf1.json` are placeholders until the first verified dataset run.
- First published cross-engine result in the lineage.

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

The default build is pure Go with no cgo and no dependency beyond the standard library, the CLI framework, and `gr`. Adapters for other engines sit behind build tags (`bolt`, `kuzu`, `duckpgq`, `age`) so they never enter the default binary.

To include the Bolt adapters (Neo4j, Memgraph, gr-bolt):

```
go build -tags bolt ./...
```

## Spec

The complete design lives at `notes/Spec/2060/bench/`. Start with `00-overview.md` for the mission and the fairness contract, `02-architecture.md` for the layout, and `10-roadmap.md` for what ships when.

## License

Apache-2.0.
