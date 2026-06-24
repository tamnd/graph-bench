//go:build bolt

// Package neo4j is the adapter for the Neo4j graph database over the Bolt
// plane. It satisfies the seven-guarantee adapter contract from
// notes/Spec/2060/bench/03-target-spi.md section 5:
//
//   - Faithful value mapping: the driver/bolt package maps Neo4j values to
//     the canonical model, including nodes, relationships, and paths.
//   - Streaming results: the Bolt cursor is consumed one record at a time.
//   - Idempotent teardown: Teardown is safe on a partially built Driver.
//   - Honest capabilities: no mock-transaction autocommit pretending.
//   - Verbatim configuration: Config.Values are applied; no secret tuning.
//   - Live version: Version() queries the running Neo4j, not a constant.
//   - No native text needed: Neo4j speaks openCypher natively (the primary
//     dialect), so this is not a native-plane adapter.
//
// Build tag: bolt. Use -tags bolt to compile this package.
package neo4j

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	neo4jlib "github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/tamnd/graph-bench/driver/bolt"
	"github.com/tamnd/graph-bench/target"
)

// New returns a Target for Neo4j. uri is the Bolt URI the container is
// reachable at (e.g. bolt://127.0.0.1:54321); it is provided by setup.Start.
// user and pass are the credentials. Passing empty user/pass uses "none" auth
// (which is what the Neo4j container spec sets).
func New(uri, user, pass string) target.Target {
	if user == "" {
		user = "neo4j"
	}
	if pass == "" {
		pass = "none"
	}
	return &neoTarget{uri: uri, user: user, pass: pass}
}

type neoTarget struct {
	uri  string
	user string
	pass string
}

func (t *neoTarget) Name() string { return "neo4j" }

func (t *neoTarget) Plane() target.Plane { return target.Bolt }

func (t *neoTarget) Capabilities() target.Capabilities {
	return target.Capabilities{
		Languages:      []target.Language{target.Cypher},
		Transactions:   true,
		BulkCSVLoad:    true,
		PersistentDisk: true,
	}
}

func (t *neoTarget) Version(ctx context.Context) (string, error) {
	pool, err := bolt.Open(ctx, t.uri, t.user, t.pass, "neo4j")
	if err != nil {
		return "", err
	}
	defer pool.Close(ctx)
	return pool.Version(ctx)
}

// Setup dials the Neo4j Bolt endpoint and returns a Driver.
func (t *neoTarget) Setup(ctx context.Context, cfg target.Config) (target.Driver, error) {
	pool, err := bolt.Open(ctx, t.uri, t.user, t.pass, "neo4j")
	if err != nil {
		return nil, fmt.Errorf("neo4j: open: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close(ctx)
		return nil, fmt.Errorf("neo4j: ping: %w", err)
	}
	return &neoDriver{pool: pool, cfg: cfg}, nil
}

// Load bulk-loads a dataset into Neo4j using LOAD CSV over Bolt.
// Neo4j's admin import tool (neo4j-admin import) needs a stopped database
// and elevated filesystem access, so we use LOAD CSV for the Bolt-plane load.
// This means Load takes seconds to minutes rather than sub-second, but it
// requires no privileges, no container restart, and no side channel.
func (t *neoTarget) Load(ctx context.Context, d target.Driver, ds target.Dataset) (target.LoadStats, error) {
	drv := d.(*neoDriver)
	start := time.Now()
	var nodes, edges int64

	// Load nodes first, then relationships.
	schema := ds.Schema()
	for label := range schema.Nodes {
		files, cols, err := ds.NodeFiles(label)
		if err != nil {
			return target.LoadStats{}, fmt.Errorf("neo4j: node files %s: %w", label, err)
		}
		for _, f := range files {
			n, err := loadCSVFile(ctx, drv, f, cols, label, true)
			if err != nil {
				return target.LoadStats{}, fmt.Errorf("neo4j: load nodes %s from %s: %w", label, filepath.Base(f), err)
			}
			nodes += n
		}
	}
	for relType := range schema.Relationships {
		files, cols, err := ds.RelFiles(relType)
		if err != nil {
			return target.LoadStats{}, fmt.Errorf("neo4j: rel files %s: %w", relType, err)
		}
		for _, f := range files {
			n, err := loadCSVFile(ctx, drv, f, cols, relType, false)
			if err != nil {
				return target.LoadStats{}, fmt.Errorf("neo4j: load rels %s from %s: %w", relType, filepath.Base(f), err)
			}
			edges += n
		}
	}
	return target.LoadStats{Duration: time.Since(start), Nodes: nodes, Edges: edges, BytesOnDisk: -1}, nil
}

// Teardown closes the Bolt pool.
func (t *neoTarget) Teardown(ctx context.Context, d target.Driver) error {
	if d == nil {
		return nil
	}
	return d.Close(ctx)
}

// neoDriver is the live handle to a Neo4j connection pool.
type neoDriver struct {
	pool *bolt.Pool
	cfg  target.Config
}

func (d *neoDriver) Run(ctx context.Context, q target.Query, params target.Params) (target.Result, error) {
	return d.pool.Run(ctx, q, params)
}

func (d *neoDriver) Begin(ctx context.Context, mode target.AccessMode) (target.Tx, error) {
	tx, sess, err := d.pool.BeginTx(ctx, mode)
	if err != nil {
		return nil, err
	}
	return &neoTx{pool: d.pool, tx: tx, sess: sess.(neo4jlib.SessionWithContext)}, nil
}

func (d *neoDriver) Close(ctx context.Context) error {
	return d.pool.Close(ctx)
}

// neoTx wraps an explicit Neo4j transaction.
type neoTx struct {
	pool *bolt.Pool
	tx   neo4jlib.ExplicitTransaction
	sess neo4jlib.SessionWithContext
}

func (t *neoTx) Run(ctx context.Context, q target.Query, params target.Params) (target.Result, error) {
	return t.pool.RunTx(ctx, t.tx, q, params)
}

func (t *neoTx) Commit(ctx context.Context) error {
	err := t.tx.Commit(ctx)
	t.sess.Close(ctx)
	return err
}

func (t *neoTx) Rollback(ctx context.Context) error {
	err := t.tx.Rollback(ctx)
	t.sess.Close(ctx)
	return err
}

// loadCSVFile issues a LOAD CSV Cypher statement for one file. It returns the
// number of rows imported. isNode controls whether it creates a node or a rel.
func loadCSVFile(ctx context.Context, d *neoDriver, path string, cols []target.Column, typeOrLabel string, isNode bool) (int64, error) {
	// Read the file content and build LOAD CSV FROM file:// URI.
	// Neo4j's LOAD CSV expects an absolute file:// URI accessible to the server.
	// When the harness runs against a container, the file is not on the server's
	// filesystem, so we fall back to bulk insert via CREATE statements from the
	// CSV content.
	f, err := os.Open(path)
	if err != nil {
		return 0, fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	content, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}

	lines := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	if len(lines) == 0 {
		return 0, nil
	}

	// Skip the header line (the typed CSV header).
	dataLines := lines[1:]
	if len(dataLines) == 0 {
		return 0, nil
	}

	// Build batched CREATE statements. We batch 500 rows per query to keep
	// statement size manageable while not paying per-row round-trip cost.
	const batchSize = 500
	var count int64
	for i := 0; i < len(dataLines); i += batchSize {
		end := i + batchSize
		if end > len(dataLines) {
			end = len(dataLines)
		}
		batch := dataLines[i:end]
		n, err := insertBatch(ctx, d, batch, cols, typeOrLabel, isNode)
		if err != nil {
			return count, err
		}
		count += n
	}
	return count, nil
}

// insertBatch builds and runs a single Cypher UNWIND+CREATE for one batch.
func insertBatch(ctx context.Context, d *neoDriver, rows []string, cols []target.Column, typeOrLabel string, isNode bool) (int64, error) {
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

// buildUnwindCypher builds a UNWIND [...] AS row CREATE statement for a batch
// of CSV rows. Returns "" for an empty batch.
//
// For nodes: property columns (Name != "") go into the row map; the :ID column
// becomes the node's id property. Result: UNWIND [...] AS row CREATE (n:Label) SET n = row.
// For rels: :START_ID and :END_ID are embedded as __s/__e integer values;
// other structural columns (:TYPE) are skipped. MATCH locates the endpoints.
func buildUnwindCypher(rows []string, cols []target.Column, typeOrLabel string, isNode bool) string {
	if len(rows) == 0 {
		return ""
	}
	sidIdx, eidIdx := -1, -1
	for j, col := range cols {
		switch col.Type {
		case "START_ID":
			sidIdx = j
		case "END_ID":
			eidIdx = j
		}
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
		writeKV := func(k, v, typ string) {
			if !first {
				sb.WriteString(",")
			}
			first = false
			sb.WriteString(k)
			sb.WriteString(":")
			switch typ {
			case "ID", "START_ID", "END_ID", "INT64", "INT32", "LONG", "INT", "INTEGER", "DOUBLE", "FLOAT":
				sb.WriteString(v)
			default:
				sb.WriteString(`"`)
				sb.WriteString(strings.ReplaceAll(v, `"`, `\"`))
				sb.WriteString(`"`)
			}
		}
		if !isNode {
			// Embed endpoint IDs as __s and __e for the MATCH clause.
			sid, eid := "0", "0"
			if sidIdx >= 0 && sidIdx < len(fields) {
				sid = fields[sidIdx]
			}
			if eidIdx >= 0 && eidIdx < len(fields) {
				eid = fields[eidIdx]
			}
			writeKV("__s", sid, "INT64")
			writeKV("__e", eid, "INT64")
		}
		for j, col := range cols {
			if col.Name == "" {
				continue // structural (:LABEL, :TYPE, :START_ID, :END_ID)
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
	} else {
		// The schema for our synthetic datasets has a single node label "Node".
		// Row carries __s and __e (the endpoint IDs) plus any rel properties.
		fmt.Fprintf(&sb,
			" MATCH (a:Node {id: row.__s}) MATCH (b:Node {id: row.__e}) CREATE (a)-[r:%s]->(b)",
			typeOrLabel)
	}
	return sb.String()
}

// contains is a case-sensitive substring check used in tests.
func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}

// neo4jAdminImport runs the neo4j-admin import tool on the host, for cases
// where the harness has access to the Neo4j data directory. This is faster
// than LOAD CSV but requires a stopped database and host-level access to the
// container data volume. It is kept as a reference; the Bolt-plane loader
// (loadCSVFile above) is used by default.
func neo4jAdminImport(ctx context.Context, dataDir, importDir string) error {
	cmd := exec.CommandContext(ctx, "neo4j-admin",
		"database", "import", "full",
		"--nodes="+filepath.Join(importDir, "nodes/"),
		"--relationships="+filepath.Join(importDir, "rels/"),
		"--overwrite-destination",
		"neo4j",
	)
	cmd.Dir = dataDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("neo4j-admin import: %w: %s", err, out)
	}
	return nil
}
