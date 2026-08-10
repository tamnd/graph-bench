//go:build bolt

// Package memgraph is the adapter for Memgraph (memgraph/memgraph-mage)
// over the shared Bolt plane (engine/bolt) — the same code path as the
// Neo4j adapter, per ADR-10. The adapter contract is
// notes/Spec/2064g/bench/04-adapters.md section 5.
//
// Memgraph-specific differences from Neo4j:
//   - No named databases: sessions use the driver default ("").
//   - Empty auth by default (community mode).
//   - In-memory by default: Capabilities.Persistent is false, so cold-cache
//     runs SKIP with reason unless the server is configured with
//     --data-recovery.
//   - Bulk load is batched UNWIND ... CREATE over Bolt with a per-label
//     `CREATE INDEX ON :Label(id)` (Memgraph index syntax).
//   - MAGE procedures provide native whole-graph kernels (pagerank, bfs,
//     wcc, sssp, bc, cdlp), declared in Capabilities.Algorithms.
//
// Build tag: bolt. Use -tags bolt to compile this package.
package memgraph

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/engine/bolt"
)

// unwindBatchSize is the number of CSV rows per UNWIND statement.
const unwindBatchSize = 500

// New returns the Memgraph engine descriptor. Nothing happens until Start.
func New() engine.Engine { return mgEngine{} }

type mgEngine struct{}

func (mgEngine) Info() engine.Info {
	return engine.Info{
		Name:  "memgraph",
		Plane: engine.Bolt,
		// MAGE first: the analytics texts are written against Memgraph's
		// procedure library, and this is the only engine that has it.
		Dialects: []engine.Dialect{engine.MAGE, engine.Cypher},
		Caps: engine.Capabilities{
			Transactions:   true,
			BulkLoad:       true,
			Deletes:        true,
			VarLengthPaths: true,
			ShortestPaths:  true,
			PathPredicates: true,
			Algorithms:     []string{"pagerank", "bfs", "wcc", "sssp", "bc", "cdlp"}, // MAGE
			MaxConcurrency: 0,
			Persistent:     false, // in-memory by default; cold runs SKIP
		},
	}
}

// resolveURI resolves the Bolt URI: Config["uri"], then $MEMGRAPH_URI,
// then bolt://127.0.0.1:7688 (the docker-compose host port).
func resolveURI(cfg engine.Config) string {
	def := "bolt://127.0.0.1:7688"
	if v := os.Getenv("MEMGRAPH_URI"); v != "" {
		def = v
	}
	return cfg.Get("uri", def)
}

func (mgEngine) Start(ctx context.Context, cfg engine.Config) (engine.Session, error) {
	uri := resolveURI(cfg)
	pool, err := bolt.Open(ctx, uri, "", "", "")
	if err != nil {
		return nil, fmt.Errorf("memgraph: open %s: %w", uri, err)
	}
	if err := pool.Ping(ctx); err != nil {
		_ = pool.Close(ctx)
		return nil, fmt.Errorf("memgraph: unreachable at %s (is the server up? see docker/docker-compose.yml): %w", uri, err)
	}
	return &session{pool: pool, cfg: cfg}, nil
}

// session is a live connection to a started Memgraph.
type session struct {
	pool *bolt.Pool
	cfg  engine.Config
}

func (s *session) Version(ctx context.Context) (string, error) {
	return s.pool.Version(ctx)
}

func (s *session) Exec(ctx context.Context, op engine.Op) (engine.Result, error) {
	return s.pool.Run(ctx, op)
}

func (s *session) Begin(ctx context.Context, mode engine.AccessMode) (engine.Tx, error) {
	tx, err := s.pool.Begin(ctx, mode)
	if err != nil {
		return nil, err
	}
	return tx, nil
}

func (s *session) Close(ctx context.Context) error {
	return s.pool.Close(ctx)
}

// Load bulk-loads the dataset via batched UNWIND ... CREATE with a
// per-label index on id. Method is always "unwind"; BytesOnDisk is -1
// (in-memory engine, F6: honest reporting).
func (s *session) Load(ctx context.Context, ds engine.Dataset) (engine.LoadStats, error) {
	start := time.Now()

	if err := s.wipe(ctx); err != nil {
		return engine.LoadStats{}, err
	}

	if stmts := ds.Statements(); len(stmts) > 0 {
		for _, st := range stmts {
			if err := s.runWrite(ctx, st); err != nil {
				return engine.LoadStats{}, fmt.Errorf("memgraph: setup statement: %w", err)
			}
		}
		return engine.LoadStats{Duration: time.Since(start), BytesOnDisk: -1, Method: "statements"}, nil
	}

	schema := ds.Schema()
	var nodes, edges int64

	// Nodes first, then the index per label, then edges: without the index
	// every endpoint MATCH in the edge load is a full scan.
	for _, label := range sortedKeys(schema.Nodes) {
		ns := schema.Nodes[label]
		files, err := ds.NodeFiles(label)
		if err != nil {
			return engine.LoadStats{}, fmt.Errorf("memgraph: node files %s: %w", label, err)
		}
		types := bolt.PropTypes(append([]engine.Column{ns.ID}, ns.Properties...))
		for _, f := range files {
			cols, rows, err := bolt.ReadCSV(f, types)
			if err != nil {
				return engine.LoadStats{}, err
			}
			n, err := s.loadUnwind(ctx, cols, rows, label, true, "", "")
			if err != nil {
				return engine.LoadStats{}, fmt.Errorf("memgraph: load nodes %s from %s: %w", label, filepath.Base(f), err)
			}
			nodes += n
		}
		if err := s.createIndex(ctx, label); err != nil {
			return engine.LoadStats{}, err
		}
	}

	for _, typ := range sortedKeys(schema.Rels) {
		rs := schema.Rels[typ]
		files, err := ds.RelFiles(typ)
		if err != nil {
			return engine.LoadStats{}, fmt.Errorf("memgraph: rel files %s: %w", typ, err)
		}
		types := bolt.PropTypes(rs.Properties)
		for _, f := range files {
			cols, rows, err := bolt.ReadCSV(f, types)
			if err != nil {
				return engine.LoadStats{}, err
			}
			n, err := s.loadUnwind(ctx, cols, rows, typ, false, rs.Start, rs.End)
			if err != nil {
				return engine.LoadStats{}, fmt.Errorf("memgraph: load rels %s from %s: %w", typ, filepath.Base(f), err)
			}
			edges += n
		}
	}

	return engine.LoadStats{
		Duration:    time.Since(start),
		Nodes:       nodes,
		Edges:       edges,
		BytesOnDisk: -1,
		Method:      "unwind",
	}, nil
}

// createIndex creates the per-label id index. Memgraph has no IF NOT
// EXISTS clause, so an "already exists" error from a warm instance is
// tolerated; every other error is fatal.
func (s *session) createIndex(ctx context.Context, label string) error {
	err := s.runWrite(ctx, indexStatement(label))
	if err != nil && !strings.Contains(strings.ToLower(err.Error()), "already exists") {
		return fmt.Errorf("memgraph: create index on %s: %w", label, err)
	}
	return nil
}

// indexStatement is the Memgraph label+property index DDL.
func indexStatement(label string) string {
	return fmt.Sprintf("CREATE INDEX ON :%s(id)", label)
}

// wipe removes data left over from a previous load on the same server.
func (s *session) wipe(ctx context.Context) error {
	if err := s.runWrite(ctx, "MATCH (n) DETACH DELETE n"); err != nil {
		return fmt.Errorf("memgraph: wipe: %w", err)
	}
	return nil
}

// runWrite executes one write statement and drains its result.
func (s *session) runWrite(ctx context.Context, text string) error {
	res, err := s.pool.Run(ctx, engine.Op{Class: engine.Write, Text: text})
	if err != nil {
		return err
	}
	for res.Next() {
	}
	if err := res.Err(); err != nil {
		_ = res.Close()
		return err
	}
	return res.Close()
}

// loadUnwind loads rows via batched UNWIND ... CREATE statements.
func (s *session) loadUnwind(ctx context.Context, cols []bolt.Column, rows []string, typeOrLabel string, isNode bool, startLabel, endLabel string) (int64, error) {
	var count int64
	for i := 0; i < len(rows); i += unwindBatchSize {
		end := min(i+unwindBatchSize, len(rows))
		cypher := buildUnwindCypher(rows[i:end], cols, typeOrLabel, isNode, startLabel, endLabel)
		if cypher == "" {
			continue
		}
		if err := s.runWrite(ctx, cypher); err != nil {
			return count, err
		}
		count += int64(end - i)
	}
	return count, nil
}

// buildUnwindCypher builds one UNWIND [...] AS row CREATE statement for a
// batch of CSV rows. Returns "" for an empty batch.
//
// Nodes: the :ID column becomes the id property; named property columns go
// into the row map; result is UNWIND [...] AS row CREATE (n:Label) SET n = row.
// Rels: :START_ID/:END_ID become __s/__e integers matched against the
// endpoint labels; properties are SET explicitly so __s/__e stay off the rel.
func buildUnwindCypher(rows []string, cols []bolt.Column, typeOrLabel string, isNode bool, startLabel, endLabel string) string {
	if len(rows) == 0 {
		return ""
	}
	idIdx, sidIdx, eidIdx := -1, -1, -1
	for j, col := range cols {
		switch col.Type {
		case "ID":
			idIdx = j
		case "START_ID":
			sidIdx = j
		case "END_ID":
			eidIdx = j
		}
	}

	var sb strings.Builder
	sb.WriteString("UNWIND [")
	var propCols []bolt.Column
	for i, row := range rows {
		if i > 0 {
			sb.WriteString(",")
		}
		fields := strings.Split(row, ",")
		sb.WriteString("{")
		first := true
		writeKV := func(k, v, typ string) {
			if !first {
				sb.WriteString(",")
			}
			first = false
			sb.WriteString(k)
			sb.WriteString(":")
			if v == "" {
				sb.WriteString("null")
				return
			}
			switch typ {
			case "ID", "START_ID", "END_ID", "INT64", "INT32", "LONG", "INT", "INTEGER",
				"FLOAT64", "DOUBLE", "FLOAT", "BOOL", "BOOLEAN":
				sb.WriteString(v)
			default:
				sb.WriteString(`"`)
				sb.WriteString(strings.ReplaceAll(v, `"`, `\"`))
				sb.WriteString(`"`)
			}
		}
		field := func(idx int) string {
			if idx >= 0 && idx < len(fields) {
				return fields[idx]
			}
			return "0"
		}
		if isNode {
			writeKV("id", field(idIdx), "ID")
		} else {
			writeKV("__s", field(sidIdx), "INT64")
			writeKV("__e", field(eidIdx), "INT64")
		}
		for j, col := range cols {
			if col.Name == "" || col.Structural() {
				continue
			}
			if i == 0 {
				propCols = append(propCols, col)
			}
			val := ""
			if j < len(fields) {
				val = fields[j]
			}
			writeKV(col.Name, val, col.Type)
		}
		sb.WriteString("}")
	}
	sb.WriteString("] AS row")
	if isNode {
		fmt.Fprintf(&sb, " CREATE (n:%s) SET n = row", typeOrLabel)
		return sb.String()
	}
	fmt.Fprintf(&sb, " MATCH (a:%s {id: row.__s}) MATCH (b:%s {id: row.__e}) CREATE (a)-[r:%s]->(b)",
		startLabel, endLabel, typeOrLabel)
	for _, col := range propCols {
		fmt.Fprintf(&sb, " SET r.%s = row.%s", col.Name, col.Name)
	}
	return sb.String()
}

// sortedKeys returns map keys in sorted order for deterministic load order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
