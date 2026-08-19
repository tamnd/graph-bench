package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/engine/sqlbase"
)

// Start connects to the server. The DSN is Config["dsn"], else
// $GRAPH_BENCH_PG_DSN, else $DATABASE_URL; the run verb puts a managed
// container's URL in the config when none of those is set.
//
// The first connection is retried for half a minute rather than tried
// once. A PostgreSQL container accepts TCP some way before it will answer
// a query, because initdb runs a server on a unix socket and restarts it,
// so a single Ping against a fresh container is a coin flip.
//
// Config keys: "dsn".
func (e *Engine) Start(ctx context.Context, cfg engine.Config) (engine.Session, error) {
	dsn := cfg.Get("dsn", "")
	for _, key := range []string{"GRAPH_BENCH_PG_DSN", "DATABASE_URL"} {
		if dsn != "" {
			break
		}
		dsn = os.Getenv(key)
	}
	if dsn == "" {
		return nil, errors.New("postgres: no server configured; set GRAPH_BENCH_PG_DSN or DATABASE_URL, " +
			"or let the run verb start a managed container (it needs Docker and no --no-docker)")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("postgres: dsn: %w", err)
	}
	db := sql.OpenDB(stdlib.GetConnector(*config))
	if err := ping(ctx, db, 30*time.Second); err != nil {
		db.Close()
		return nil, fmt.Errorf("postgres: connect: %w", err)
	}
	return sqlbase.Open(&driver{}, db, nil), nil
}

func ping(ctx context.Context, db *sql.DB, within time.Duration) error {
	deadline := time.Now().Add(within)
	var err error
	for {
		if err = db.PingContext(ctx); err == nil {
			return nil
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			return err
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// driver is the PostgreSQL half of the shared relational session.
type driver struct{}

var _ sqlbase.Driver = (*driver)(nil)

func (d *driver) Name() string { return "postgres" }

// Placeholder: PostgreSQL numbers its parameters from 1.
func (d *driver) Placeholder(i int) string { return "$" + strconv.Itoa(i) }

// Schema is the two tables with no key on either, and the drops in front
// of them because a server is reused across sessions where an embedded
// file is not: a session that inherited the last one's rows would report
// a load that never happened.
//
// The keys are missing on purpose. In PostgreSQL a primary key is a
// separate B-tree over a heap, so declaring it here would not change how
// a row is stored and would only mean one index insert per row through
// the whole load. It goes on afterwards with the rest, which is what the
// PostgreSQL documentation says to do and what the DuckDB adapter does
// for the same reason.
func (d *driver) Schema() []string {
	return []string{
		`DROP TABLE IF EXISTS edge`,
		`DROP TABLE IF EXISTS node`,
		`CREATE TABLE node (id BIGINT NOT NULL)`,
		`CREATE TABLE edge (src BIGINT NOT NULL, dst BIGINT NOT NULL)`,
	}
}

// Indexes are built after the load, and they are the whole storage story
// for this engine.
//
// The heap holds every row once. The primary key on (src, dst) is the
// out-adjacency, sorted by src, so a neighbour list is one range scan and
// the index covers the query: PostgreSQL can answer a one-hop count from
// the index alone without touching the heap. edge_dst is the same two
// columns the other way round, the in-adjacency an undirected walk needs,
// and it is a second full copy of the edge list. So an edge is stored
// three times here, once in the heap and once in each index, and that is
// what a relational store costs for adjacency that is fast in both
// directions.
func (d *driver) Indexes() []string {
	return []string{
		`ALTER TABLE node ADD PRIMARY KEY (id)`,
		`ALTER TABLE edge ADD PRIMARY KEY (src, dst)`,
		`CREATE INDEX edge_dst ON edge (dst, src)`,
	}
}

func (d *driver) LoadNodes(ctx context.Context, db *sql.DB, files []string) (int64, error) {
	return copyIn(ctx, db, "node", []string{"id"}, func(ctx context.Context, emit func([]any) error) error {
		return sqlbase.Nodes(ctx, files, func(id int64) error {
			return emit([]any{id})
		})
	})
}

func (d *driver) LoadEdges(ctx context.Context, db *sql.DB, files []string) (int64, error) {
	return copyIn(ctx, db, "edge", []string{"src", "dst"}, func(ctx context.Context, emit func([]any) error) error {
		return sqlbase.Edges(ctx, files, func(src, dst int64) error {
			return emit([]any{src, dst})
		})
	})
}

// copyIn streams rows into a table with COPY FROM STDIN in the binary
// format, which is what pgx's CopyFrom speaks. It is PostgreSQL's bulk
// loader and the only honest way to load it: an INSERT per row over a
// network would report the round trip as the load time.
//
// produce runs on its own goroutine so the CSV reader and the copy overlap,
// and its error is read back at the end rather than dropped, because a
// copy that stopped early looks exactly like a copy that finished. The
// child context is what unblocks the reader when the copy fails: without
// it a producer parked on a send into a channel nobody drains would sit
// there for the life of the process.
func copyIn(ctx context.Context, db *sql.DB, table string, cols []string, produce func(context.Context, func([]any) error) error) (int64, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	rows := make(chan []any, 4096)
	produced := make(chan error, 1)
	go func() {
		defer close(rows)
		produced <- produce(ctx, func(row []any) error {
			select {
			case rows <- row:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}()

	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Close()
	var n int64
	err = conn.Raw(func(dc any) error {
		pgConn, ok := dc.(*stdlib.Conn)
		if !ok {
			return fmt.Errorf("expected a pgx connection, got %T", dc)
		}
		count, err := pgConn.Conn().CopyFrom(ctx, pgx.Identifier{table}, cols, &chanSource{rows: rows})
		n = count
		return err
	})
	if err != nil {
		cancel()
		<-produced
		return 0, err
	}
	if err := <-produced; err != nil {
		return 0, err
	}
	return n, nil
}

// chanSource adapts a channel of rows to pgx.CopyFromSource.
type chanSource struct {
	rows <-chan []any
	cur  []any
}

var _ pgx.CopyFromSource = (*chanSource)(nil)

func (s *chanSource) Next() bool {
	row, ok := <-s.rows
	s.cur = row
	return ok
}

func (s *chanSource) Values() ([]any, error) { return s.cur, nil }
func (s *chanSource) Err() error             { return nil }

// LoadMethod: COPY FROM STDIN, PostgreSQL's bulk loader.
func (d *driver) LoadMethod() string { return "copy" }

// Analyze is VACUUM ANALYZE rather than plain ANALYZE: the copy leaves
// the visibility map unset, and until it is set an index-only scan is not
// index-only, so the first queries would pay a heap fetch the engine
// would never pay in production.
func (d *driver) Analyze(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, "VACUUM ANALYZE node, edge")
	return err
}

func (d *driver) Version(ctx context.Context, db *sql.DB) (string, error) {
	var v string
	if err := db.QueryRowContext(ctx, "SHOW server_version").Scan(&v); err != nil {
		return "", err
	}
	return v, nil
}

// DiskBytes is what the two tables occupy with everything attached:
// pg_total_relation_size counts the heap, the indexes, the visibility and
// free space maps and any TOAST, which is the whole of what this database
// wrote. It does not count the write-ahead log, and it should not: the
// WAL is shared by every database on the server, it is recycled at a size
// the server chose rather than one the data implies, and attributing it
// to one load would be attributing a server setting to a dataset.
func (d *driver) DiskBytes(ctx context.Context, db *sql.DB) (int64, error) {
	var total int64
	err := db.QueryRowContext(ctx,
		`SELECT pg_total_relation_size('node') + pg_total_relation_size('edge')`).Scan(&total)
	if err != nil {
		return 0, err
	}
	return total, nil
}
