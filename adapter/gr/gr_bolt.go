//go:build bolt

// This file adds the gr-bolt target to the gr adapter package. gr-bolt is the
// gr adapter pointed at a running "gr serve" instance over the Bolt plane. It
// shares the driver/bolt client with the other Bolt engines so gr over the wire
// is measured by exactly the same code path as Neo4j over the wire. The
// difference between gr (in-process) and gr-bolt in the matrix is the plane
// overhead, a published number (ADR-2 from the spec research doc).
//
// Loading: gr's Bolt endpoint does not support UNWIND + MATCH in a single
// query, which rules out the standard batch load pattern used by Neo4j and
// Memgraph. For large datasets the benchmark therefore loads data through the
// native gr bulk loader (the same four-pass path used by the in-process
// adapter) and then serves it via gr serve --bolt. Set GR_BOLT_DB_PATH to the
// database file path that the running "gr serve --bolt" process was started
// against. When set, Load() uses the gr bulk loader to write fresh data to
// that path, stops the serve process (via GR_BOLT_PID_FILE or GR_BOLT_PID),
// loads, then restarts it. If GR_BOLT_PRELOADED=1 is set instead, Load()
// skips loading entirely (assumes the database is already populated) and only
// queries the current node/edge counts for the stats record.
//
// Build tag: bolt. gr-bolt is compiled only when the Bolt plane is included.
package gr

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"time"

	grdb "github.com/tamnd/gr"
	"github.com/tamnd/gr/loader"
	neo4jlib "github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/tamnd/graph-bench/driver/bolt"
	"github.com/tamnd/graph-bench/target"
)

// BoltTarget is the gr-bolt target: gr accessed over the Bolt wire protocol
// rather than the in-process library. It satisfies the same Target interface as
// the in-process Target, but on target.Bolt plane.
type BoltTarget struct {
	// URI is the Bolt URI of the running "gr serve" instance,
	// e.g. "bolt://127.0.0.1:7688".
	URI string
	// User and Pass are the credentials. gr serve's default is no auth.
	User string
	Pass string
}

// NewBolt returns a BoltTarget. uri is the bolt:// URI of the gr server.
func NewBolt(uri string) *BoltTarget {
	return &BoltTarget{URI: uri}
}

func (t *BoltTarget) Name() string { return "gr-bolt" }

func (t *BoltTarget) Plane() target.Plane { return target.Bolt }

func (t *BoltTarget) Capabilities() target.Capabilities {
	return target.Capabilities{
		Languages:      []target.Language{target.Cypher},
		Transactions:   true,
		BulkCSVLoad:    true,
		PersistentDisk: true,
	}
}

func (t *BoltTarget) Version(ctx context.Context) (string, error) {
	pool, err := bolt.Open(ctx, t.URI, t.User, t.Pass, "")
	if err != nil {
		return "", err
	}
	defer pool.Close(ctx)
	return pool.Version(ctx)
}

// Setup dials the gr serve instance and returns a Driver.
func (t *BoltTarget) Setup(ctx context.Context, cfg target.Config) (target.Driver, error) {
	pool, err := bolt.Open(ctx, t.URI, t.User, t.Pass, "")
	if err != nil {
		return nil, fmt.Errorf("gr-bolt: open: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close(ctx)
		return nil, fmt.Errorf("gr-bolt: ping: %w", err)
	}
	return &boltDriver{pool: pool, cfg: cfg}, nil
}

// Load imports the dataset into gr. Three modes are available, selected by
// environment variables (see package comment at the top of this file).
//
//   - GR_BOLT_PRELOADED=1: assume the database is already populated; skip
//     loading and return the current node/edge counts as stats. Use this after
//     pre-loading via the in-process adapter or gr import CLI.
//   - GR_BOLT_DB_PATH=<path>: use the native gr bulk loader to write to the
//     given path, stopping and restarting the gr serve process around the load.
//     GR_BOLT_PID_FILE or GR_BOLT_PID identifies the serve process to stop.
//   - (default): fall back to Bolt-based loading via UNWIND+MERGE. This works
//     for small datasets but is impractically slow for large ones (>10k edges).
func (t *BoltTarget) Load(ctx context.Context, d target.Driver, ds target.Dataset) (target.LoadStats, error) {
	drv := d.(*boltDriver)

	if os.Getenv("GR_BOLT_PRELOADED") == "1" {
		return queryCurrentStats(ctx, drv)
	}
	if dbPath := os.Getenv("GR_BOLT_DB_PATH"); dbPath != "" {
		return loadViaNativeLoader(ctx, drv, ds, dbPath)
	}
	return loadDatasetViaBolt(ctx, drv, ds)
}

// queryCurrentStats queries the node and edge counts from the live gr serve
// instance without loading any data. Used when GR_BOLT_PRELOADED=1.
func queryCurrentStats(ctx context.Context, d *boltDriver) (target.LoadStats, error) {
	var stats target.LoadStats
	nodeRes, err := d.Run(ctx, target.Query{Text: "MATCH (n) RETURN count(n) AS cnt"}, nil)
	if err == nil {
		if nodeRes.Next() {
			if row := nodeRes.Row(); len(row) > 0 {
				if n, ok := row[0].(int64); ok {
					stats.Nodes = n
				}
			}
		}
		nodeRes.Close()
	}
	edgeRes, err2 := d.Run(ctx, target.Query{Text: "MATCH ()-[r]->() RETURN count(r) AS cnt"}, nil)
	if err2 == nil {
		if edgeRes.Next() {
			if row := edgeRes.Row(); len(row) > 0 {
				if n, ok := row[0].(int64); ok {
					stats.Edges = n
				}
			}
		}
		edgeRes.Close()
	}
	return stats, nil
}

// loadViaNativeLoader uses the gr bulk loader to write dataset data to dbPath,
// stopping the running gr serve process before loading and restarting it after.
// The gr serve process PID is read from GR_BOLT_PID_FILE (path to a PID file)
// or GR_BOLT_PID (the PID itself). After loading, the serve process is
// restarted with GR_BOLT_SERVE_CMD (default: "gr serve --bolt --path <dbPath>").
func loadViaNativeLoader(ctx context.Context, d *boltDriver, ds target.Dataset, dbPath string) (target.LoadStats, error) {
	if ds.Dir() == "" {
		return target.LoadStats{}, fmt.Errorf("gr-bolt native load: dataset %q has no directory (statements datasets not supported)", ds.Name())
	}

	pid := nativeLoaderServePID()

	// Stop the running server.
	if pid > 0 {
		proc, err := os.FindProcess(pid)
		if err == nil {
			_ = proc.Signal(syscall.SIGTERM)
			// Wait up to 5s for it to exit.
			for i := 0; i < 50; i++ {
				time.Sleep(100 * time.Millisecond)
				if err := proc.Signal(syscall.Signal(0)); err != nil {
					break
				}
			}
		}
	}

	// Remove the old database files so the loader builds fresh.
	for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm"} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return target.LoadStats{}, fmt.Errorf("gr-bolt native load: clear %s: %w", p, err)
		}
	}

	nodeSrcs, err := nodeSources(ds)
	if err != nil {
		return target.LoadStats{}, err
	}
	relSrcs, err := relSources(ds)
	if err != nil {
		return target.LoadStats{}, err
	}

	start := time.Now()
	l := loader.New(loader.Options{
		Nodes:         nodeSrcs,
		Relationships: relSrcs,
		ArrayDelim:    ';',
		OnDangling:    loader.Skip,
	})
	if err := l.Run(dbPath); err != nil {
		return target.LoadStats{}, fmt.Errorf("gr-bolt native load: %w", err)
	}
	dur := time.Since(start)

	lstats := l.Stats()
	out := target.LoadStats{
		Duration: dur,
		Nodes:    int64(lstats.Nodes),
		Edges:    int64(lstats.Rels),
	}
	if db, openErr := grdb.Open(dbPath, grdb.Options{}); openErr == nil {
		if info, infoErr := db.Info(); infoErr == nil {
			out.BytesOnDisk = info.SizeBytes
		}
		db.Close()
	}

	// Restart the gr serve process.
	serveCmd := os.Getenv("GR_BOLT_SERVE_CMD")
	if serveCmd == "" {
		serveCmd = fmt.Sprintf("gr serve --bolt --path %s", dbPath)
	}
	parts := strings.Fields(serveCmd)
	cmd := exec.CommandContext(ctx, parts[0], parts[1:]...) //nolint:gosec
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return out, fmt.Errorf("gr-bolt native load: restart serve: %w", err)
	}
	// Write the new PID so future runs can stop it.
	if pf := os.Getenv("GR_BOLT_PID_FILE"); pf != "" {
		_ = os.WriteFile(pf, []byte(strconv.Itoa(cmd.Process.Pid)), 0600)
	}

	// Wait for the Bolt port to be ready.
	for i := 0; i < 100; i++ {
		time.Sleep(100 * time.Millisecond)
		if err := d.pool.Ping(ctx); err == nil {
			break
		}
	}

	return out, nil
}

// nativeLoaderServePID returns the PID of the running gr serve process from
// GR_BOLT_PID_FILE (preferred) or GR_BOLT_PID.
func nativeLoaderServePID() int {
	if pf := os.Getenv("GR_BOLT_PID_FILE"); pf != "" {
		if b, err := os.ReadFile(pf); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				return pid
			}
		}
	}
	if ps := os.Getenv("GR_BOLT_PID"); ps != "" {
		if pid, err := strconv.Atoi(ps); err == nil {
			return pid
		}
	}
	return 0
}

// Teardown closes the Bolt pool. It does not stop the gr serve process (that
// is the responsibility of the caller who started it).
func (t *BoltTarget) Teardown(ctx context.Context, d target.Driver) error {
	if d == nil {
		return nil
	}
	return d.Close(ctx)
}

// boltDriver is the live handle to a gr-bolt connection pool.
type boltDriver struct {
	pool *bolt.Pool
	cfg  target.Config
}

func (d *boltDriver) Run(ctx context.Context, q target.Query, params target.Params) (target.Result, error) {
	return d.pool.Run(ctx, q, params)
}

func (d *boltDriver) Begin(ctx context.Context, mode target.AccessMode) (target.Tx, error) {
	tx, sess, err := d.pool.BeginTx(ctx, mode)
	if err != nil {
		return nil, err
	}
	return &boltTx{pool: d.pool, tx: tx, sess: sess.(neo4jlib.SessionWithContext)}, nil
}

func (d *boltDriver) Close(ctx context.Context) error {
	return d.pool.Close(ctx)
}

type boltTx struct {
	pool *bolt.Pool
	tx   neo4jlib.ExplicitTransaction
	sess neo4jlib.SessionWithContext
}

func (t *boltTx) Run(ctx context.Context, q target.Query, params target.Params) (target.Result, error) {
	return t.pool.RunTx(ctx, t.tx, q, params)
}

func (t *boltTx) Commit(ctx context.Context) error {
	err := t.tx.Commit(ctx)
	t.sess.Close(ctx)
	return err
}

func (t *boltTx) Rollback(ctx context.Context) error {
	err := t.tx.Rollback(ctx)
	t.sess.Close(ctx)
	return err
}

// loadDatasetViaBolt imports the dataset by issuing CREATE statements over the
// Bolt connection (the same approach used in the Neo4j and Memgraph adapters).
func loadDatasetViaBolt(ctx context.Context, d *boltDriver, ds target.Dataset) (target.LoadStats, error) {
	schema := ds.Schema()
	var stats target.LoadStats

	// Wipe any stale data from prior runs.
	wipeRes, wipeErr := d.Run(ctx, target.Query{Class: target.Write, Text: "MATCH (n) DETACH DELETE n"}, nil)
	if wipeErr == nil {
		for wipeRes.Next() {
		}
		wipeRes.Close()
	}

	// Load nodes, then create an index on id before loading edges. Without the
	// index each MATCH (n:Label {id: x}) in the edge load is a full scan.
	for label := range schema.Nodes {
		files, cols, err := ds.NodeFiles(label)
		if err != nil {
			return stats, fmt.Errorf("gr-bolt: node files %s: %w", label, err)
		}
		for _, f := range files {
			n, err := boltInsertCSV(ctx, d, f, cols, label, true)
			if err != nil {
				return stats, fmt.Errorf("gr-bolt: load nodes %s: %w", label, err)
			}
			stats.Nodes += n
		}
		idxCypher := fmt.Sprintf("CREATE INDEX IF NOT EXISTS FOR (n:%s) ON (n.id)", label)
		idxRes, idxErr := d.Run(ctx, target.Query{Class: target.Write, Text: idxCypher}, nil)
		if idxErr == nil {
			for idxRes.Next() {
			}
			idxRes.Close()
		}
	}
	for relType := range schema.Relationships {
		files, cols, err := ds.RelFiles(relType)
		if err != nil {
			return stats, fmt.Errorf("gr-bolt: rel files %s: %w", relType, err)
		}
		for _, f := range files {
			n, err := boltInsertCSV(ctx, d, f, cols, relType, false)
			if err != nil {
				return stats, fmt.Errorf("gr-bolt: load rels %s: %w", relType, err)
			}
			stats.Edges += n
		}
	}
	return stats, nil
}

// boltInsertCSV reads a CSV file and batch-inserts it via UNWIND+CREATE.
func boltInsertCSV(ctx context.Context, d *boltDriver, path string, cols []target.Column, typeOrLabel string, isNode bool) (int64, error) {
	content, err := readFile(path)
	if err != nil {
		return 0, err
	}
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")
	if len(lines) <= 1 {
		return 0, nil
	}
	const batchSize = 500
	var count int64
	for i := 1; i < len(lines); i += batchSize {
		end := i + batchSize
		if end > len(lines) {
			end = len(lines)
		}
		n, err := boltBatch(ctx, d, lines[i:end], cols, typeOrLabel, isNode)
		if err != nil {
			return count, err
		}
		count += n
	}
	return count, nil
}

func boltBatch(ctx context.Context, d *boltDriver, rows []string, cols []target.Column, typeOrLabel string, isNode bool) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}

	// Find structural column indices.
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
	for i, row := range rows {
		if i > 0 {
			sb.WriteString(",")
		}
		fields := strings.Split(row, ",")
		sb.WriteString("{")
		first := true
		writeKV := func(k, v string, numeric bool) {
			if !first {
				sb.WriteString(",")
			}
			first = false
			sb.WriteString(k)
			sb.WriteString(":")
			if numeric {
				sb.WriteString(v)
			} else {
				sb.WriteString(`"`)
				sb.WriteString(strings.ReplaceAll(v, `"`, `\"`))
				sb.WriteString(`"`)
			}
		}
		if isNode {
			id := "0"
			if idIdx >= 0 && idIdx < len(fields) {
				id = fields[idIdx]
			}
			writeKV("id", id, true)
		} else {
			sid, eid := "0", "0"
			if sidIdx >= 0 && sidIdx < len(fields) {
				sid = fields[sidIdx]
			}
			if eidIdx >= 0 && eidIdx < len(fields) {
				eid = fields[eidIdx]
			}
			writeKV("__s", sid, true)
			writeKV("__e", eid, true)
		}
		for j, col := range cols {
			if col.Name == "" {
				continue
			}
			val := ""
			if j < len(fields) {
				val = fields[j]
			}
			numeric := false
			switch col.Type {
			case "ID", "INT64", "INT32", "LONG", "INT", "INTEGER", "DOUBLE", "FLOAT", "FLOAT64":
				numeric = true
			}
			writeKV(col.Name, val, numeric)
		}
		sb.WriteString("}")
	}
	sb.WriteString("] AS row")
	if isNode {
		fmt.Fprintf(&sb, " CREATE (n:%s {id: row.id})", typeOrLabel)
	} else {
		// gr's Bolt endpoint does not support UNWIND + MATCH. Use MERGE instead;
		// since nodes are pre-loaded, MERGE always finds the existing node.
		fmt.Fprintf(&sb, " MERGE (a:Node {id: row.__s}) MERGE (b:Node {id: row.__e}) CREATE (a)-[r:%s]->(b)", typeOrLabel)
	}
	q := target.Query{Class: target.Write, Text: sb.String()}
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

// readFile reads a file and returns its content as a string.
func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return string(b), nil
}
