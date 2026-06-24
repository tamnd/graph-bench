// Package graphalytics registers the LDBC Graphalytics algorithm workload: the
// six whole-graph algorithms on a static graph (BFS, PageRank, weakly connected
// components, community detection by label propagation, local clustering
// coefficient, and single-source shortest paths). It calls workload.Register in
// its init so any binary that imports it (blank or otherwise) gets the
// "graphalytics" workload in the registry.
//
// These are class Analytical and they are the deferred analytical tier: most
// graph databases do not run them as queries, they run them as procedures (a CALL
// in Cypher, the GDS library in Neo4j) or not at all. The seam is
// Capabilities.Algorithms (doc 03 section 1.5): an engine that exposes an
// algorithm runs the CALL text, an engine that does not shows a blank cell, which
// is itself information. The Cypher texts here are representative GDS-style
// procedure calls keyed by dialect, the per-engine procedure being the seam to
// refine; the value the suite leans on is the engine-independent reference, an
// LDBC-faithful implementation of each algorithm over the canonical CSV that two
// engines validate against exactly (the determinism choices: distance-not-parent
// BFS, smallest-label CDLP, smallest-member WCC labels).
//
// See notes/Spec/2060/bench/05-workloads.md section 8.2.
package graphalytics

import (
	"fmt"
	"strconv"

	"github.com/tamnd/graph-bench/target"
	"github.com/tamnd/graph-bench/workload"
)

func init() {
	workload.Register(graphalyticsWorkload)
}

// source is the fixed BFS and SSSP start node. Node 0 exists in every synthetic
// dataset (the generators emit a dense id space from 0), so the source is
// self-contained and the run needs no curated pool.
const source = "0"

// pageRankDamping, pageRankTol, and pageRankMaxIter are the LDBC PageRank
// parameters: the standard 0.85 damping, a convergence tolerance at the float
// comparison floor, and a cap so the reference always terminates.
const (
	pageRankDamping = 0.85
	pageRankTol     = 1e-12
	pageRankMaxIter = 100
	cdlpRounds      = 10
)

// graphalyticsWorkload runs the six algorithms on a power-law graph, the skewed
// degree distribution that makes PageRank and community detection non-trivial.
// Every query runs in isolation (no Mix): an algorithm is timed end to end, not
// mixed with point reads.
var graphalyticsWorkload = &workload.Workload{
	Name:    "graphalytics",
	Title:   "LDBC Graphalytics algorithms (BFS, PageRank, WCC, CDLP, LCC, SSSP)",
	Dataset: "powerlaw",
	Queries: []*workload.WorkloadQuery{
		bfsQuery(), pageRankQuery(), wccQuery(), cdlpQuery(), lccQuery(), ssspQuery(),
	},
}

// idValue parses a node id token to an int64 so the reference returns the id as a
// number, the way an engine returns n.id; the synthetic ids are dense integers.
func idValue(id string) (int64, error) {
	return strconv.ParseInt(id, 10, 64)
}

// bfsQuery is breadth-first search from a fixed source, returning each reachable
// node's level (distance in edges). Distance, not parent, so two engines agree.
func bfsQuery() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "ga-bfs",
		Class: target.Analytical,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `CALL gds.bfs.stream({nodeProjection: 'Node', relationshipProjection: 'EDGE', sourceNode: $source}) YIELD path
WITH nodes(path) AS ns
UNWIND range(0, size(ns) - 1) AS level
RETURN ns[level].id AS id, level ORDER BY id`,
		},
		Params: workload.NewFixed(target.Params{"source": source}),
		Reference: workload.RefStrategy{
			Compute: func(ds target.Dataset, _ target.Params) (*target.Answer, error) {
				g, err := workload.LoadGraph(ds)
				if err != nil {
					return nil, fmt.Errorf("ga-bfs reference: %w", err)
				}
				levels, ok := g.BFSLevels(source)
				if !ok {
					return nil, fmt.Errorf("ga-bfs: source %q not in graph", source)
				}
				rows := make([][]target.Value, 0, len(levels))
				for _, l := range levels {
					id, err := idValue(l.ID)
					if err != nil {
						return nil, fmt.Errorf("ga-bfs: id %q not numeric: %w", l.ID, err)
					}
					rows = append(rows, []target.Value{id, l.Val})
				}
				return &target.Answer{Columns: []string{"id", "level"}, Rows: rows}, nil
			},
			Compare: workload.CompareSpec{CoerceNum: true},
		},
	}
}

// pageRankQuery scores every node by PageRank to the float tolerance.
func pageRankQuery() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "ga-pagerank",
		Class: target.Analytical,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `CALL gds.pageRank.stream({nodeProjection: 'Node', relationshipProjection: 'EDGE', dampingFactor: 0.85}) YIELD nodeId, score
MATCH (n) WHERE id(n) = nodeId RETURN n.id AS id, score ORDER BY id`,
		},
		Params: workload.NewFixed(nil),
		Reference: workload.RefStrategy{
			Compute: func(ds target.Dataset, _ target.Params) (*target.Answer, error) {
				g, err := workload.LoadGraph(ds)
				if err != nil {
					return nil, fmt.Errorf("ga-pagerank reference: %w", err)
				}
				scores := g.PageRank(pageRankDamping, pageRankTol, pageRankMaxIter)
				rows := make([][]target.Value, 0, len(scores))
				for _, s := range scores {
					id, err := idValue(s.ID)
					if err != nil {
						return nil, fmt.Errorf("ga-pagerank: id %q not numeric: %w", s.ID, err)
					}
					rows = append(rows, []target.Value{id, s.Val})
				}
				return &target.Answer{Columns: []string{"id", "score"}, Rows: rows}, nil
			},
			Compare: workload.CompareSpec{CoerceNum: true},
		},
	}
}

// wccQuery labels every node with its weakly connected component, the component
// named by its smallest member id.
func wccQuery() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "ga-wcc",
		Class: target.Analytical,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `CALL gds.wcc.stream({nodeProjection: 'Node', relationshipProjection: {EDGE: {orientation: 'UNDIRECTED'}}}) YIELD nodeId, componentId
MATCH (n) WHERE id(n) = nodeId
WITH componentId, min(n.id) AS label, collect(n.id) AS members
UNWIND members AS member
RETURN member AS id, label AS component ORDER BY id`,
		},
		Params: workload.NewFixed(nil),
		Reference: workload.RefStrategy{
			Compute: func(ds target.Dataset, _ target.Params) (*target.Answer, error) {
				g, err := workload.LoadGraph(ds)
				if err != nil {
					return nil, fmt.Errorf("ga-wcc reference: %w", err)
				}
				return labelAnswer(g.WeaklyConnectedComponents(), "component")
			},
			Compare: workload.CompareSpec{CoerceNum: true},
		},
	}
}

// cdlpQuery assigns every node a community by label propagation, smallest-label
// tie-break, a fixed round count.
func cdlpQuery() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "ga-cdlp",
		Class: target.Analytical,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `CALL gds.labelPropagation.stream({nodeProjection: 'Node', relationshipProjection: {EDGE: {orientation: 'UNDIRECTED'}}, maxIterations: 10}) YIELD nodeId, communityId
MATCH (n) WHERE id(n) = nodeId RETURN n.id AS id, communityId AS community ORDER BY id`,
		},
		Params: workload.NewFixed(nil),
		Reference: workload.RefStrategy{
			Compute: func(ds target.Dataset, _ target.Params) (*target.Answer, error) {
				g, err := workload.LoadGraph(ds)
				if err != nil {
					return nil, fmt.Errorf("ga-cdlp reference: %w", err)
				}
				return labelAnswer(g.LabelPropagation(cdlpRounds), "community")
			},
			Compare: workload.CompareSpec{CoerceNum: true},
		},
	}
}

// lccQuery computes the local clustering coefficient of every node.
func lccQuery() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "ga-lcc",
		Class: target.Analytical,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `CALL gds.localClusteringCoefficient.stream({nodeProjection: 'Node', relationshipProjection: 'EDGE'}) YIELD nodeId, localClusteringCoefficient
MATCH (n) WHERE id(n) = nodeId RETURN n.id AS id, localClusteringCoefficient AS coefficient ORDER BY id`,
		},
		Params: workload.NewFixed(nil),
		Reference: workload.RefStrategy{
			Compute: func(ds target.Dataset, _ target.Params) (*target.Answer, error) {
				g, err := workload.LoadGraph(ds)
				if err != nil {
					return nil, fmt.Errorf("ga-lcc reference: %w", err)
				}
				coeffs := g.LocalClustering()
				rows := make([][]target.Value, 0, len(coeffs))
				for _, c := range coeffs {
					id, err := idValue(c.ID)
					if err != nil {
						return nil, fmt.Errorf("ga-lcc: id %q not numeric: %w", c.ID, err)
					}
					rows = append(rows, []target.Value{id, c.Val})
				}
				return &target.Answer{Columns: []string{"id", "coefficient"}, Rows: rows}, nil
			},
			Compare: workload.CompareSpec{CoerceNum: true},
		},
	}
}

// ssspQuery computes the single-source shortest-path distance from a fixed source
// to every reachable node. The synthetic graph is unweighted, so the distance is
// the BFS level in float form; it runs as its own query because an engine reaches
// it through a weighted-path procedure measured separately from BFS.
func ssspQuery() *workload.WorkloadQuery {
	return &workload.WorkloadQuery{
		ID:    "ga-sssp",
		Class: target.Analytical,
		Texts: map[workload.Dialect]string{
			workload.Cypher: `CALL gds.allShortestPaths.dijkstra.stream({nodeProjection: 'Node', relationshipProjection: 'EDGE', sourceNode: $source}) YIELD targetNode, totalCost
MATCH (n) WHERE id(n) = targetNode RETURN n.id AS id, totalCost AS distance ORDER BY id`,
		},
		Params: workload.NewFixed(target.Params{"source": source}),
		Reference: workload.RefStrategy{
			Compute: func(ds target.Dataset, _ target.Params) (*target.Answer, error) {
				g, err := workload.LoadGraph(ds)
				if err != nil {
					return nil, fmt.Errorf("ga-sssp reference: %w", err)
				}
				dists, ok := g.SSSPUnit(source)
				if !ok {
					return nil, fmt.Errorf("ga-sssp: source %q not in graph", source)
				}
				rows := make([][]target.Value, 0, len(dists))
				for _, d := range dists {
					id, err := idValue(d.ID)
					if err != nil {
						return nil, fmt.Errorf("ga-sssp: id %q not numeric: %w", d.ID, err)
					}
					rows = append(rows, []target.Value{id, d.Val})
				}
				return &target.Answer{Columns: []string{"id", "distance"}, Rows: rows}, nil
			},
			Compare: workload.CompareSpec{CoerceNum: true},
		},
	}
}

// labelAnswer turns a label-valued algorithm result into an Answer with an id and
// a label column, parsing the id token to a number.
func labelAnswer(labels []workload.NodeLabel, col string) (*target.Answer, error) {
	rows := make([][]target.Value, 0, len(labels))
	for _, l := range labels {
		id, err := idValue(l.ID)
		if err != nil {
			return nil, fmt.Errorf("graphalytics: id %q not numeric: %w", l.ID, err)
		}
		rows = append(rows, []target.Value{id, l.Label})
	}
	return &target.Answer{Columns: []string{"id", col}, Rows: rows}, nil
}
