//go:build bolt

// Package bolt is the shared Bolt plane: it connects to any engine that speaks
// the Bolt wire protocol and openCypher (Neo4j, Memgraph, FalkorDB, gr-bolt),
// runs a Cypher query, and decodes the result stream into the harness canonical
// value model. Every Bolt adapter in adapter/ uses this package rather than
// owning its own Bolt client, which keeps the same-work guarantee (F1, F8) from
// being silently broken by an adapter that decodes results differently.
//
// Build tag: bolt. The default no-tag build remains pure Go and does not link
// the neo4j-go-driver. Use -tags bolt to compile the Bolt plane.
//
// See notes/Spec/2060/bench/03-target-spi.md section 3.2 for the contract.
package bolt

import (
	"context"
	"fmt"

	"github.com/neo4j/neo4j-go-driver/v5/neo4j"
	"github.com/neo4j/neo4j-go-driver/v5/neo4j/dbtype"

	"github.com/tamnd/graph-bench/target"
)

// Pool is a connection pool to a single Bolt endpoint. It is created once per
// engine in Setup and shared across all Driver calls. Every Bolt adapter embeds
// a *Pool.
type Pool struct {
	driver neo4j.DriverWithContext
	db     string // target database name, e.g. "neo4j", "memgraph", ""
}

// Open dials uri and authenticates with user/pass. db is the default database
// name to use for sessions; empty means the driver's default. Open does not
// return an error if the server is not yet ready; use Ping to check.
func Open(ctx context.Context, uri, user, pass, db string) (*Pool, error) {
	d, err := neo4j.NewDriverWithContext(uri, neo4j.BasicAuth(user, pass, ""))
	if err != nil {
		return nil, fmt.Errorf("bolt: dial %s: %w", uri, err)
	}
	return &Pool{driver: d, db: db}, nil
}

// Ping verifies the server is reachable and the credentials work.
func (p *Pool) Ping(ctx context.Context) error {
	return p.driver.VerifyConnectivity(ctx)
}

// Close releases the underlying driver and all pooled connections. Called by
// the adapter's Teardown.
func (p *Pool) Close(ctx context.Context) error {
	return p.driver.Close(ctx)
}

// Version queries the server for its bolt-agent version string.
func (p *Pool) Version(ctx context.Context) (string, error) {
	info, err := p.driver.GetServerInfo(ctx)
	if err != nil {
		return "", fmt.Errorf("bolt: version: %w", err)
	}
	return info.Agent(), nil
}

// Run executes a Cypher query against the pool and returns a streaming Result.
// The caller must call Result.Close when done, even on error.
func (p *Pool) Run(ctx context.Context, q target.Query, params target.Params) (target.Result, error) {
	cfg := &neo4j.SessionConfig{AccessMode: neo4j.AccessModeRead}
	if q.Class == target.Write {
		cfg.AccessMode = neo4j.AccessModeWrite
	}
	if p.db != "" {
		cfg.DatabaseName = p.db
	}
	sess := p.driver.NewSession(ctx, *cfg)

	// ExecuteRead/Write handles retries on transient errors. We choose the mode
	// based on the query class.
	if cfg.AccessMode == neo4j.AccessModeWrite {
		tx, err := sess.BeginTransaction(ctx)
		if err != nil {
			_ = sess.Close(ctx)
			return nil, fmt.Errorf("bolt: begin write tx: %w", err)
		}
		neo4jRes, err := tx.Run(ctx, q.Text, paramsToNeo4j(params))
		if err != nil {
			_ = tx.Rollback(ctx)
			_ = sess.Close(ctx)
			return nil, fmt.Errorf("bolt: run write: %w", err)
		}
		return &streamResult{neo4jRes: neo4jRes, sess: sess, tx: tx, write: true}, nil
	}

	neo4jRes, err := sess.Run(ctx, q.Text, paramsToNeo4j(params))
	if err != nil {
		_ = sess.Close(ctx)
		return nil, fmt.Errorf("bolt: run: %w", err)
	}
	return &streamResult{neo4jRes: neo4jRes, sess: sess}, nil
}

// RunTx executes a query inside an already-open explicit transaction.
func (p *Pool) RunTx(ctx context.Context, tx neo4j.ExplicitTransaction, q target.Query, params target.Params) (target.Result, error) {
	neo4jRes, err := tx.Run(ctx, q.Text, paramsToNeo4j(params))
	if err != nil {
		return nil, fmt.Errorf("bolt: run tx: %w", err)
	}
	return &streamResult{neo4jRes: neo4jRes, txOnly: true}, nil
}

// BeginTx opens an explicit transaction for Driver.Begin.
func (p *Pool) BeginTx(ctx context.Context, mode target.AccessMode) (neo4j.ExplicitTransaction, neo4j.SessionWithContext, error) {
	nm := neo4j.AccessModeRead
	if mode == target.WriteMode {
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
		return nil, nil, fmt.Errorf("bolt: begin tx: %w", err)
	}
	return tx, sess, nil
}

// streamResult implements target.Result backed by a neo4j cursor.
type streamResult struct {
	neo4jRes neo4j.ResultWithContext
	sess     neo4j.SessionWithContext
	tx       neo4j.ExplicitTransaction
	write    bool
	txOnly   bool // owned by the caller, do not close

	cols []string
	row  []target.Value
	err  error
}

func (r *streamResult) Columns() []string {
	if r.cols == nil {
		keys, _ := r.neo4jRes.Keys()
		r.cols = keys
	}
	return r.cols
}

func (r *streamResult) Next() bool {
	ok := r.neo4jRes.Next(context.Background())
	if !ok {
		r.err = r.neo4jRes.Err()
		return false
	}
	rec := r.neo4jRes.Record()
	vals := rec.Values
	out := make([]target.Value, len(vals))
	for i, v := range vals {
		out[i] = decodeValue(v)
	}
	r.row = out
	return true
}

func (r *streamResult) Row() []target.Value { return r.row }

func (r *streamResult) Err() error { return r.err }

func (r *streamResult) Close() error {
	if r.txOnly {
		return nil // caller owns session
	}
	var errs []error
	if r.write && r.tx != nil {
		if err := r.tx.Commit(context.Background()); err != nil {
			errs = append(errs, err)
		}
	}
	if r.sess != nil {
		if err := r.sess.Close(context.Background()); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// paramsToNeo4j converts target.Params (map[string]any) to the map the
// neo4j driver expects. The canonical value model is a subset of what the
// driver accepts, so the conversion is a shallow copy.
func paramsToNeo4j(p target.Params) map[string]any {
	if len(p) == 0 {
		return nil
	}
	m := make(map[string]any, len(p))
	for k, v := range p {
		m[k] = v
	}
	return m
}

// decodeValue maps a neo4j driver value to the canonical model.
// The canonical model is: nil, bool, int64, float64, string, []byte, []Value,
// map[string]Value, Node, Relationship, Path. The neo4j driver returns values
// as any; we type-switch to ensure the canonical types are used rather than
// whatever the driver chose.
func decodeValue(v any) target.Value {
	if v == nil {
		return nil
	}
	switch t := v.(type) {
	case bool:
		return t
	case int64:
		return t
	case int:
		return int64(t)
	case int32:
		return int64(t)
	case float64:
		return t
	case float32:
		return float64(t)
	case string:
		return t
	case []byte:
		return t
	case []any:
		out := make([]target.Value, len(t))
		for i, elem := range t {
			out[i] = decodeValue(elem)
		}
		return out
	case map[string]any:
		out := make(map[string]target.Value, len(t))
		for k, val := range t {
			out[k] = decodeValue(val)
		}
		return out
	case dbtype.Node:
		n := target.Node{
			ID:     t.GetElementId(),
			Labels: t.Labels,
			Props:  make(map[string]target.Value, len(t.Props)),
		}
		for k, val := range t.Props {
			n.Props[k] = decodeValue(val)
		}
		return n
	case dbtype.Relationship:
		r := target.Relationship{
			ID:      t.GetElementId(),
			Type:    t.Type,
			StartID: t.StartElementId,
			EndID:   t.EndElementId,
			Props:   make(map[string]target.Value, len(t.Props)),
		}
		for k, val := range t.Props {
			r.Props[k] = decodeValue(val)
		}
		return r
	case dbtype.Path:
		p := target.Path{
			Nodes: make([]target.Node, len(t.Nodes)),
			Rels:  make([]target.Relationship, len(t.Relationships)),
		}
		for i, n := range t.Nodes {
			decoded := decodeValue(n)
			if node, ok := decoded.(target.Node); ok {
				p.Nodes[i] = node
			}
		}
		for i, rel := range t.Relationships {
			decoded := decodeValue(rel)
			if r, ok := decoded.(target.Relationship); ok {
				p.Rels[i] = r
			}
		}
		return p
	default:
		// Unknown type: return as-is; validation will catch a mismatch.
		return v
	}
}
