// Package gr binds the gr graph database to the target SPI on the in-process
// plane: it opens a gr database in the harness process, loads a dataset through
// gr's library, and runs queries by calling gr directly with no serialization
// and no network. It is the reference adapter and the only one in the default,
// pure-Go build. The gr-over-Bolt adapter is a separate plane and lands with the
// Bolt milestone.
//
// See notes/Spec/2060/bench/03-target-spi.md section 3.1 for the contract this
// adapter implements.
package gr

import (
	"context"
	"fmt"
	"runtime/debug"
	"time"

	grdb "github.com/tamnd/gr"
	"github.com/tamnd/gr/vfs"
	"github.com/tamnd/graph-bench/target"
)

// modulePath is the gr module path used to read the running version from the
// build info, so the stamp records what actually ran rather than a constant.
const modulePath = "github.com/tamnd/gr"

// Target is the gr in-process target. One Target is one gr database on the
// in-process plane.
type Target struct{}

// New returns a gr in-process target.
func New() *Target { return &Target{} }

// compile-time check that the adapter satisfies the SPI.
var _ target.Target = (*Target)(nil)

// Name is the stable identifier for gr in-process in reports and the lineage.
func (t *Target) Name() string { return "gr" }

// Plane reports that this target runs in process.
func (t *Target) Plane() target.Plane { return target.InProc }

// Version reads the gr module version from the build info. Under a local replace
// the version is not stamped, so it reports "devel"; a released gr reports its
// tag.
func (t *Target) Version(ctx context.Context) (string, error) {
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range bi.Deps {
			if dep.Path != modulePath {
				continue
			}
			if dep.Replace != nil && dep.Replace.Version != "" {
				return dep.Replace.Version, nil
			}
			if dep.Version != "" && dep.Version != "(devel)" {
				return dep.Version, nil
			}
			return "devel", nil
		}
	}
	return "devel", nil
}

// Capabilities reports what the gr adapter exposes. BulkCSVLoad is true: a
// file-backed dataset is ingested through gr's four-pass loader, the same bulk
// path gr import uses. A statements dataset (no directory) is still loaded by
// issuing queries.
func (t *Target) Capabilities() target.Capabilities {
	return target.Capabilities{
		Languages:      []target.Language{target.Cypher},
		Transactions:   true,
		BulkCSVLoad:    true,
		Algorithms:     nil,
		PersistentDisk: true,
	}
}

// Setup opens a fresh gr database and returns a Driver bound to it. The path
// comes from Config.Values["path"]; an empty path or ":memory:" opens a
// transient in-memory database, which is what the adapter test uses.
func (t *Target) Setup(ctx context.Context, config target.Config) (target.Driver, error) {
	path := config.Values["path"]
	var (
		db  *grdb.DB
		err error
	)
	mem := path == "" || path == ":memory:"
	if mem {
		db, err = grdb.Open(":memory:.gr", grdb.Options{VFS: vfs.NewMem()})
	} else {
		db, err = grdb.Open(path, grdb.Options{})
	}
	if err != nil {
		return nil, fmt.Errorf("gr: open %q: %w", path, err)
	}
	return &driver{db: db, path: path, mem: mem}, nil
}

// Load ingests the dataset. A file-backed dataset (one with a directory of
// canonical CSV files) goes through gr's four-pass bulk loader; a statements
// dataset is built by running its statements in order. The bulk path is the
// identity case the canonical layout was shaped for, so it does no translation.
func (t *Target) Load(ctx context.Context, d target.Driver, ds target.Dataset) (target.LoadStats, error) {
	drv, ok := d.(*driver)
	if !ok {
		return target.LoadStats{}, fmt.Errorf("gr: Load got a %T, want *driver", d)
	}
	if ds.Dir() != "" {
		return t.loadCSV(ctx, drv, ds)
	}
	start := time.Now()
	for i, stmt := range ds.Statements() {
		res, err := drv.db.Run(ctx, stmt, nil)
		if err != nil {
			return target.LoadStats{}, fmt.Errorf("gr: load statement %d: %w", i, err)
		}
		for res.Next() {
		}
		err = res.Err()
		_ = res.Close()
		if err != nil {
			return target.LoadStats{}, fmt.Errorf("gr: load statement %d: %w", i, err)
		}
	}
	if err := createIDIndexes(ctx, drv.db, ds); err != nil {
		return target.LoadStats{}, err
	}
	stats := target.LoadStats{Duration: time.Since(start)}
	if info, err := drv.db.Info(); err == nil {
		stats.BytesOnDisk = info.SizeBytes
		stats.Nodes = int64(info.Nodes)
		stats.Edges = int64(info.Relationships)
	}
	return stats, nil
}

// Teardown closes the Driver. It is safe on a partially constructed Driver and
// is always called.
func (t *Target) Teardown(ctx context.Context, d target.Driver) error {
	if d == nil {
		return nil
	}
	return d.Close(ctx)
}

// driver is a live handle to an open gr database. path and mem record how it was
// opened, so the bulk CSV loader (which builds a fresh database file) knows
// whether it can run and where to write.
type driver struct {
	db   *grdb.DB
	path string
	mem  bool
}

var _ target.Driver = (*driver)(nil)

// Run executes one query through gr's library and wraps the result.
func (d *driver) Run(ctx context.Context, q target.Query, params target.Params) (target.Result, error) {
	res, err := d.db.Run(ctx, q.Text, map[string]any(params))
	if err != nil {
		return nil, err
	}
	return &result{r: res}, nil
}

// Begin opens a gr session and starts a transaction in it. The session is closed
// when the transaction commits or rolls back.
func (d *driver) Begin(ctx context.Context, mode target.AccessMode) (target.Tx, error) {
	gmode := grdb.Read
	if mode == target.WriteMode {
		gmode = grdb.Write
	}
	sess := d.db.Session()
	gtx, err := sess.Begin(ctx, gmode)
	if err != nil {
		_ = sess.Close()
		return nil, err
	}
	return &tx{sess: sess, gtx: gtx}, nil
}

// Close closes the underlying gr database.
func (d *driver) Close(ctx context.Context) error {
	if d == nil || d.db == nil {
		return nil
	}
	return d.db.Close()
}

// tx wraps a gr transaction and the session that owns it.
type tx struct {
	sess *grdb.Session
	gtx  *grdb.Tx
}

var _ target.Tx = (*tx)(nil)

func (t *tx) Run(ctx context.Context, q target.Query, params target.Params) (target.Result, error) {
	res, err := t.gtx.Run(ctx, q.Text, map[string]any(params))
	if err != nil {
		return nil, err
	}
	return &result{r: res}, nil
}

func (t *tx) Commit(ctx context.Context) error {
	err := t.gtx.Commit()
	_ = t.sess.Close()
	return err
}

func (t *tx) Rollback(ctx context.Context) error {
	err := t.gtx.Rollback()
	_ = t.sess.Close()
	return err
}

// result wraps a *gr.Result as a target.Result, mapping gr's values into the
// canonical model on the way out. gr already exposes Go-native scalars, so the
// mapping is a near identity; only the graph objects are translated.
type result struct {
	r *grdb.Result
}

var _ target.Result = (*result)(nil)

func (x *result) Columns() []string { return x.r.Columns() }
func (x *result) Next() bool        { return x.r.Next() }
func (x *result) Err() error        { return x.r.Err() }
func (x *result) Close() error      { return x.r.Close() }

func (x *result) Row() []target.Value {
	vals := x.r.Record().Values()
	out := make([]target.Value, len(vals))
	for i, v := range vals {
		out[i] = convertValue(v)
	}
	return out
}

// convertValue maps a gr value into the canonical value model. Scalars pass
// through; gr's graph objects become the target graph objects; lists and maps
// are converted recursively so a nested node is mapped too.
func convertValue(v any) target.Value {
	switch t := v.(type) {
	case grdb.Node:
		return convertNode(t)
	case grdb.Relationship:
		return convertRel(t)
	case grdb.Path:
		return convertPath(t)
	case []any:
		out := make([]target.Value, len(t))
		for i, e := range t {
			out[i] = convertValue(e)
		}
		return out
	case map[string]any:
		return convertMap(t)
	default:
		return v
	}
}

func convertMap(m map[string]any) map[string]target.Value {
	if m == nil {
		return nil
	}
	out := make(map[string]target.Value, len(m))
	for k, v := range m {
		out[k] = convertValue(v)
	}
	return out
}

func convertNode(n grdb.Node) target.Node {
	return target.Node{
		ID:     n.ElementId(),
		Labels: n.Labels(),
		Props:  convertMap(n.Props()),
	}
}

func convertRel(r grdb.Relationship) target.Relationship {
	return target.Relationship{
		ID:      r.ElementId(),
		Type:    r.Type(),
		StartID: r.StartElementId(),
		EndID:   r.EndElementId(),
		Props:   convertMap(r.Props()),
	}
}

func convertPath(p grdb.Path) target.Path {
	nodes := make([]target.Node, len(p.Nodes()))
	for i, n := range p.Nodes() {
		nodes[i] = convertNode(n)
	}
	rels := make([]target.Relationship, len(p.Relationships()))
	for i, r := range p.Relationships() {
		rels[i] = convertRel(r)
	}
	return target.Path{Nodes: nodes, Rels: rels}
}
