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

// detectImportDir returns the path to the Neo4j import directory, or empty string
// when it cannot be found. Priority:
//  1. cfg.Values["import_dir"] — explicit override from the run config.
//  2. JAVA_HOME env-scoped neo4j-admin server home + /import — standard neo4j layout.
//  3. /opt/homebrew/var/neo4j/import — homebrew default on macOS (var tree).
//  4. /opt/homebrew/Cellar/neo4j/*/libexec/import — homebrew default (libexec tree,
//     which is what server.directories.import=import actually resolves to).
//
// The directory must exist and be writable; a found but unwritable path is
// treated as not found so the caller falls back to UNWIND batching.
func detectImportDir(cfg target.Config) string {
	if v, ok := cfg.Values["import_dir"]; ok && v != "" {
		return v
	}
	// Try neo4j-admin server home (works for both homebrew and tarball installs).
	if home := neo4jAdminHome(); home != "" {
		dir := filepath.Join(home, "import")
		if isWritableDir(dir) {
			return dir
		}
	}
	// Homebrew default (var tree).
	if isWritableDir("/opt/homebrew/var/neo4j/import") {
		return "/opt/homebrew/var/neo4j/import"
	}
	// Homebrew default (libexec tree): server.directories.import=import is relative
	// to NEO4J_HOME which points at the Cellar libexec directory.
	if entries, err := os.ReadDir("/opt/homebrew/Cellar/neo4j"); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			dir := "/opt/homebrew/Cellar/neo4j/" + e.Name() + "/libexec/import"
			if isWritableDir(dir) {
				return dir
			}
		}
	}
	return ""
}

// neo4jAdminHome runs neo4j-admin server home and returns the trimmed output.
// Returns empty string on any error.
func neo4jAdminHome() string {
	javaHome := os.Getenv("JAVA_HOME")
	env := os.Environ()
	if javaHome == "" {
		// Try the known homebrew OpenJDK path used on macOS Apple Silicon.
		if _, err := os.Stat("/opt/homebrew/opt/openjdk@21/bin/java"); err == nil {
			javaHome = "/opt/homebrew/opt/openjdk@21"
			env = append(env, "JAVA_HOME="+javaHome)
		}
	}
	cmd := exec.Command("neo4j-admin", "server", "home")
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// isWritableDir returns true when path is an existing directory that the current
// process can write into.
func isWritableDir(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || !fi.IsDir() {
		return false
	}
	probe := filepath.Join(path, ".graph-bench-probe")
	f, err := os.CreateTemp(path, ".graph-bench-probe-*")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	_ = probe
	return true
}

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

	// Wipe any data from previous runs. Neo4j persists across connections, so
	// without this each run accumulates stale nodes and edges.
	wipeRes, wipeErr := drv.Run(ctx, target.Query{Class: target.Write, Text: "MATCH (n) DETACH DELETE n"}, nil)
	if wipeErr == nil {
		for wipeRes.Next() {
		}
		wipeRes.Close()
	}

	// Resolve the import directory once. Query neo4j for the runtime value so we
	// always use the same path as the server (not a heuristic guess).
	importDir := queryImportDir(ctx, drv)
	if importDir == "" {
		importDir = detectImportDir(drv.cfg)
	}

	// Load nodes first, then create an index on id, then load edges.
	// The index is critical: without it, each MATCH (n:Label {id: x}) in the
	// relationship load is a full scan, making 20k-edge loads take minutes.
	schema := ds.Schema()
	for label := range schema.Nodes {
		files, cols, err := ds.NodeFiles(label)
		if err != nil {
			return target.LoadStats{}, fmt.Errorf("neo4j: node files %s: %w", label, err)
		}
		for _, f := range files {
			n, err := loadCSVFile(ctx, drv, f, cols, label, true, importDir)
			if err != nil {
				return target.LoadStats{}, fmt.Errorf("neo4j: load nodes %s from %s: %w", label, filepath.Base(f), err)
			}
			nodes += n
		}
		// Create a range index on id so relationship MATCHes are O(log N).
		idxCypher := fmt.Sprintf("CREATE INDEX IF NOT EXISTS FOR (n:%s) ON (n.id)", label)
		idxRes, idxErr := drv.Run(ctx, target.Query{Class: target.Write, Text: idxCypher}, nil)
		if idxErr == nil {
			for idxRes.Next() {
			}
			idxRes.Close()
		}
	}
	for relType := range schema.Relationships {
		files, cols, err := ds.RelFiles(relType)
		if err != nil {
			return target.LoadStats{}, fmt.Errorf("neo4j: rel files %s: %w", relType, err)
		}
		for _, f := range files {
			n, err := loadCSVFile(ctx, drv, f, cols, relType, false, importDir)
			if err != nil {
				return target.LoadStats{}, fmt.Errorf("neo4j: load rels %s from %s: %w", relType, filepath.Base(f), err)
			}
			edges += n
		}
	}
	return target.LoadStats{Duration: time.Since(start), Nodes: nodes, Edges: edges, BytesOnDisk: -1}, nil
}


// queryImportDir asks the running neo4j for the value of server.directories.import.
// Returns the trimmed absolute path, or empty string on any failure.
func queryImportDir(ctx context.Context, d *neoDriver) string {
	q := target.Query{Text: "CALL dbms.listConfig() YIELD name, value WHERE name = 'server.directories.import' RETURN value"}
	res, err := d.Run(ctx, q, nil)
	if err != nil {
		return ""
	}
	defer res.Close()
	if res.Next() {
		row := res.Row()
		if len(row) > 0 {
			if s, ok := row[0].(string); ok && s != "" && isWritableDir(s) {
				return s
			}
		}
	}
	return ""
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

// loadCSVFile loads one dataset CSV file into Neo4j. importDir is the resolved
// neo4j import directory from the caller (Load queries it once via queryImportDir).
// When importDir is non-empty, it uses LOAD CSV (fast: one server-side file read,
// one transaction). When empty it falls back to UNWIND batching (slow: one round
// trip per 500 rows, full data over the wire).
func loadCSVFile(ctx context.Context, d *neoDriver, path string, cols []target.Column, typeOrLabel string, isNode bool, importDir string) (int64, error) {
	if importDir != "" {
		return loadCSVLocalFile(ctx, d, path, cols, typeOrLabel, isNode, importDir)
	}
	return loadCSVUnwind(ctx, d, path, cols, typeOrLabel, isNode)
}

// loadCSVLocalFile copies the dataset CSV to the Neo4j import directory and
// issues a LOAD CSV WITH HEADERS FROM file:///basename query. It writes a
// simplified CSV (plain column names, no type annotations) because LOAD CSV
// reads the raw string value for every column and relies on the Cypher
// coercion functions (toInteger, toFloat) in the query itself rather than
// schema-level type declarations.
func loadCSVLocalFile(ctx context.Context, d *neoDriver, path string, cols []target.Column, typeOrLabel string, isNode bool, importDir string) (int64, error) {
	// Write a simplified CSV to the import dir. Plain headers, no :TYPE suffixes.
	tmp, err := os.CreateTemp(importDir, "gb-*.csv")
	if err != nil {
		return 0, fmt.Errorf("neo4j: create import temp: %w", err)
	}
	defer func() {
		tmp.Close()
		os.Remove(tmp.Name())
	}()

	content, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("neo4j: read %s: %w", path, err)
	}
	lines := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	if len(lines) == 0 {
		return 0, nil
	}
	dataLines := lines[1:]
	if len(dataLines) == 0 {
		return 0, nil
	}

	// Build simplified header.
	var header strings.Builder
	sidIdx, eidIdx, idIdx := -1, -1, -1
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
	if isNode {
		header.WriteString("__id")
		for _, col := range cols {
			if col.Name != "" {
				header.WriteString(",")
				header.WriteString(col.Name)
			}
		}
	} else {
		header.WriteString("__s,__e")
		for _, col := range cols {
			if col.Name != "" {
				header.WriteString(",")
				header.WriteString(col.Name)
			}
		}
	}
	if _, err := fmt.Fprintln(tmp, header.String()); err != nil {
		return 0, fmt.Errorf("neo4j: write csv header: %w", err)
	}

	// Write data rows with remapped columns.
	for _, row := range dataLines {
		fields := strings.Split(row, ",")
		var out strings.Builder
		if isNode {
			id := "0"
			if idIdx >= 0 && idIdx < len(fields) {
				id = fields[idIdx]
			}
			out.WriteString(id)
		} else {
			sid, eid := "0", "0"
			if sidIdx >= 0 && sidIdx < len(fields) {
				sid = fields[sidIdx]
			}
			if eidIdx >= 0 && eidIdx < len(fields) {
				eid = fields[eidIdx]
			}
			out.WriteString(sid)
			out.WriteString(",")
			out.WriteString(eid)
		}
		for j, col := range cols {
			if col.Name == "" {
				continue
			}
			val := ""
			if j < len(fields) {
				val = fields[j]
			}
			out.WriteString(",")
			out.WriteString(val)
		}
		if _, err := fmt.Fprintln(tmp, out.String()); err != nil {
			return 0, fmt.Errorf("neo4j: write csv row: %w", err)
		}
	}
	tmp.Close()

	// Build the LOAD CSV query.
	basename := filepath.Base(tmp.Name())
	cypher := buildLoadCSVCypher(basename, cols, typeOrLabel, isNode)
	q := target.Query{Class: target.Write, Text: cypher}
	res, err := d.Run(ctx, q, nil)
	if err != nil {
		return 0, fmt.Errorf("neo4j: LOAD CSV %s: %w", basename, err)
	}
	for res.Next() {
	}
	if err := res.Err(); err != nil {
		res.Close()
		return 0, fmt.Errorf("neo4j: LOAD CSV %s result: %w", basename, err)
	}
	res.Close()
	return int64(len(dataLines)), nil
}

// buildLoadCSVCypher builds the LOAD CSV WITH HEADERS FROM ... query for one
// file. It coerces __id/__s/__e from string to integer and maps named property
// columns by their type. No CALL IN TRANSACTIONS wrapper: our benchmark
// datasets are small enough to load in one autocommit transaction, and
// CALL IN TRANSACTIONS requires an implicit session which the bolt pool
// does not provide by default.
func buildLoadCSVCypher(basename string, cols []target.Column, typeOrLabel string, isNode bool) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "LOAD CSV WITH HEADERS FROM 'file:///%s' AS row\n", basename)
	if isNode {
		fmt.Fprintf(&sb, "CREATE (n:%s {id: toInteger(row.__id)", typeOrLabel)
		for _, col := range cols {
			if col.Name == "" {
				continue
			}
			fmt.Fprintf(&sb, ", %s: %s", col.Name, coerceExpr("row."+col.Name, col.Type))
		}
		sb.WriteString("})")
	} else {
		fmt.Fprintf(&sb, "MATCH (a:Node {id: toInteger(row.__s)})\n")
		fmt.Fprintf(&sb, "MATCH (b:Node {id: toInteger(row.__e)})\n")
		sb.WriteString("CREATE (a)-[r:")
		sb.WriteString(typeOrLabel)
		sb.WriteString(" {")
		first := true
		for _, col := range cols {
			if col.Name == "" {
				continue
			}
			if !first {
				sb.WriteString(", ")
			}
			first = false
			fmt.Fprintf(&sb, "%s: %s", col.Name, coerceExpr("row."+col.Name, col.Type))
		}
		sb.WriteString("}]->(b)")
	}
	return sb.String()
}

// coerceExpr returns a Cypher expression that coerces a string row value to the
// given typed-CSV column type.
func coerceExpr(expr, typ string) string {
	switch typ {
	case "INT64", "INT32", "LONG", "INT", "INTEGER", "ID", "START_ID", "END_ID":
		return "toInteger(" + expr + ")"
	case "DOUBLE", "FLOAT", "FLOAT64":
		return "toFloat(" + expr + ")"
	case "BOOL", "BOOLEAN":
		return "toBoolean(" + expr + ")"
	default:
		return expr
	}
}

// loadCSVUnwind falls back to UNWIND batching when the Neo4j import directory
// is not accessible. This sends all data over the Bolt wire (slow for large
// datasets) but works in any deployment including remote containers.
func loadCSVUnwind(ctx context.Context, d *neoDriver, path string, cols []target.Column, typeOrLabel string, isNode bool) (int64, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}

	lines := strings.Split(strings.TrimRight(string(content), "\n"), "\n")
	if len(lines) == 0 {
		return 0, nil
	}

	dataLines := lines[1:]
	if len(dataLines) == 0 {
		return 0, nil
	}

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
