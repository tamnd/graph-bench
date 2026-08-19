//go:build sqlite

// The live half of the SQLite adapter. Build tag: sqlite, because
// go-sqlite3 compiles the amalgamation through cgo and needs a C
// compiler on the machine.
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	sqlite3 "github.com/mattn/go-sqlite3"

	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/engine/sqlbase"
)

// pragmas are applied to every connection as it opens, not once at Start:
// database/sql keeps a pool, a pragma is per connection, and a query that
// happened to land on a connection nobody configured would read through a
// 2 MiB cache and quietly cost several times what the others did. The
// journal mode is the exception, being a property of the file, but it is
// harmless to repeat.
func pragmas(mode Mode) []string {
	journal, sync := "WAL", "NORMAL"
	switch mode {
	case Sync:
		sync = "FULL"
	case Memory:
		journal, sync = "MEMORY", "OFF"
	}
	return []string{
		"PRAGMA journal_mode = " + journal,
		"PRAGMA synchronous = " + sync,
		"PRAGMA cache_size = -262144",
		"PRAGMA mmap_size = 268435456",
		"PRAGMA temp_store = MEMORY",
		"PRAGMA busy_timeout = 10000",
		"PRAGMA foreign_keys = OFF",
	}
}

// register installs one database/sql driver per mode, each with a connect
// hook that runs that mode's pragmas. Registering is global and panics on
// a duplicate name, so it happens once per mode however many sessions run.
var registered sync.Map

func register(mode Mode) string {
	name := "sqlite3-graph-bench-" + string(mode)
	if _, loaded := registered.LoadOrStore(name, true); loaded {
		return name
	}
	stmts := pragmas(mode)
	sql.Register(name, &sqlite3.SQLiteDriver{
		ConnectHook: func(c *sqlite3.SQLiteConn) error {
			for _, stmt := range stmts {
				if _, err := c.Exec(stmt, nil); err != nil {
					return fmt.Errorf("%s: %w", stmt, err)
				}
			}
			return nil
		},
	})
	return name
}

// Start opens the database. For the file modes it is Config["path"] if
// given, else "bench.sqlite" in a fresh temp directory removed at Close.
// The memory mode opens a named shared-cache database instead, so the
// several connections in the pool see one database rather than one empty
// database each.
//
// Config keys: "path".
func (e *Engine) Start(ctx context.Context, cfg engine.Config) (engine.Session, error) {
	driverName := register(e.mode)
	var dsn, dir, path string
	var cleanup func() error
	if e.mode == Memory {
		// The name is per session so two sessions in one process do not
		// share a database, and cache=shared is what makes the pool's
		// connections agree on which database that is.
		dsn = fmt.Sprintf("file:graph-bench-%p?mode=memory&cache=shared", e)
	} else {
		path = cfg.Get("path", "")
		if path == "" {
			var err error
			dir, err = os.MkdirTemp("", "graph-bench-sqlite-*")
			if err != nil {
				return nil, fmt.Errorf("sqlite: temp dir: %w", err)
			}
			path = filepath.Join(dir, "bench.sqlite")
			cleanup = func() error { return os.RemoveAll(dir) }
		}
		dsn = "file:" + path
	}
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		if cleanup != nil {
			cleanup()
		}
		return nil, fmt.Errorf("sqlite: open %s: %w", dsn, err)
	}
	// A memory database exists only while a connection to it is open, so
	// the pool never drops to nothing.
	if e.mode == Memory {
		db.SetConnMaxIdleTime(0)
		db.SetMaxIdleConns(64)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		if cleanup != nil {
			cleanup()
		}
		return nil, fmt.Errorf("sqlite: open %s: %w", dsn, err)
	}
	return sqlbase.Open(&driver{mode: e.mode, path: path}, db, cleanup), nil
}

// driver is the SQLite half of the shared relational session.
type driver struct {
	mode Mode
	path string // empty for the memory mode
}

var _ sqlbase.Driver = (*driver)(nil)

func (d *driver) Name() string {
	if d.mode == WAL {
		return "sqlite"
	}
	return "sqlite-" + string(d.mode)
}

// Placeholder: SQLite takes positional ?, so the index is implied.
func (d *driver) Placeholder(int) string { return "?" }

// Schema is the two tables. Both choices in it are about storage.
//
// node is INTEGER PRIMARY KEY, which in SQLite is the rowid itself rather
// than an index over it, so the table is the index and a point read is
// one B-tree descent with nothing to dereference.
//
// edge is WITHOUT ROWID with the pair as the primary key, so the table is
// the out-adjacency, clustered by src: the neighbours of a node are a
// contiguous range and the table stores no rowid nobody would use. That
// is the most compact honest shape for adjacency in SQLite, and it is
// what makes the comparison against a graph engine's adjacency list a
// comparison of two designs rather than of one design against a strawman.
func (d *driver) Schema() []string {
	return []string{
		`CREATE TABLE node (id INTEGER PRIMARY KEY)`,
		`CREATE TABLE edge (src INTEGER NOT NULL, dst INTEGER NOT NULL, PRIMARY KEY (src, dst)) WITHOUT ROWID`,
	}
}

// Indexes is the in-adjacency, built after the load because building an
// index over a finished table is one sort and maintaining it row by row
// is one B-tree insert per edge. On a WITHOUT ROWID table the index key
// is (dst, src) and the primary key it points back with is the same two
// columns, so this is a second copy of the edge list and nothing more.
func (d *driver) Indexes() []string {
	return []string{`CREATE INDEX edge_dst ON edge (dst, src)`}
}

func (d *driver) LoadNodes(ctx context.Context, db *sql.DB, files []string) (int64, error) {
	return sqlbase.InsertNodes(ctx, db, d, files)
}

func (d *driver) LoadEdges(ctx context.Context, db *sql.DB, files []string) (int64, error) {
	return sqlbase.InsertEdges(ctx, db, d, files)
}

// LoadMethod: SQLite has no bulk loader inside the library. The CLI's
// .import is a prepared INSERT in a transaction, which is what this is.
func (d *driver) LoadMethod() string { return "insert-tx" }

func (d *driver) Analyze(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, "ANALYZE")
	return err
}

func (d *driver) Version(ctx context.Context, db *sql.DB) (string, error) {
	var v string
	if err := db.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&v); err != nil {
		return "", err
	}
	return v, nil
}

// DiskBytes is the database file plus its write-ahead log: the bytes that
// have to survive a restart. The -shm file is left out because it is
// shared memory that happens to have a name, it is 32 KiB whatever the
// database holds, and it is gone the moment the last connection closes.
// Counting it would put a fixed 32 KiB on top of every SQLite footprint
// and call it storage.
//
// A checkpoint runs first so the pages sitting in the log are counted
// where they end up rather than twice. The memory mode has no files and
// reports the database's own page accounting instead, which is the same
// measurement of the same pages with nowhere to put them.
func (d *driver) DiskBytes(ctx context.Context, db *sql.DB) (int64, error) {
	if d.mode == Memory {
		var pages, size int64
		if err := db.QueryRowContext(ctx, "PRAGMA page_count").Scan(&pages); err != nil {
			return 0, err
		}
		if err := db.QueryRowContext(ctx, "PRAGMA page_size").Scan(&size); err != nil {
			return 0, err
		}
		return pages * size, nil
	}
	if _, err := db.ExecContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)"); err != nil &&
		!strings.Contains(err.Error(), "not in WAL mode") {
		return 0, err
	}
	var total int64
	for _, suffix := range []string{"", "-wal"} {
		fi, err := os.Stat(d.path + suffix)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return 0, err
		}
		total += fi.Size()
	}
	return total, nil
}
