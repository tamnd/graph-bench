//go:build duckdb

// The live half of the DuckDB adapter. Build tag: duckdb, because
// go-duckdb links the DuckDB C library through cgo.
package duckdb

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	goduckdb "github.com/marcboeker/go-duckdb/v2"

	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/engine/sqlbase"
)

// Start opens the database. For the file mode it is Config["path"] if
// given, else "bench.duckdb" in a fresh temp directory removed at Close;
// for the memory mode it is the empty DSN, which is how DuckDB spells an
// in-memory database.
//
// The connector is built explicitly rather than going through
// sql.Open("duckdb", dsn), because a connector is one DuckDB database and
// every connection database/sql opens from it is a connection to that
// same database. An in-memory database opened per connection would be a
// pool of empty databases.
//
// Config keys: "path".
func (e *Engine) Start(ctx context.Context, cfg engine.Config) (engine.Session, error) {
	var dsn, dir, path string
	var cleanup func() error
	if e.mode == File {
		path = cfg.Get("path", "")
		if path == "" {
			var err error
			dir, err = os.MkdirTemp("", "graph-bench-duckdb-*")
			if err != nil {
				return nil, fmt.Errorf("duckdb: temp dir: %w", err)
			}
			path = filepath.Join(dir, "bench.duckdb")
			cleanup = func() error { return os.RemoveAll(dir) }
		}
		dsn = path
	}
	connector, err := goduckdb.NewConnector(dsn, nil)
	if err != nil {
		if cleanup != nil {
			cleanup()
		}
		return nil, fmt.Errorf("duckdb: open %q: %w", dsn, err)
	}
	db := sql.OpenDB(connector)
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		if cleanup != nil {
			cleanup()
		}
		return nil, fmt.Errorf("duckdb: open %q: %w", dsn, err)
	}
	return sqlbase.Open(&driver{mode: e.mode, path: path}, db, cleanup), nil
}

// driver is the DuckDB half of the shared relational session.
type driver struct {
	mode Mode
	path string // empty for the memory mode
}

var _ sqlbase.Driver = (*driver)(nil)

func (d *driver) Name() string {
	if d.mode == File {
		return "duckdb"
	}
	return "duckdb-" + string(d.mode)
}

// Placeholder: DuckDB takes positional ?, so the index is implied.
func (d *driver) Placeholder(int) string { return "?" }

// Schema is the two tables, with no constraint on either.
//
// This is where DuckDB and SQLite genuinely differ and the adapters have
// to differ with them. SQLite's primary key is the storage: declaring it
// is what clusters the table and there is nothing to add afterwards. A
// DuckDB primary key is a constraint backed by a separate ART index and
// changes nothing about how the rows are stored, so declaring it up front
// only means maintaining that index one row at a time through the load.
// The indexes go on after the data is in, which is what DuckDB's own
// loading guidance says and what the SQLite adapter already does for its
// second index.
func (d *driver) Schema() []string {
	return []string{
		`CREATE TABLE node (id BIGINT NOT NULL)`,
		`CREATE TABLE edge (src BIGINT NOT NULL, dst BIGINT NOT NULL)`,
	}
}

// Indexes are the three probes the workloads make: a node by id, and an
// edge by either endpoint.
//
// They are here because they were measured, not because a row store
// would have them. DuckDB prunes an equality filter with zone maps
// whether or not an index exists, and EXPLAIN says sequential scan
// either way, so it is a fair question whether an ART index earns its
// keep. On 1M nodes and 4M edges here it does, by three to five times:
// a point read went from 1.25ms to 470us, one hop from 2.42ms to 460us,
// two hops from 5.73ms to 1.64ms and three from 9.46ms to 2.40ms.
//
// What it costs is worth knowing before reading the storage column. The
// same three indexes took the database from 10.8 MiB to 127.5 MiB and
// added 9.3 seconds to the load, so on this shape of data DuckDB's ART
// indexes are an order of magnitude larger than the table they index.
// That is the trade this adapter takes, deliberately and in one place,
// because a DuckDB that answers a point read in a millisecond and a half
// would be a strawman.
func (d *driver) Indexes() []string {
	return []string{
		`CREATE UNIQUE INDEX node_id ON node (id)`,
		`CREATE INDEX edge_src ON edge (src)`,
		`CREATE INDEX edge_dst ON edge (dst)`,
	}
}

func (d *driver) LoadNodes(ctx context.Context, db *sql.DB, files []string) (int64, error) {
	return copyCSV(ctx, db, "node", []string{"id"}, files, []string{":ID"})
}

func (d *driver) LoadEdges(ctx context.Context, db *sql.DB, files []string) (int64, error) {
	return copyCSV(ctx, db, "edge", []string{"src", "dst"}, files, []string{":START_ID", ":END_ID"})
}

// copyCSV hands the file to DuckDB's own CSV reader rather than pushing
// rows through the driver one at a time, because reading a file is
// something DuckDB does natively and in parallel and it would be a poor
// account of the engine to load it the slow way.
//
// The columns are named, not positioned: read_csv names its columns from
// the header, the canonical header is typed (id:ID, :START_ID), and the
// column carrying a suffix is found by reading the header rather than by
// assuming which one it is.
func copyCSV(ctx context.Context, db *sql.DB, table string, into []string, files []string, suffixes []string) (int64, error) {
	for _, path := range files {
		names, err := sqlbase.HeaderNames(path, suffixes)
		if err != nil {
			return 0, err
		}
		selected := make([]string, len(names))
		for i, name := range names {
			selected[i] = quoteIdent(name)
		}
		stmt := fmt.Sprintf(`INSERT INTO %s (%s) SELECT %s FROM read_csv(%s, header = true)`,
			table, strings.Join(into, ", "), strings.Join(selected, ", "), quoteString(path))
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return 0, fmt.Errorf("load %s from %s: %w", table, path, err)
		}
	}
	// The count comes from the table rather than from RowsAffected: the
	// table started empty, so this is the same number, and it is the same
	// number whatever a driver decides an insert's affected-row count
	// means.
	var n int64
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+table).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

func quoteIdent(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func quoteString(s string) string {
	return `'` + strings.ReplaceAll(s, `'`, `''`) + `'`
}

// LoadMethod: read_csv is DuckDB's native bulk loader, the same one COPY
// uses underneath.
func (d *driver) LoadMethod() string { return "read_csv" }

func (d *driver) Analyze(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, "ANALYZE")
	return err
}

func (d *driver) Version(ctx context.Context, db *sql.DB) (string, error) {
	var v string
	if err := db.QueryRowContext(ctx, "SELECT version()").Scan(&v); err != nil {
		return "", err
	}
	// DuckDB answers "v1.4.2" and every other engine here answers without
	// the v, so the prefix comes off and the string stays a version.
	return strings.TrimPrefix(v, "v"), nil
}

// DiskBytes is what the database occupies. The checkpoint comes first so
// that the write-ahead log is folded into the file and the pages in it
// are counted where they end up rather than twice; after it the log is
// empty and the file is the whole story.
//
// The number comes from DuckDB's own block accounting rather than from
// the file size, because the two are not the same: a DuckDB file is
// allocated in blocks and holds on to free ones, so the file can be
// larger than the data in it, and used_blocks times block_size is what
// the database actually occupies.
//
// One number to expect and not be surprised by: DuckDB's default block is
// 256 KiB, against SQLite's 4 KiB page, so the smallest non-empty DuckDB
// database is 256 KiB and a small graph reports a footprint that is
// almost entirely block granularity. That is a real property of the
// format rather than an artifact of the measurement, and it is a reason
// to read the storage column on a dataset big enough for the block size
// to stop dominating.
//
// The memory mode reports -1, which is what LoadStats means by not
// meaningful. Its block accounting is all zeros, because an in-memory
// DuckDB database has no blocks, and 0 would read as a database that
// stores nothing rather than as a database with nowhere to store it.
func (d *driver) DiskBytes(ctx context.Context, db *sql.DB) (int64, error) {
	if d.mode != File {
		return -1, nil
	}
	if _, err := db.ExecContext(ctx, "CHECKPOINT"); err != nil {
		return 0, err
	}
	var used, block int64
	err := db.QueryRowContext(ctx,
		`SELECT used_blocks, block_size FROM pragma_database_size()`).Scan(&used, &block)
	if err != nil {
		return 0, err
	}
	return used * block, nil
}
