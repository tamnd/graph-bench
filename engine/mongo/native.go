package mongo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	driver "go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/engine/sqlbase"
)

const (
	dbName    = "bench"
	nodeColl  = "node"
	edgeColl  = "edge"
	batchSize = 10000
)

// Start connects to the server. The URI is Config["uri"], else
// $GRAPH_BENCH_MONGO_URI or $MONGODB_URI; the run verb puts a managed
// container's URL in the config when none of those is set.
//
// Config keys: "uri".
func (e *Engine) Start(ctx context.Context, cfg engine.Config) (engine.Session, error) {
	uri := cfg.Get("uri", "")
	for _, key := range []string{"GRAPH_BENCH_MONGO_URI", "MONGODB_URI"} {
		if uri != "" {
			break
		}
		uri = os.Getenv(key)
	}
	if uri == "" {
		return nil, errors.New("mongodb: no server configured; set GRAPH_BENCH_MONGO_URI or MONGODB_URI, " +
			"or let the run verb start a managed container (it needs Docker and no --no-docker)")
	}
	client, err := driver.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return nil, fmt.Errorf("mongodb: connect: %w", err)
	}
	if err := ping(ctx, client, 30*time.Second); err != nil {
		_ = client.Disconnect(context.WithoutCancel(ctx))
		return nil, fmt.Errorf("mongodb: connect: %w", err)
	}
	return &Session{client: client, db: client.Database(dbName)}, nil
}

// ping waits for the server to answer. Connect itself does not: the
// driver resolves a topology in the background and hands back a client
// that fails on first use, which would report a container that is still
// starting as an engine that is broken.
func ping(ctx context.Context, c *driver.Client, within time.Duration) error {
	deadline := time.Now().Add(within)
	var err error
	for {
		if err = c.Ping(ctx, nil); err == nil {
			return nil
		}
		if ctx.Err() != nil || time.Now().After(deadline) {
			return err
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// Session is a live MongoDB connection.
type Session struct {
	client *driver.Client
	db     *driver.Database
	closed bool
}

var _ engine.Session = (*Session)(nil)

// Version reports the server version from buildInfo.
func (s *Session) Version(ctx context.Context) (string, error) {
	var out struct {
		Version string `bson:"version"`
	}
	err := s.db.RunCommand(ctx, bson.D{{Key: "buildInfo", Value: 1}}).Decode(&out)
	if err != nil {
		return "", err
	}
	return out.Version, nil
}

// Load writes the dataset into the two collections and builds the edge
// indexes afterwards, which is the documented order: an index maintained
// through a bulk insert costs a write per document per index, and one
// built over a finished collection is a single pass.
//
// The insert is unordered, in batches, with write concern left at the
// server default. Unordered is not a durability choice, it is what lets
// the server apply a batch without serializing on the first failure, and
// there are no failures to serialize on here.
func (s *Session) Load(ctx context.Context, ds engine.Dataset) (engine.LoadStats, error) {
	start := time.Now()
	nodeFiles, relFiles, err := sqlbase.SingleTable("mongodb", ds)
	if err != nil {
		return engine.LoadStats{}, err
	}
	// A server outlives a session where an embedded file does not, so the
	// collections go before the load: a session that inherited the last
	// one's documents would report a load that never happened.
	for _, name := range []string{nodeColl, edgeColl} {
		if err := s.db.Collection(name).Drop(ctx); err != nil {
			return engine.LoadStats{}, fmt.Errorf("mongodb: drop %s: %w", name, err)
		}
	}

	nodes, err := insertAll(ctx, s.db.Collection(nodeColl), func(emit func(bson.D) error) error {
		return sqlbase.Nodes(ctx, nodeFiles, func(id int64) error {
			return emit(bson.D{{Key: "_id", Value: id}})
		})
	})
	if err != nil {
		return engine.LoadStats{}, fmt.Errorf("mongodb: load nodes: %w", err)
	}
	edges, err := insertAll(ctx, s.db.Collection(edgeColl), func(emit func(bson.D) error) error {
		return sqlbase.Edges(ctx, relFiles, func(src, dst int64) error {
			return emit(bson.D{{Key: "src", Value: src}, {Key: "dst", Value: dst}})
		})
	})
	if err != nil {
		return engine.LoadStats{}, fmt.Errorf("mongodb: load edges: %w", err)
	}

	// The out-adjacency and the in-adjacency. Both are compound and both
	// cover their query, so a neighbour count is answered from the index
	// without fetching a document. The second one is a full second copy of
	// the edge collection, which is the same price the relational engines
	// pay for the same ability.
	_, err = s.db.Collection(edgeColl).Indexes().CreateMany(ctx, []driver.IndexModel{
		{Keys: bson.D{{Key: "src", Value: 1}, {Key: "dst", Value: 1}}},
		{Keys: bson.D{{Key: "dst", Value: 1}, {Key: "src", Value: 1}}},
	})
	if err != nil {
		return engine.LoadStats{}, fmt.Errorf("mongodb: edge indexes: %w", err)
	}

	elapsed := time.Since(start)
	bytes, err := s.diskBytes(ctx)
	if err != nil {
		return engine.LoadStats{}, fmt.Errorf("mongodb: disk bytes: %w", err)
	}
	return engine.LoadStats{
		Duration:    elapsed,
		Nodes:       nodes,
		Edges:       edges,
		BytesOnDisk: bytes,
		Method:      "insert-many",
	}, nil
}

// insertAll drains produce into batched InsertMany calls and reports how
// many documents it wrote.
func insertAll(ctx context.Context, coll *driver.Collection, produce func(emit func(bson.D) error) error) (int64, error) {
	opts := options.InsertMany().SetOrdered(false)
	batch := make([]any, 0, batchSize)
	var n int64
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		res, err := coll.InsertMany(ctx, batch, opts)
		if err != nil {
			return err
		}
		n += int64(len(res.InsertedIDs))
		batch = batch[:0]
		return nil
	}
	err := produce(func(doc bson.D) error {
		batch = append(batch, doc)
		if len(batch) < batchSize {
			return nil
		}
		return flush()
	})
	if err != nil {
		return 0, err
	}
	if err := flush(); err != nil {
		return 0, err
	}
	return n, nil
}

// diskBytes is what the two collections occupy: the compressed documents
// on disk plus every index. dbStats reports storageSize and indexSize for
// the whole database, and the database holds nothing but these two
// collections, so the sum is this dataset's footprint. It is the
// compressed figure, since WiredTiger compresses by default and what the
// engine actually wrote is what the row is for.
//
// The fsync in front of it is not optional. WiredTiger sizes a file from
// its last checkpoint, and a load that just finished has most of itself
// in a dirty cache, so reading dbStats straight away reports a database
// smaller than the data in it. Checkpointing first is the same thing the
// DuckDB adapter does with CHECKPOINT and for the same reason.
func (s *Session) diskBytes(ctx context.Context) (int64, error) {
	admin := s.client.Database("admin")
	if err := admin.RunCommand(ctx, bson.D{{Key: "fsync", Value: 1}}).Err(); err != nil {
		return 0, fmt.Errorf("fsync: %w", err)
	}
	var out struct {
		StorageSize float64 `bson:"storageSize"`
		IndexSize   float64 `bson:"indexSize"`
	}
	err := s.db.RunCommand(ctx, bson.D{{Key: "dbStats", Value: 1}}).Decode(&out)
	if err != nil {
		return 0, err
	}
	return int64(out.StorageSize + out.IndexSize), nil
}

// query is the parsed form of a Mongo dialect text.
type query struct {
	Collection string          `json:"collection"`
	Columns    []string        `json:"columns"`
	Pipeline   json.RawMessage `json:"pipeline"`
}

// Exec runs one aggregation. The text is parsed on every call rather than
// cached, which is a JSON decode of a few hundred bytes against a network
// round trip; if that ever shows up in a profile it is a map away, but
// paying it keeps Exec free of shared state.
func (s *Session) Exec(ctx context.Context, op engine.Op) (engine.Result, error) {
	if op.Dialect != engine.Mongo {
		return nil, fmt.Errorf("mongodb: %s: dialect %q is not mongo", op.QueryID, op.Dialect)
	}
	var q query
	if err := json.Unmarshal([]byte(op.Text), &q); err != nil {
		return nil, fmt.Errorf("mongodb: %s: text is not a mongo query object: %w", op.QueryID, err)
	}
	if q.Collection == "" || len(q.Columns) == 0 || len(q.Pipeline) == 0 {
		return nil, fmt.Errorf("mongodb: %s: text needs a collection, columns and a pipeline", op.QueryID)
	}
	var pipeline []bson.D
	if err := bson.UnmarshalExtJSON(q.Pipeline, false, &pipeline); err != nil {
		return nil, fmt.Errorf("mongodb: %s: pipeline: %w", op.QueryID, err)
	}
	opts := options.Aggregate()
	if len(op.Params) > 0 {
		let := bson.D{}
		for k, v := range op.Params {
			let = append(let, bson.E{Key: k, Value: v})
		}
		opts.SetLet(let)
	}
	cur, err := s.db.Collection(q.Collection).Aggregate(ctx, pipeline, opts)
	if err != nil {
		return nil, fmt.Errorf("mongodb: %s: %w", op.QueryID, err)
	}
	return &result{ctx: ctx, cur: cur, cols: q.Columns}, nil
}

// Begin refuses. MongoDB has transactions on a replica set or a sharded
// cluster and this adapter runs against a single mongod, so there is
// nothing honest to return.
func (s *Session) Begin(context.Context, engine.AccessMode) (engine.Tx, error) {
	return nil, engine.ErrNoTransactions
}

// Close disconnects. It is idempotent and safe on a session that never
// loaded.
func (s *Session) Close(ctx context.Context) error {
	if s.closed {
		return nil
	}
	s.closed = true
	return s.client.Disconnect(ctx)
}

// result streams a cursor's documents as rows in the declared column
// order.
type result struct {
	ctx  context.Context
	cur  *driver.Cursor
	cols []string
	row  []engine.Value
	err  error
}

var _ engine.Result = (*result)(nil)

func (r *result) Columns() []string { return r.cols }

func (r *result) Next() bool {
	if r.err != nil || !r.cur.Next(r.ctx) {
		return false
	}
	var doc bson.M
	if err := r.cur.Decode(&doc); err != nil {
		r.err = err
		return false
	}
	row := make([]engine.Value, len(r.cols))
	for i, col := range r.cols {
		v, ok := doc[col]
		if !ok {
			r.err = fmt.Errorf("mongodb: result has no column %q, got %v", col, keys(doc))
			return false
		}
		row[i] = value(v)
	}
	r.row = row
	return true
}

func (r *result) Row() []engine.Value { return r.row }

func (r *result) Err() error {
	if r.err != nil {
		return r.err
	}
	return r.cur.Err()
}

func (r *result) Close() error { return r.cur.Close(r.ctx) }

// value maps a decoded BSON value into the canonical model. The one
// conversion that matters is int32 to int64: MongoDB's $sum over a
// literal 1 answers with a 32-bit integer, and a count is a count
// whatever width the server chose to send it in.
func value(v any) engine.Value {
	switch t := v.(type) {
	case int32:
		return int64(t)
	case bson.A:
		out := make([]engine.Value, len(t))
		for i, e := range t {
			out[i] = value(e)
		}
		return out
	case bson.M:
		out := make(map[string]engine.Value, len(t))
		for k, e := range t {
			out[k] = value(e)
		}
		return out
	default:
		return v
	}
}

func keys(m bson.M) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
