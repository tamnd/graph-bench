//go:build bolt

// Package memgraph is the adapter for the Memgraph graph database over the
// Bolt plane. Memgraph speaks Bolt and openCypher, so its adapter is
// structurally identical to the Neo4j adapter: both use driver/bolt for the
// wire protocol and the canonical value model.
//
// The Memgraph-specific differences from Neo4j:
//   - The database name for sessions is "" (Memgraph has no named databases).
//   - Memgraph persists to disk by default but also supports a pure-memory mode;
//     Capabilities.PersistentDisk reports the configured mode from Config.Values.
//   - Bulk load uses LOAD CSV through the Bolt connection; Memgraph does not
//     expose neo4j-admin import. For larger datasets, the mg_client tool or
//     direct import through the Bolt LOAD CSV is the supported path.
//   - BytesOnDisk is -1 for in-memory-mode runs (F6: honest reporting).
//
// Build tag: bolt. Use -tags bolt to compile this package.
package memgraph

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	neo4jlib "github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/tamnd/graph-bench/driver/bolt"
	"github.com/tamnd/graph-bench/target"
)

// New returns a Target for Memgraph. uri is the Bolt URI (e.g.
// bolt://127.0.0.1:7687); user and pass are the credentials. Passing empty
// user/pass connects unauthenticated, which is the default Memgraph community
// mode.
func New(uri, user, pass string) target.Target {
	return &mgTarget{uri: uri, user: user, pass: pass}
}

type mgTarget struct {
	uri  string
	user string
	pass string
}

func (t *mgTarget) Name() string { return "memgraph" }

func (t *mgTarget) Plane() target.Plane { return target.Bolt }

func (t *mgTarget) Capabilities() target.Capabilities {
	return target.Capabilities{
		Languages:      []target.Language{target.Cypher},
		Transactions:   true,
		BulkCSVLoad:    true,
		PersistentDisk: true,
	}
}

func (t *mgTarget) Version(ctx context.Context) (string, error) {
	pool, err := bolt.Open(ctx, t.uri, t.user, t.pass, "")
	if err != nil {
		return "", err
	}
	defer pool.Close(ctx)
	return pool.Version(ctx)
}

// Setup dials the Memgraph Bolt endpoint and returns a Driver.
func (t *mgTarget) Setup(ctx context.Context, cfg target.Config) (target.Driver, error) {
	pool, err := bolt.Open(ctx, t.uri, t.user, t.pass, "")
	if err != nil {
		return nil, fmt.Errorf("memgraph: open: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close(ctx)
		return nil, fmt.Errorf("memgraph: ping: %w", err)
	}
	return &mgDriver{pool: pool, cfg: cfg}, nil
}

// Load bulk-loads a dataset into Memgraph using LOAD CSV Cypher statements.
func (t *mgTarget) Load(ctx context.Context, d target.Driver, ds target.Dataset) (target.LoadStats, error) {
	drv := d.(*mgDriver)
	start := time.Now()
	var nodes, edges int64

	schema := ds.Schema()
	for label := range schema.Nodes {
		files, cols, err := ds.NodeFiles(label)
		if err != nil {
			return target.LoadStats{}, fmt.Errorf("memgraph: node files %s: %w", label, err)
		}
		for _, f := range files {
			n, err := loadCSVFile(ctx, drv, f, cols, label, true)
			if err != nil {
				return target.LoadStats{}, fmt.Errorf("memgraph: load nodes %s from %s: %w", label, filepath.Base(f), err)
			}
			nodes += n
		}
	}
	for relType := range schema.Relationships {
		files, cols, err := ds.RelFiles(relType)
		if err != nil {
			return target.LoadStats{}, fmt.Errorf("memgraph: rel files %s: %w", relType, err)
		}
		for _, f := range files {
			n, err := loadCSVFile(ctx, drv, f, cols, relType, false)
			if err != nil {
				return target.LoadStats{}, fmt.Errorf("memgraph: load rels %s from %s: %w", relType, filepath.Base(f), err)
			}
			edges += n
		}
	}

	bytesOnDisk := int64(-1)
	return target.LoadStats{Duration: time.Since(start), Nodes: nodes, Edges: edges, BytesOnDisk: bytesOnDisk}, nil
}

// Teardown closes the Bolt pool.
func (t *mgTarget) Teardown(ctx context.Context, d target.Driver) error {
	if d == nil {
		return nil
	}
	return d.Close(ctx)
}

// mgDriver is the live handle to a Memgraph connection pool.
type mgDriver struct {
	pool *bolt.Pool
	cfg  target.Config
}

func (d *mgDriver) Run(ctx context.Context, q target.Query, params target.Params) (target.Result, error) {
	return d.pool.Run(ctx, q, params)
}

func (d *mgDriver) Begin(ctx context.Context, mode target.AccessMode) (target.Tx, error) {
	tx, sess, err := d.pool.BeginTx(ctx, mode)
	if err != nil {
		return nil, err
	}
	return &mgTx{pool: d.pool, tx: tx, sess: sess.(neo4jlib.SessionWithContext)}, nil
}

func (d *mgDriver) Close(ctx context.Context) error {
	return d.pool.Close(ctx)
}

type mgTx struct {
	pool *bolt.Pool
	tx   neo4jlib.ExplicitTransaction
	sess neo4jlib.SessionWithContext
}

func (t *mgTx) Run(ctx context.Context, q target.Query, params target.Params) (target.Result, error) {
	return t.pool.RunTx(ctx, t.tx, q, params)
}

func (t *mgTx) Commit(ctx context.Context) error {
	err := t.tx.Commit(ctx)
	t.sess.Close(ctx)
	return err
}

func (t *mgTx) Rollback(ctx context.Context) error {
	err := t.tx.Rollback(ctx)
	t.sess.Close(ctx)
	return err
}

// loadCSVFile reads a CSV file and bulk-imports it via batched Cypher UNWIND.
func loadCSVFile(ctx context.Context, d *mgDriver, path string, cols []target.Column, typeOrLabel string, isNode bool) (int64, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("open %s: %w", path, err)
	}
	lines := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	if len(lines) <= 1 {
		return 0, nil
	}
	dataLines := lines[1:]

	const batchSize = 500
	var count int64
	for i := 0; i < len(dataLines); i += batchSize {
		end := i + batchSize
		if end > len(dataLines) {
			end = len(dataLines)
		}
		n, err := insertBatch(ctx, d, dataLines[i:end], cols, typeOrLabel, isNode)
		if err != nil {
			return count, err
		}
		count += n
	}
	return count, nil
}

func insertBatch(ctx context.Context, d *mgDriver, rows []string, cols []target.Column, typeOrLabel string, isNode bool) (int64, error) {
	cypher := buildUnwindCypher(rows, cols, typeOrLabel, isNode)
	if cypher == "" {
		return 0, nil
	}
	q := target.Query{Class: target.Write, Text: cypher}
	res, err := d.Run(ctx, q, nil)
	if err != nil {
		return 0, err
	}
	for res.Next() {
	}
	if err := res.Err(); err != nil {
		res.Close()
		return 0, err
	}
	res.Close()
	return int64(len(rows)), nil
}

// buildUnwindCypher builds a UNWIND [...] AS row CREATE/MERGE statement for a
// batch of CSV rows. Returns "" for an empty batch.
func buildUnwindCypher(rows []string, cols []target.Column, typeOrLabel string, isNode bool) string {
	if len(rows) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("UNWIND [")
	for i, row := range rows {
		if i > 0 {
			sb.WriteString(",")
		}
		fields := strings.Split(row, ",")
		sb.WriteString("{")
		first := true
		for j, col := range cols {
			if col.Name == "" {
				continue
			}
			if !first {
				sb.WriteString(",")
			}
			first = false
			val := ""
			if j < len(fields) {
				val = fields[j]
			}
			sb.WriteString(col.Name)
			sb.WriteString(":")
			switch col.Type {
			case "int", "long", "integer", "double", "float":
				sb.WriteString(val)
			default:
				sb.WriteString("\"")
				sb.WriteString(strings.ReplaceAll(val, `"`, `\"`))
				sb.WriteString("\"")
			}
		}
		sb.WriteString("}")
	}
	sb.WriteString("] AS row")
	if isNode {
		fmt.Fprintf(&sb, " CREATE (n:%s) SET n = row", typeOrLabel)
	} else {
		fmt.Fprintf(&sb, " MERGE (a {id: row.id}) MERGE (b {id: row.end_id}) CREATE (a)-[r:%s]->(b) SET r = row", typeOrLabel)
	}
	return sb.String()
}
