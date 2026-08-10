//go:build bolt

// Package neo4j is the adapter for Neo4j community edition over the shared
// Bolt plane (engine/bolt). It implements the engine SPI from
// notes/Spec/2064g/bench/03-engine-spi.md; the adapter contract is
// notes/Spec/2064g/bench/04-adapters.md section 3.
//
// Load path, in preference order (choice stamped in LoadStats.Method):
//  1. neo4j-admin database import full ("admin-import") — offline import,
//     attempted only when Config["admin_import"] == "1" because it requires
//     a stopped database and host-level access.
//  2. LOAD CSV from the server's import directory ("load-csv"), with
//     per-label CREATE INDEX on id afterwards.
//  3. Batched UNWIND ... CREATE over Bolt ("unwind"), refusing datasets
//     over 10 M edges — an UNWIND load at that scale would be a strawman
//     (F2), not a bulk load.
//
// Build tag: bolt. Use -tags bolt to compile this package.
package neo4j

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/engine/bolt"
)

// maxUnwindEdges is the largest dataset the UNWIND fallback will load.
const maxUnwindEdges = 10_000_000

// unwindBatchSize is the number of CSV rows per UNWIND statement.
const unwindBatchSize = 500

// New returns the Neo4j engine descriptor. Nothing happens until Start.
func New() engine.Engine { return neoEngine{} }

type neoEngine struct{}

func (neoEngine) Info() engine.Info {
	return engine.Info{
		Name:     "neo4j",
		Plane:    engine.Bolt,
		Dialects: []engine.Dialect{engine.Cypher25, engine.Cypher},
		Caps: engine.Capabilities{
			Transactions:   true,
			BulkLoad:       true,
			Deletes:        true,
			VarLengthPaths: true,
			ShortestPaths:  true,
			PathPredicates: true,
			Algorithms:     nil, // GDS is enterprise-adjacent; community SKIPs analytics
			MaxConcurrency: 0,   // driver pool bounds it (Config["pool"], default 64)
			Persistent:     true,
		},
	}
}

// connConfig is the resolved connection configuration.
type connConfig struct {
	URI  string
	User string
	Pass string
	DB   string
	Pool int
}

// resolveConfig resolves connection settings from Config with environment
// fallbacks: uri ($NEO4J_URI, default bolt://127.0.0.1:7687), user
// ($NEO4J_USER, default neo4j), pass ($NEO4J_PASS, or $NEO4J_PASSWORD),
// database (neo4j), pool (default 64).
//
// Both password spellings are accepted because NEO4J_PASSWORD is the one
// operators already have set: it is half of the official image's NEO4J_AUTH
// convention and what the surrounding tooling uses. Honoring only NEO4J_PASS
// turns that near-miss into an authentication failure the harness reports as
// "server unreachable", which sends the reader looking at the network.
func resolveConfig(cfg engine.Config) connConfig {
	c := connConfig{
		URI:  cfg.Get("uri", envOr("NEO4J_URI", "bolt://127.0.0.1:7687")),
		User: cfg.Get("user", envOr("NEO4J_USER", "neo4j")),
		Pass: cfg.Get("pass", envOr("NEO4J_PASS", os.Getenv("NEO4J_PASSWORD"))),
		DB:   cfg.Get("database", "neo4j"),
		Pool: 64,
	}
	if n, err := strconv.Atoi(cfg.Get("pool", "64")); err == nil && n > 0 {
		c.Pool = n
	}
	return c
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func (neoEngine) Start(ctx context.Context, cfg engine.Config) (engine.Session, error) {
	c := resolveConfig(cfg)
	pool, err := bolt.Open(ctx, c.URI, c.User, c.Pass, c.DB, bolt.WithPoolSize(c.Pool))
	if err != nil {
		return nil, fmt.Errorf("neo4j: open %s: %w", c.URI, err)
	}
	if err := pool.Ping(ctx); err != nil {
		_ = pool.Close(ctx)
		// A rejected credential is not an unreachable server, and saying so
		// sends the reader to inspect the network instead of the password.
		if strings.Contains(err.Error(), "Unauthorized") {
			return nil, fmt.Errorf(
				"neo4j: authentication failed at %s as user %q (set $NEO4J_PASS or $NEO4J_PASSWORD, or Config[\"pass\"]): %w",
				c.URI, c.User, err)
		}
		return nil, fmt.Errorf("neo4j: unreachable at %s (is the server up? see docker/docker-compose.yml): %w", c.URI, err)
	}
	return &session{pool: pool, cfg: cfg}, nil
}

// session is a live connection to a started Neo4j.
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

// Load bulk-loads the dataset. See the package comment for the load-path
// preference order.
func (s *session) Load(ctx context.Context, ds engine.Dataset) (engine.LoadStats, error) {
	start := time.Now()

	if stmts := ds.Statements(); len(stmts) > 0 {
		if err := s.wipe(ctx); err != nil {
			return engine.LoadStats{}, err
		}
		for _, st := range stmts {
			if err := s.runWrite(ctx, st); err != nil {
				return engine.LoadStats{}, fmt.Errorf("neo4j: setup statement: %w", err)
			}
		}
		return engine.LoadStats{Duration: time.Since(start), BytesOnDisk: -1, Method: "statements"}, nil
	}

	if s.cfg.Get("admin_import", "") == "1" {
		return s.adminImport(ctx, ds, start)
	}

	if err := s.wipe(ctx); err != nil {
		return engine.LoadStats{}, err
	}

	// Resolve the import directory once. Ask the running server for its
	// runtime value first so we use the same path the server does, then
	// fall back to local-install heuristics.
	importDir := s.queryImportDir(ctx)
	if importDir == "" {
		importDir = detectImportDir(s.cfg)
	}
	method := "load-csv"
	if importDir == "" {
		method = "unwind"
		if m := ds.Manifest(); m != nil {
			if err := checkUnwindScale(m.Invariants.EdgeCount); err != nil {
				return engine.LoadStats{}, err
			}
		}
	}

	schema := ds.Schema()
	listDelim := listDelimiter(ds)
	var nodes, edges int64

	// Nodes first, then an index per label on id, then await population,
	// then edges: without the index every endpoint MATCH in the edge load
	// is a full scan.
	for _, label := range sortedKeys(schema.Nodes) {
		ns := schema.Nodes[label]
		files, err := ds.NodeFiles(label)
		if err != nil {
			return engine.LoadStats{}, fmt.Errorf("neo4j: node files %s: %w", label, err)
		}
		types := bolt.PropTypes(append([]engine.Column{ns.ID}, ns.Properties...))
		for _, f := range files {
			cols, rows, err := bolt.ReadCSV(f, types)
			if err != nil {
				return engine.LoadStats{}, err
			}
			var n int64
			if importDir != "" {
				n, err = s.loadCSVFile(ctx, importDir, cols, rows, label, true, "", "", listDelim)
			} else {
				n, err = s.loadUnwind(ctx, cols, rows, label, true, "", "")
			}
			if err != nil {
				return engine.LoadStats{}, fmt.Errorf("neo4j: load nodes %s from %s: %w", label, filepath.Base(f), err)
			}
			nodes += n
		}
		idx := fmt.Sprintf("CREATE INDEX IF NOT EXISTS FOR (n:%s) ON (n.id)", label)
		if err := s.runWrite(ctx, idx); err != nil {
			return engine.LoadStats{}, fmt.Errorf("neo4j: create index on %s: %w", label, err)
		}
	}
	if err := s.runWrite(ctx, "CALL db.awaitIndexes()"); err != nil {
		return engine.LoadStats{}, fmt.Errorf("neo4j: await indexes: %w", err)
	}

	for _, typ := range sortedKeys(schema.Rels) {
		rs := schema.Rels[typ]
		files, err := ds.RelFiles(typ)
		if err != nil {
			return engine.LoadStats{}, fmt.Errorf("neo4j: rel files %s: %w", typ, err)
		}
		types := bolt.PropTypes(rs.Properties)
		for _, f := range files {
			cols, rows, err := bolt.ReadCSV(f, types)
			if err != nil {
				return engine.LoadStats{}, err
			}
			var n int64
			if importDir != "" {
				n, err = s.loadCSVFile(ctx, importDir, cols, rows, typ, false, rs.Start, rs.End, listDelim)
			} else {
				n, err = s.loadUnwind(ctx, cols, rows, typ, false, rs.Start, rs.End)
			}
			if err != nil {
				return engine.LoadStats{}, fmt.Errorf("neo4j: load rels %s from %s: %w", typ, filepath.Base(f), err)
			}
			edges += n
		}
	}

	return engine.LoadStats{
		Duration:    time.Since(start),
		Nodes:       nodes,
		Edges:       edges,
		BytesOnDisk: -1,
		Method:      method,
	}, nil
}

// checkUnwindScale refuses an UNWIND bulk load beyond maxUnwindEdges (F2).
func checkUnwindScale(edges int64) error {
	if edges > maxUnwindEdges {
		return fmt.Errorf("neo4j: refusing UNWIND load of %d edges (limit %d): an UNWIND bulk load at this scale is a strawman, not the documented bulk path (F2); mount the dataset into the server's import directory (Config[\"import_dir\"]) or use Config[\"admin_import\"]=1", edges, maxUnwindEdges)
	}
	return nil
}

// wipe removes data left over from previous runs; Neo4j persists across
// connections.
func (s *session) wipe(ctx context.Context) error {
	if err := s.runWrite(ctx, "MATCH (n) DETACH DELETE n"); err != nil {
		return fmt.Errorf("neo4j: wipe: %w", err)
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

// queryImportDir asks the running server for server.directories.import.
// Returns "" on any failure or when the directory is not locally writable
// (a remote server's import dir is not reachable from this process).
func (s *session) queryImportDir(ctx context.Context) string {
	res, err := s.pool.Run(ctx, engine.Op{
		Text: "CALL dbms.listConfig() YIELD name, value WHERE name = 'server.directories.import' RETURN value",
	})
	if err != nil {
		return ""
	}
	defer res.Close()
	if res.Next() {
		if row := res.Row(); len(row) > 0 {
			if dir, ok := row[0].(string); ok && isWritableDir(dir) {
				return dir
			}
		}
	}
	return ""
}

// detectImportDir returns the Neo4j import directory, or "" when it cannot
// be found. Priority:
//  1. Config["import_dir"] — explicit override from the run config.
//  2. `neo4j-admin server home` + /import — standard layout for homebrew
//     and tarball installs.
//  3. /opt/homebrew/var/neo4j/import — homebrew default (var tree).
//  4. /opt/homebrew/Cellar/neo4j/*/libexec/import — homebrew libexec tree,
//     which is what server.directories.import=import resolves to.
//
// The directory must exist and be writable; a found but unwritable path is
// treated as not found so the caller falls back to UNWIND batching.
func detectImportDir(cfg engine.Config) string {
	if v := cfg.Get("import_dir", ""); v != "" {
		return v
	}
	if home := neo4jAdminHome(); home != "" {
		if dir := filepath.Join(home, "import"); isWritableDir(dir) {
			return dir
		}
	}
	if isWritableDir("/opt/homebrew/var/neo4j/import") {
		return "/opt/homebrew/var/neo4j/import"
	}
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

// neo4jAdminHome runs `neo4j-admin server home` and returns the trimmed
// output, or "" on any error.
func neo4jAdminHome() string {
	env := os.Environ()
	if os.Getenv("JAVA_HOME") == "" {
		// Known homebrew OpenJDK path on macOS Apple Silicon.
		if _, err := os.Stat("/opt/homebrew/opt/openjdk@21/bin/java"); err == nil {
			env = append(env, "JAVA_HOME=/opt/homebrew/opt/openjdk@21")
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

// isWritableDir reports whether path is an existing directory the current
// process can write into.
func isWritableDir(path string) bool {
	fi, err := os.Stat(path)
	if err != nil || !fi.IsDir() {
		return false
	}
	f, err := os.CreateTemp(path, ".graph-bench-probe-*")
	if err != nil {
		return false
	}
	f.Close()
	os.Remove(f.Name())
	return true
}

// adminImport runs `neo4j-admin database import full` against the dataset's
// canonical CSV files (their headers are already in neo4j-admin annotated
// form). It requires a stopped database and host-level access, which is why
// it is attempted only on explicit request (Config["admin_import"] == "1")
// and fails loudly rather than falling back.
func (s *session) adminImport(ctx context.Context, ds engine.Dataset, start time.Time) (engine.LoadStats, error) {
	schema := ds.Schema()
	args := []string{"database", "import", "full", "--overwrite-destination"}
	for _, label := range sortedKeys(schema.Nodes) {
		files, err := ds.NodeFiles(label)
		if err != nil {
			return engine.LoadStats{}, fmt.Errorf("neo4j: node files %s: %w", label, err)
		}
		args = append(args, "--nodes="+label+"="+strings.Join(files, ","))
	}
	for _, typ := range sortedKeys(schema.Rels) {
		files, err := ds.RelFiles(typ)
		if err != nil {
			return engine.LoadStats{}, fmt.Errorf("neo4j: rel files %s: %w", typ, err)
		}
		args = append(args, "--relationships="+typ+"="+strings.Join(files, ","))
	}
	args = append(args, s.cfg.Get("database", "neo4j"))

	cmd := exec.CommandContext(ctx, s.cfg.Get("admin_bin", "neo4j-admin"), args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return engine.LoadStats{}, fmt.Errorf("neo4j-admin import: %w: %s", err, out)
	}
	stats := engine.LoadStats{Duration: time.Since(start), BytesOnDisk: -1, Method: "admin-import"}
	if m := ds.Manifest(); m != nil {
		stats.Nodes = m.Invariants.NodeCount
		stats.Edges = m.Invariants.EdgeCount
	}
	return stats, nil
}

// loadCSVFile copies one dataset file into the server's import directory
// (simplified: __id or __s/__e first, then named property columns) and
// issues a LOAD CSV WITH HEADERS query with typed coercion.
func (s *session) loadCSVFile(ctx context.Context, importDir string, cols []bolt.Column, rows []string, typeOrLabel string, isNode bool, startLabel, endLabel, listDelim string) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	tmpPath, err := writeImportCSV(importDir, cols, rows, isNode)
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmpPath)

	cypher := buildLoadCSVCypher(filepath.Base(tmpPath), cols, typeOrLabel, isNode, startLabel, endLabel, listDelim)
	if err := s.runWrite(ctx, cypher); err != nil {
		return 0, fmt.Errorf("LOAD CSV %s: %w", filepath.Base(tmpPath), err)
	}
	return int64(len(rows)), nil
}

// writeImportCSV writes the simplified CSV (plain headers, structural
// columns remapped to __id or __s/__e) into dir and returns its path.
func writeImportCSV(dir string, cols []bolt.Column, rows []string, isNode bool) (string, error) {
	tmp, err := os.CreateTemp(dir, "gb-*.csv")
	if err != nil {
		return "", fmt.Errorf("create import temp: %w", err)
	}
	defer tmp.Close()

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
	if isNode {
		sb.WriteString("__id")
	} else {
		sb.WriteString("__s,__e")
	}
	for _, col := range cols {
		if col.Name != "" && !col.Structural() {
			sb.WriteString(",")
			sb.WriteString(col.Name)
		}
	}
	sb.WriteString("\n")

	field := func(fields []string, idx int) string {
		if idx >= 0 && idx < len(fields) {
			return fields[idx]
		}
		return "0"
	}
	for _, row := range rows {
		fields := strings.Split(row, ",")
		if isNode {
			sb.WriteString(field(fields, idIdx))
		} else {
			sb.WriteString(field(fields, sidIdx))
			sb.WriteString(",")
			sb.WriteString(field(fields, eidIdx))
		}
		for j, col := range cols {
			if col.Name == "" || col.Structural() {
				continue
			}
			sb.WriteString(",")
			if j < len(fields) {
				sb.WriteString(fields[j])
			}
		}
		sb.WriteString("\n")
	}
	if _, err := tmp.WriteString(sb.String()); err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("write import csv: %w", err)
	}
	return tmp.Name(), nil
}

// buildLoadCSVCypher builds the LOAD CSV WITH HEADERS FROM ... statement
// for one simplified import file. It coerces __id/__s/__e to integers and
// maps named property columns through coerceExpr. No CALL IN TRANSACTIONS
// wrapper: benchmark datasets load in one transaction, and the refusal in
// checkUnwindScale never applies here (LOAD CSV is server-side).
func buildLoadCSVCypher(basename string, cols []bolt.Column, typeOrLabel string, isNode bool, startLabel, endLabel, listDelim string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "LOAD CSV WITH HEADERS FROM 'file:///%s' AS row\n", basename)
	props := func() string {
		var p strings.Builder
		for _, col := range cols {
			if col.Name == "" || col.Structural() {
				continue
			}
			fmt.Fprintf(&p, ", %s: %s", col.Name, coerceExpr("row."+col.Name, col.Type, listDelim))
		}
		return p.String()
	}
	if isNode {
		fmt.Fprintf(&sb, "CREATE (n:%s {id: toInteger(row.__id)%s})", typeOrLabel, props())
		return sb.String()
	}
	fmt.Fprintf(&sb, "MATCH (a:%s {id: toInteger(row.__s)})\n", startLabel)
	fmt.Fprintf(&sb, "MATCH (b:%s {id: toInteger(row.__e)})\n", endLabel)
	if p := props(); p != "" {
		fmt.Fprintf(&sb, "CREATE (a)-[r:%s {%s}]->(b)", typeOrLabel, strings.TrimPrefix(p, ", "))
	} else {
		fmt.Fprintf(&sb, "CREATE (a)-[r:%s]->(b)", typeOrLabel)
	}
	return sb.String()
}

// coerceExpr returns a Cypher expression coercing a LOAD CSV string value
// to the typed-CSV column type.
func coerceExpr(expr, typ, listDelim string) string {
	switch typ {
	case "INT64", "INT32", "LONG", "INT", "INTEGER", "ID", "START_ID", "END_ID":
		return "toInteger(" + expr + ")"
	case "FLOAT64", "DOUBLE", "FLOAT":
		return "toFloat(" + expr + ")"
	case "BOOL", "BOOLEAN":
		return "toBoolean(" + expr + ")"
	case "DATE":
		return "date(" + expr + ")"
	case "DATETIME":
		return "datetime(" + expr + ")"
	case "STRING[]":
		return "split(" + expr + ", '" + listDelim + "')"
	default:
		return expr
	}
}

// loadUnwind loads rows via batched UNWIND ... CREATE statements — the
// last-resort path for servers whose import directory is unreachable.
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
			sb.WriteString(cypherLiteral(v, typ))
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
			writeKV("__s", field(sidIdx), "START_ID")
			writeKV("__e", field(eidIdx), "END_ID")
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

// cypherLiteral renders one CSV field as a Cypher literal.
//
// The declared column type says how a value is meant to be read, but the value
// itself decides how it can be written. An :ID column holds integers in the
// canonical LDBC datasets and opaque strings in others, and a bare A0 is not an
// identifier to Cypher — it is a reference to an undefined variable, so the
// whole batch fails to parse. A type that permits a bare literal therefore has
// to prove the field really is one; whatever fails that check gets quoted.
func cypherLiteral(v, typ string) string {
	if v == "" {
		return "null"
	}
	switch typ {
	case "ID", "START_ID", "END_ID", "INT64", "INT32", "LONG", "INT", "INTEGER":
		if _, err := strconv.ParseInt(v, 10, 64); err == nil {
			return v
		}
	case "FLOAT64", "DOUBLE", "FLOAT":
		if _, err := strconv.ParseFloat(v, 64); err == nil {
			return v
		}
	case "BOOL", "BOOLEAN":
		if v == "true" || v == "false" {
			return v
		}
	}
	return quoteCypher(v)
}

// quoteCypher renders v as a double-quoted Cypher string literal. Backslash is
// escaped before quote, not after: a value ending in a backslash would
// otherwise escape its own closing quote and splice the rest of the batch into
// the string, turning a data error into a parse error hundreds of rows away.
func quoteCypher(v string) string {
	v = strings.ReplaceAll(v, `\`, `\\`)
	v = strings.ReplaceAll(v, `"`, `\"`)
	return `"` + v + `"`
}

// listDelimiter returns the dataset's list delimiter, defaulting to ";".
func listDelimiter(ds engine.Dataset) string {
	if m := ds.Manifest(); m != nil && m.ListDelimiter != "" {
		return m.ListDelimiter
	}
	return ";"
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
