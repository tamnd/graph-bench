//go:build bolt

// Package bolt is the shared Bolt plane: it connects to any engine that
// speaks the Bolt wire protocol and openCypher (Neo4j, Memgraph), runs a
// Cypher statement, and decodes the result stream into the canonical value
// model. Every Bolt adapter in engine/ uses this package rather than owning
// its own Bolt client, which keeps the same-work guarantee (F1, F8) from
// being silently broken by an adapter that decodes results differently
// (ADR-10: one Bolt code path).
//
// Build tag: bolt. The default no-tag build remains pure Go and does not
// link the neo4j-go-driver. Use -tags bolt to compile the Bolt plane.
//
// See notes/Spec/2064g/bench/04-adapters.md section 7 for the contract.
package bolt

import (
	"context"
	"fmt"

	"github.com/neo4j/neo4j-go-driver/v6/neo4j"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j/config"
	"github.com/neo4j/neo4j-go-driver/v6/neo4j/dbtype"

	"github.com/tamnd/graph-bench/engine"
)

// Pool is a connection pool to a single Bolt endpoint. It is created once
// per engine in Start and shared across all Session calls. Every Bolt
// adapter embeds a *Pool.
type Pool struct {
	driver neo4j.Driver
	db     string // target database name, e.g. "neo4j"; "" for engines without named databases
}

// WithPoolSize returns a driver configurer that bounds the connection pool.
// Non-positive sizes leave the driver default in place.
func WithPoolSize(n int) func(*config.Config) {
	return func(c *config.Config) {
		if n > 0 {
			c.MaxConnectionPoolSize = n
		}
	}
}

// Open dials uri and authenticates with user/pass. Empty user and pass
// connect unauthenticated (Memgraph community default). db is the default
// database name for sessions; empty means the driver's default. Open does
// not verify the server is ready; use Ping.
func Open(ctx context.Context, uri, user, pass, db string, configurers ...func(*config.Config)) (*Pool, error) {
	auth := neo4j.NoAuth()
	if user != "" || pass != "" {
		auth = neo4j.BasicAuth(user, pass, "")
	}
	d, err := neo4j.NewDriver(uri, auth, configurers...)
	if err != nil {
		return nil, fmt.Errorf("bolt: dial %s: %w", uri, err)
	}
	return &Pool{driver: d, db: db}, nil
}

// Ping verifies the server is reachable and the credentials work.
func (p *Pool) Ping(ctx context.Context) error {
	return p.driver.VerifyConnectivity(ctx)
}

// Close releases the underlying driver and all pooled connections. It is
// idempotent and safe on a partially failed Pool.
func (p *Pool) Close(ctx context.Context) error {
	if p == nil || p.driver == nil {
		return nil
	}
	d := p.driver
	p.driver = nil
	return d.Close(ctx)
}

// Version queries the server for its bolt-agent version string, live from
// the running engine (F7), never from a pin.
func (p *Pool) Version(ctx context.Context) (string, error) {
	info, err := p.driver.GetServerInfo(ctx)
	if err != nil {
		return "", fmt.Errorf("bolt: version: %w", err)
	}
	return info.Agent(), nil
}

// Run executes one operation and returns a streaming Result. Reads run in
// an autocommit session; writes run inside an explicit transaction that
// commits on Result.Close. The caller must call Close, even on error.
func (p *Pool) Run(ctx context.Context, op engine.Op) (engine.Result, error) {
	cfg := neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead}
	if op.Class == engine.Write {
		cfg.AccessMode = neo4j.AccessModeWrite
	}
	if p.db != "" {
		cfg.DatabaseName = p.db
	}
	sess := p.driver.NewSession(ctx, cfg)

	if cfg.AccessMode == neo4j.AccessModeWrite {
		tx, err := sess.BeginTransaction(ctx)
		if err != nil {
			_ = sess.Close(ctx)
			return nil, fmt.Errorf("bolt: begin write tx: %w", err)
		}
		res, err := tx.Run(ctx, op.Text, op.Params)
		if err != nil {
			_ = tx.Rollback(ctx)
			_ = sess.Close(ctx)
			return nil, fmt.Errorf("bolt: run write: %w", err)
		}
		return &streamResult{ctx: ctx, res: res, sess: sess, tx: tx, write: true}, nil
	}

	res, err := sess.Run(ctx, op.Text, op.Params)
	if err != nil {
		_ = sess.Close(ctx)
		return nil, fmt.Errorf("bolt: run: %w", err)
	}
	return &streamResult{ctx: ctx, res: res, sess: sess}, nil
}

// Begin opens an explicit transaction. The returned Tx implements
// engine.Tx; Commit/Rollback release the underlying session.
func (p *Pool) Begin(ctx context.Context, mode engine.AccessMode) (*Tx, error) {
	nm := neo4j.AccessModeRead
	if mode == engine.WriteMode {
		nm = neo4j.AccessModeWrite
	}
	cfg := neo4j.SessionConfig{AccessMode: nm}
	if p.db != "" {
		cfg.DatabaseName = p.db
	}
	sess := p.driver.NewSession(ctx, cfg)
	tx, err := sess.BeginTransaction(ctx)
	if err != nil {
		_ = sess.Close(ctx)
		return nil, fmt.Errorf("bolt: begin tx: %w", err)
	}
	return &Tx{tx: tx, sess: sess}, nil
}

// Tx is an explicit Bolt transaction implementing engine.Tx.
type Tx struct {
	tx   neo4j.ExplicitTransaction
	sess neo4j.Session
}

// Exec runs one operation inside the transaction. The returned result is
// owned by the transaction: Close does not commit.
func (t *Tx) Exec(ctx context.Context, op engine.Op) (engine.Result, error) {
	res, err := t.tx.Run(ctx, op.Text, op.Params)
	if err != nil {
		return nil, fmt.Errorf("bolt: run tx: %w", err)
	}
	return &streamResult{ctx: ctx, res: res, txOwned: true}, nil
}

// Commit commits the transaction and releases its session.
func (t *Tx) Commit(ctx context.Context) error {
	err := t.tx.Commit(ctx)
	_ = t.sess.Close(ctx)
	return err
}

// Rollback rolls the transaction back and releases its session.
func (t *Tx) Rollback(ctx context.Context) error {
	err := t.tx.Rollback(ctx)
	_ = t.sess.Close(ctx)
	return err
}

// streamResult implements engine.Result backed by a neo4j cursor, decoding
// rows lazily in Next.
type streamResult struct {
	ctx     context.Context
	res     neo4j.Result
	sess    neo4j.Session
	tx      neo4j.ExplicitTransaction
	write   bool // autocommit-style write: commit tx on Close
	txOwned bool // result belongs to a caller-owned Tx; Close is a no-op
	closed  bool

	cols []string
	row  []engine.Value
	err  error
}

func (r *streamResult) Columns() []string {
	if r.cols == nil {
		keys, _ := r.res.Keys()
		r.cols = keys
	}
	return r.cols
}

func (r *streamResult) Next() bool {
	if !r.res.Next(r.ctx) {
		if err := r.res.Err(); err != nil {
			r.err = err
		}
		return false
	}
	vals := r.res.Record().Values
	out := make([]engine.Value, len(vals))
	for i, v := range vals {
		out[i] = decodeValue(v)
	}
	r.row = out
	return true
}

func (r *streamResult) Row() []engine.Value { return r.row }

func (r *streamResult) Err() error { return r.err }

func (r *streamResult) Close() error {
	if r.txOwned || r.closed {
		return nil // caller owns the transaction/session, or already closed
	}
	r.closed = true
	var firstErr error
	if r.write && r.tx != nil {
		if err := r.tx.Commit(r.ctx); err != nil {
			firstErr = err
		}
	}
	if r.sess != nil {
		if err := r.sess.Close(r.ctx); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// decodeValue maps a neo4j driver value to the canonical model: nil, bool,
// int64, float64, string, time.Time, []Value, map[string]Value, engine.Node,
// engine.Rel, engine.Path. Temporal dbtypes become time.Time, integer widths
// normalize to int64, and the v6 vector type becomes []float64.
func decodeValue(v any) engine.Value {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case bool, int64, float64, string, []byte:
		return t
	case int:
		return int64(t)
	case int32:
		return int64(t)
	case int16:
		return int64(t)
	case int8:
		return int64(t)
	case float32:
		return float64(t)
	case []any:
		out := make([]engine.Value, len(t))
		for i, elem := range t {
			out[i] = decodeValue(elem)
		}
		return out
	case map[string]any:
		out := make(map[string]engine.Value, len(t))
		for k, val := range t {
			out[k] = decodeValue(val)
		}
		return out
	case dbtype.Node:
		return decodeNode(t)
	case dbtype.Relationship:
		return decodeRel(t)
	case dbtype.Path:
		p := engine.Path{
			Nodes: make([]engine.Node, len(t.Nodes)),
			Rels:  make([]engine.Rel, len(t.Relationships)),
		}
		for i, n := range t.Nodes {
			p.Nodes[i] = decodeNode(n)
		}
		for i, rel := range t.Relationships {
			p.Rels[i] = decodeRel(rel)
		}
		return p
	case dbtype.Date:
		return t.Time()
	case dbtype.LocalDateTime:
		return t.Time()
	case dbtype.LocalTime:
		return t.Time()
	case dbtype.Time:
		return t.Time()
	case dbtype.Vector[float64]:
		return append([]float64(nil), t.Elems...)
	case dbtype.Vector[float32]:
		return vectorToFloat64(t.Elems)
	case dbtype.Vector[int8]:
		return vectorToFloat64(t.Elems)
	case dbtype.Vector[int16]:
		return vectorToFloat64(t.Elems)
	case dbtype.Vector[int32]:
		return vectorToFloat64(t.Elems)
	case dbtype.Vector[int64]:
		return vectorToFloat64(t.Elems)
	default:
		// time.Time (Cypher DATETIME) and unknown types pass through;
		// answer validation will catch a mismatch.
		return v
	}
}

func decodeNode(t dbtype.Node) engine.Node {
	n := engine.Node{
		ID:     t.ElementId,
		Labels: t.Labels,
		Props:  make(map[string]engine.Value, len(t.Props)),
	}
	for k, val := range t.Props {
		n.Props[k] = decodeValue(val)
	}
	return n
}

func decodeRel(t dbtype.Relationship) engine.Rel {
	r := engine.Rel{
		ID:    t.ElementId,
		Type:  t.Type,
		Start: t.StartElementId,
		End:   t.EndElementId,
		Props: make(map[string]engine.Value, len(t.Props)),
	}
	for k, val := range t.Props {
		r.Props[k] = decodeValue(val)
	}
	return r
}

func vectorToFloat64[T float32 | int8 | int16 | int32 | int64](elems []T) []float64 {
	out := make([]float64, len(elems))
	for i, e := range elems {
		out[i] = float64(e)
	}
	return out
}
