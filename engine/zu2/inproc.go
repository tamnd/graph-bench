//go:build zu2inproc

// The live half of the zu2 adapter, over libzu2 (crates/zu2-capi). The
// database opens inside the harness process and every operation is a
// direct C call: no frame, no pipe, no child process in the timed
// region.
//
// Build tag: zu2inproc. The descriptor in zu2.go builds without the tag
// and its Start says what to build, so the engine is never silently
// missing.
//
// # Library location
//
// The #cgo lines default to a sibling zu checkout built in release mode
// (../../../zu relative to this file, which is the layout the repos are
// developed in). cgo cannot read environment variables, so anything else
// overrides the flags at build time, the same seam the zu and ladybug
// adapters document:
//
//	CGO_CFLAGS="-I$ZU2_INCLUDE" \
//	CGO_LDFLAGS="-L$ZU2_LIB -lzu2 -Wl,-rpath,$ZU2_LIB" \
//	go build -tags zu2inproc ./...
//
// Build libzu2 first: cargo build --release -p zu2-capi in the zu repo,
// which writes libzu2.dylib (macOS) or libzu2.so (Linux) into
// target/release.
//
// # When the database opens
//
// At Load, not at Start, and that is deliberate. zu2 sizes its hash
// index and its vertex table once, at open, and neither grows; past the
// load factor the index degrades into long probes. A host embedding zu2
// knows roughly how many records it is about to hold and says so, so
// this adapter does the same and takes the count off the dataset
// manifest. A session that is asked for a query before anything was
// loaded opens at the engine's own defaults instead.
//
// # Threading
//
// A libzu2 session may move between threads but must not be in two
// calls at once, and it answers ZU2_MISUSE_CONCURRENT rather than
// handing back a buffer another thread is filling. So a Session here is
// a pool of them: Exec checks one out, runs on it, and puts it back, and
// no two callers ever hold the same one. The pool grows to whatever
// concurrency the run asks for and never shrinks.
//
// That pool is also why every answer is copied out before Exec returns.
// The buffer a walk answers into belongs to the session and lives
// exactly until the next call on it, so a result that pointed into it
// would go stale the moment the session went back into the pool.
package zu2

// #cgo CFLAGS: -I${SRCDIR}/../../../zu/crates/zu2-capi/include
// #cgo LDFLAGS: -L${SRCDIR}/../../../zu/target/release -lzu2 -Wl,-rpath,${SRCDIR}/../../../zu/target/release
// #include <stdlib.h>
// #include "zu2.h"
import "C"

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/tamnd/graph-bench/engine"
)

// Start binds a session to a database path: Config["path"] if given,
// else "bench.zu2" in a fresh temp dir, removed on Close. Nothing opens
// yet; see the package comment on why the open waits for Load.
//
// Config keys: "path", and "durability" as async or durable. The
// durability default is the engine's own, which is durable, so a run
// that says nothing measures zu2 the way it ships.
func (e *Engine) Start(ctx context.Context, cfg engine.Config) (engine.Session, error) {
	workDir, err := os.MkdirTemp("", "graph-bench-zu2-")
	if err != nil {
		return nil, fmt.Errorf("zu2: create work dir: %w", err)
	}
	dbPath := cfg.Get("path", "")
	if dbPath == "" {
		dbPath = filepath.Join(workDir, "bench.zu2")
	}
	durability := C.ZU2_DURABLE
	switch cfg.Get("durability", "durable") {
	case "durable":
	case "async":
		durability = C.ZU2_ASYNC
	default:
		os.RemoveAll(workDir)
		return nil, fmt.Errorf("zu2: durability %q is not async or durable", cfg.Get("durability", ""))
	}
	return &Session{
		dbPath:     dbPath,
		workDir:    workDir,
		durability: C.zu2_durability(durability),
	}, nil
}

// Session is an open zu2 database plus a pool of libzu2 sessions on it.
type Session struct {
	dbPath     string
	workDir    string
	durability C.zu2_durability

	mu     sync.Mutex
	db     *C.zu2_db
	all    []*conn // every session opened, so Close can free them
	idle   []*conn // the ones not checked out
	closed bool

	// plans caches the parsed directive for a query text. Parsing is
	// microseconds and every engine here caches its prepared statements,
	// so not caching this would put a parse in the measured region that
	// no rival pays.
	plans sync.Map // string -> directive

	nodes, edges int64
}

// conn is one libzu2 session. Nothing here takes a lock: a conn is only
// ever touched by the caller holding it, and the pool is what guarantees
// there is only one.
type conn struct {
	s  *C.zu2_session
	db *C.zu2_db
}

var _ engine.Session = (*Session)(nil)

// Mode reports the exec surface, for the condition stamp. There is only
// one: direct calls into libzu2.
func (s *Session) Mode() string { return "capi" }

// Version reports the library version from zu2_version(), which is the
// version of the code actually linked in and not of anything on PATH.
func (s *Session) Version(ctx context.Context) (string, error) {
	var n C.size_t
	return C.GoString(C.zu2_version(&n)), nil
}

// Calibrate reports the per-operation process-spawn floor, which is zero
// here: nothing spawns per operation. The number is still stamped, so a
// result file says on its face that the measurement carries no
// transport.
func (s *Session) Calibrate(ctx context.Context) time.Duration { return 0 }

// Begin reports that there are no transactions. zu2 has no BEGIN and no
// COMMIT: a write is acknowledged when the session's durability setting
// says it is. Saying so is what makes the runner skip the transactional
// queries rather than measure something narrower under their name.
func (s *Session) Begin(ctx context.Context, mode engine.AccessMode) (engine.Tx, error) {
	return nil, engine.ErrNoTransactions
}

// Load reads the dataset's node and rel CSVs and builds the graph
// through add_vertex and add_edge, which is the only load path the
// engine has. Vertices come first so an edge is two lookups in a map
// that is already complete, and the map is dropped when the load is
// done: at query time a seed is resolved through the engine's own index,
// which is the probe the rival engines are also paying.
//
// One node label only. Vertices are keyed by the id column and two
// labels can carry the same id, so a dataset with two node tables would
// quietly merge two vertices into one. A dataset with more than one
// relationship type is refused for the matching reason: there is one
// adjacency here and no edge types in it, so a query that asks about one
// type would be answered from all of them.
func (s *Session) Load(ctx context.Context, ds engine.Dataset) (engine.LoadStats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return engine.LoadStats{}, errors.New("zu2: session is closed")
	}
	if ds.Dir() == "" {
		return engine.LoadStats{}, errors.New("zu2: this engine has no statement surface, so a statements-only dataset has no load path here")
	}
	schema := ds.Schema()
	if len(schema.Nodes) != 1 {
		return engine.LoadStats{}, fmt.Errorf("zu2: a vertex is keyed by its id alone, so this adapter takes one node label and the dataset has %d", len(schema.Nodes))
	}
	if len(schema.Rels) != 1 {
		return engine.LoadStats{}, fmt.Errorf("zu2: there is one adjacency here and no edge types in it, so this adapter takes one relationship type and the dataset has %d", len(schema.Rels))
	}

	start := time.Now()
	if err := s.openLocked(sizeOf(ds)); err != nil {
		return engine.LoadStats{}, err
	}
	c, err := s.acquireLocked()
	if err != nil {
		return engine.LoadStats{}, err
	}
	defer s.releaseLocked(c)

	ids := make(map[string]C.uint32_t)
	for label := range schema.Nodes {
		files, err := ds.NodeFiles(label)
		if err != nil {
			return engine.LoadStats{}, fmt.Errorf("zu2: node files for %q: %w", label, err)
		}
		for _, path := range files {
			if err := c.loadNodes(ctx, path, ids); err != nil {
				return engine.LoadStats{}, err
			}
		}
	}
	var edges int64
	for typ := range schema.Rels {
		files, err := ds.RelFiles(typ)
		if err != nil {
			return engine.LoadStats{}, fmt.Errorf("zu2: rel files for %q: %w", typ, err)
		}
		for _, path := range files {
			n, err := c.loadRels(ctx, path, ids)
			if err != nil {
				return engine.LoadStats{}, err
			}
			edges += n
		}
	}
	// The tail goes onto the device before anything measures the file,
	// whatever durability the load ran at.
	if st := C.zu2_sync(s.db); st != C.ZU2_OK {
		return engine.LoadStats{}, dbErr(s.db, st, "sync after load")
	}
	s.nodes, s.edges = int64(len(ids)), edges

	var bytes C.uint64_t
	onDisk := int64(-1)
	if st := C.zu2_disk_bytes(s.db, &bytes); st == C.ZU2_OK {
		onDisk = int64(bytes)
	}
	return engine.LoadStats{
		Duration:    time.Since(start),
		Nodes:       s.nodes,
		Edges:       s.edges,
		BytesOnDisk: onDisk,
		Method:      "capi",
	}, nil
}

// loadNodes adds one vertex per row of a node CSV, keyed by the id
// column, and records the dense id it came back with.
func (c *conn) loadNodes(ctx context.Context, path string, ids map[string]C.uint32_t) error {
	return eachRow(ctx, path, []string{":ID"}, func(cols []string) error {
		key := cols[0]
		if _, dup := ids[key]; dup {
			return fmt.Errorf("zu2: %s: id %q appears twice", path, key)
		}
		kp, kn := bytesOf(key)
		var id C.uint32_t
		if st := C.zu2_add_vertex(c.s, kp, kn, &id); st != C.ZU2_OK {
			return sessErr(c.s, st, fmt.Sprintf("add vertex %q", key))
		}
		ids[key] = id
		return nil
	})
}

// loadRels adds one edge per row of a rel CSV and reports how many.
func (c *conn) loadRels(ctx context.Context, path string, ids map[string]C.uint32_t) (int64, error) {
	var n int64
	err := eachRow(ctx, path, []string{":START_ID", ":END_ID"}, func(cols []string) error {
		src, ok := ids[cols[0]]
		if !ok {
			return fmt.Errorf("zu2: %s: edge from %q, which is not a node in this dataset", path, cols[0])
		}
		dst, ok := ids[cols[1]]
		if !ok {
			return fmt.Errorf("zu2: %s: edge to %q, which is not a node in this dataset", path, cols[1])
		}
		if st := C.zu2_add_edge(c.s, src, dst); st != C.ZU2_OK {
			return sessErr(c.s, st, fmt.Sprintf("add edge %s -> %s", cols[0], cols[1]))
		}
		n++
		return nil
	})
	return n, err
}

// eachRow reads a canonical-layout CSV and calls fn with the columns
// whose headers end in the given suffixes, in that order. The headers
// are the neo4j-admin spelling the dataset generator writes: "id:ID",
// ":START_ID", ":END_ID". Matching on the suffix rather than the
// position is what keeps a dataset that carries properties readable.
func eachRow(ctx context.Context, path string, suffixes []string, fn func([]string) error) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("zu2: open %s: %w", path, err)
	}
	defer f.Close()
	r := csv.NewReader(f)
	r.ReuseRecord = true
	r.FieldsPerRecord = -1
	header, err := r.Read()
	if err != nil {
		return fmt.Errorf("zu2: read header of %s: %w", path, err)
	}
	want := make([]int, len(suffixes))
	for i, suffix := range suffixes {
		want[i] = -1
		for j, name := range header {
			if strings.HasSuffix(name, suffix) {
				want[i] = j
				break
			}
		}
		if want[i] < 0 {
			return fmt.Errorf("zu2: %s has no %s column, its header is %q", path, suffix, strings.Join(header, ","))
		}
	}
	cols := make([]string, len(want))
	for line := 2; ; line++ {
		record, err := r.Read()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("zu2: %s line %d: %w", path, line, err)
		}
		if line%4096 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		for i, at := range want {
			if at >= len(record) {
				return fmt.Errorf("zu2: %s line %d has %d fields, wanted at least %d", path, line, len(record), at+1)
			}
			cols[i] = record[at]
		}
		if err := fn(cols); err != nil {
			return err
		}
	}
}

// sizeOf reads the dataset's recorded node count, which is what the
// index and the vertex table are sized from. A dataset with no manifest
// says nothing and the engine's defaults stand.
func sizeOf(ds engine.Dataset) int64 {
	m := ds.Manifest()
	if m == nil {
		return 0
	}
	return m.Invariants.NodeCount
}

// Exec runs one directive on a session out of the pool. Safe to call
// concurrently, because no two callers get the same session. libzu2 has
// no cancellation, so the context is checked on the way in and not again
// until the call returns.
func (s *Session) Exec(ctx context.Context, op engine.Op) (engine.Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d, err := s.plan(op.Text)
	if err != nil {
		return nil, err
	}
	c, err := s.acquire()
	if err != nil {
		return nil, err
	}
	defer s.release(c)
	return c.run(d, op.Params)
}

// plan parses a directive once per text and remembers it.
func (s *Session) plan(text string) (directive, error) {
	if cached, ok := s.plans.Load(text); ok {
		return cached.(directive), nil
	}
	d, err := parse(text)
	if err != nil {
		return directive{}, err
	}
	s.plans.Store(text, d)
	return d, nil
}

// run executes one parsed directive and copies its answer out.
func (c *conn) run(d directive, params map[string]engine.Value) (engine.Result, error) {
	switch d.verb {
	case verbCount:
		return one(d.column, int64(C.zu2_vertices(c.db))), nil

	case verbPoint:
		k, err := key(params, d.seed)
		if err != nil {
			return nil, err
		}
		_, found, err := c.vertexOf(k)
		if err != nil {
			return nil, err
		}
		if !found {
			return none(d.column), nil
		}
		return one(d.column, idValue(k)), nil

	case verbEdge:
		src, dst, both, err := c.pair(params, d)
		if err != nil {
			return nil, err
		}
		if !both {
			// An endpoint that is not in the graph cannot have the
			// edge, and that is false rather than no rows: the
			// question was whether the edge is there.
			return one(d.column, false), nil
		}
		var out *C.uint32_t
		var n C.size_t
		if st := C.zu2_neighbours(c.s, C.zu2_direction(dirOut), src, &out, &n); st != C.ZU2_OK {
			return nil, sessErr(c.s, st, "neighbours")
		}
		return one(d.column, contains(slice(out, n), uint32(dst))), nil

	case verbDegree:
		seed, found, err := c.seedOf(params, d)
		if err != nil || !found {
			return zeroOr(d, err)
		}
		var degree C.uint32_t
		if st := C.zu2_degree(c.s, C.zu2_direction(d.dir), seed, &degree); st != C.ZU2_OK {
			return nil, sessErr(c.s, st, "degree")
		}
		return one(d.column, int64(degree)), nil

	case verbKhop:
		seed, found, err := c.seedOf(params, d)
		if err != nil || !found {
			return zeroOr(d, err)
		}
		var out *C.uint32_t
		var n C.size_t
		st := C.zu2_khop(c.s, C.zu2_direction(d.dir), seed, C.uint32_t(d.depth), &out, &n)
		if st != C.ZU2_OK {
			return nil, sessErr(c.s, st, "khop")
		}
		return one(d.column, int64(n)), nil

	case verbReach:
		seed, found, err := c.seedOf(params, d)
		if err != nil || !found {
			return zeroOr(d, err)
		}
		var out *C.uint32_t
		var n C.size_t
		// No cap on the size of the answer: a cap would turn a count
		// into a different number without saying so.
		st := C.zu2_reach(c.s, C.zu2_direction(d.dir), seed, C.uint32_t(d.depth), 0, &out, &n)
		if st != C.ZU2_OK {
			return nil, sessErr(c.s, st, "reach")
		}
		return one(d.column, int64(n)), nil

	case verbPath:
		src, dst, both, err := c.pair(params, d)
		if err != nil {
			return nil, err
		}
		if !both {
			return none(d.column), nil
		}
		var hops C.uint32_t
		var found C.int
		// No depth bound: a bounded search that ends without arriving
		// reports the same not-found as one with no path at all, and
		// the reference here is the true distance.
		st := C.zu2_shortest(c.s, C.zu2_direction(d.dir), src, dst, 0, &hops, &found)
		if st != C.ZU2_OK {
			return nil, sessErr(c.s, st, "shortest")
		}
		if found == 0 {
			return none(d.column), nil
		}
		return one(d.column, int64(hops)), nil
	}
	return nil, fmt.Errorf("zu2: %q has no implementation, which is a bug in this adapter", d.verb)
}

// seedOf resolves a directive's seed parameter to a dense vertex id.
func (c *conn) seedOf(params map[string]engine.Value, d directive) (C.uint32_t, bool, error) {
	k, err := key(params, d.seed)
	if err != nil {
		return 0, false, err
	}
	return c.vertexOf(k)
}

// pair resolves a directive's two endpoint parameters, reporting
// whether both are vertices this graph has.
func (c *conn) pair(params map[string]engine.Value, d directive) (C.uint32_t, C.uint32_t, bool, error) {
	srcKey, err := key(params, d.src)
	if err != nil {
		return 0, 0, false, err
	}
	dstKey, err := key(params, d.dst)
	if err != nil {
		return 0, 0, false, err
	}
	src, ok, err := c.vertexOf(srcKey)
	if err != nil || !ok {
		return 0, 0, false, err
	}
	dst, ok, err := c.vertexOf(dstKey)
	if err != nil || !ok {
		return 0, 0, false, err
	}
	return src, dst, true, nil
}

// vertexOf is the index probe: one hash lookup of the key a vertex was
// loaded under. Every seeded directive pays exactly one, at the seed,
// which is the same probe a Cypher engine pays to bind `{id: $seed}`.
func (c *conn) vertexOf(k string) (C.uint32_t, bool, error) {
	kp, kn := bytesOf(k)
	var id C.uint32_t
	var found C.int
	if st := C.zu2_vertex_of(c.s, kp, kn, &id, &found); st != C.ZU2_OK {
		return 0, false, sessErr(c.s, st, fmt.Sprintf("vertex_of %q", k))
	}
	return id, found != 0, nil
}

// zeroOr is the answer for a seed that is not in the graph: an
// expansion from a vertex that is not there reaches nothing, and nothing
// counts zero. It is not an empty result, because the reference for a
// count query is a row holding zero.
func zeroOr(d directive, err error) (engine.Result, error) {
	if err != nil {
		return nil, err
	}
	return one(d.column, int64(0)), nil
}

// slice reads a C answer buffer as a Go slice. It aliases the session's
// buffer and so is only good until the next call on that session, which
// is why every caller finishes with it before returning.
func slice(p *C.uint32_t, n C.size_t) []uint32 {
	if p == nil || n == 0 {
		return nil
	}
	return unsafe.Slice((*uint32)(unsafe.Pointer(p)), int(n))
}

// contains reports whether a neighbour list holds a vertex. The list is
// ascending, which the header promises, so this is a descent rather than
// a scan and a hub's neighbourhood does not turn an adjacency probe into
// a phase.
func contains(list []uint32, want uint32) bool {
	i := sort.Search(len(list), func(i int) bool { return list[i] >= want })
	return i < len(list) && list[i] == want
}

// bytesOf hands C a pointer into a Go string for the length of one call.
// libzu2 copies what it keeps, so nothing on the C side outlives the
// call, which is the condition the cgo pointer rules ask for. An empty
// string is a NULL and a zero length, which the header calls the empty
// string rather than an error.
func bytesOf(s string) (*C.uint8_t, C.size_t) {
	if len(s) == 0 {
		return nil, 0
	}
	return (*C.uint8_t)(unsafe.Pointer(unsafe.StringData(s))), C.size_t(len(s))
}

// Close frees every session the pool ever opened, checked out or not,
// closes the database, and removes the work dir. The lifecycle has Close
// after the last Exec has returned, so a session still out at this point
// is one a caller leaked, and leaving it open would leak its epoch slot
// with it. Idempotent.
func (s *Session) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	for _, c := range s.all {
		c.close()
	}
	s.all, s.idle = nil, nil
	if s.db != nil {
		C.zu2_close(s.db)
		s.db = nil
	}
	if s.workDir != "" {
		os.RemoveAll(s.workDir)
	}
	return nil
}

// openLocked opens the database, sized for a dataset of n nodes. Caller
// holds s.mu. A second call is a no-op: the file is open and neither the
// index nor the vertex table can be resized under it.
func (s *Session) openLocked(n int64) error {
	if s.db != nil {
		return nil
	}
	var opt C.zu2_options
	if st := C.zu2_options_init(&opt); st != C.ZU2_OK {
		return fmt.Errorf("zu2: options_init returned status %d", int(st))
	}
	opt.durability = s.durability
	if n > 0 {
		// One record per vertex and eight slots to a bucket, sized so
		// the index sits at half its slots when the load is done.
		// Undersizing it is what turns a probe into a walk down a
		// chain, and the count is on the manifest, so there is no
		// reason to find out the hard way.
		opt.index_buckets = C.uint64_t(powerOfTwoAtLeast(uint64(n)/4 + 1))
		opt.max_vertices = C.uint64_t(uint64(n) + uint64(n)/8 + 1024)
	}
	cpath := C.CString(s.dbPath)
	defer C.free(unsafe.Pointer(cpath))
	var db *C.zu2_db
	var cerr *C.char
	var errLen C.size_t
	st := C.zu2_open(cpath, C.size_t(len(s.dbPath)), &opt, &db, &cerr, &errLen)
	if st != C.ZU2_OK {
		msg := ""
		if cerr != nil {
			msg = C.GoStringN(cerr, C.int(errLen))
		}
		return fmt.Errorf("zu2: open %q failed with status %d: %s", s.dbPath, int(st), msg)
	}
	s.db = db
	return nil
}

// powerOfTwoAtLeast rounds up to a power of two, which is what the
// bucket count has to be.
func powerOfTwoAtLeast(n uint64) uint64 {
	p := uint64(1)
	for p < n {
		p <<= 1
	}
	return p
}

// acquire takes a session out of the pool, opening one if every session
// the pool has is already out.
func (s *Session) acquire() (*conn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acquireLocked()
}

// acquireLocked is acquire with the lock already held, for Load, which
// holds it across the whole load. Caller holds s.mu.
func (s *Session) acquireLocked() (*conn, error) {
	if s.closed {
		return nil, errors.New("zu2: session is closed")
	}
	// A query before any load: nothing sized it, so the engine's own
	// defaults stand.
	if err := s.openLocked(0); err != nil {
		return nil, err
	}
	if n := len(s.idle); n > 0 {
		c := s.idle[n-1]
		s.idle = s.idle[:n-1]
		return c, nil
	}
	var handle *C.zu2_session
	if st := C.zu2_session_open(s.db, &handle); st != C.ZU2_OK {
		return nil, dbErr(s.db, st, "session_open")
	}
	if st := C.zu2_set_durability(handle, s.durability); st != C.ZU2_OK {
		C.zu2_session_close(handle)
		return nil, fmt.Errorf("zu2: set_durability returned status %d", int(st))
	}
	c := &conn{s: handle, db: s.db}
	s.all = append(s.all, c)
	return c, nil
}

// release puts a session back for the next caller.
func (s *Session) release(c *conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.releaseLocked(c)
}

// releaseLocked is release with the lock already held. A session given
// back after Close is dropped rather than pooled: Close already freed
// it. Caller holds s.mu.
func (s *Session) releaseLocked(c *conn) {
	if s.closed {
		return
	}
	s.idle = append(s.idle, c)
}

// close frees one libzu2 session.
func (c *conn) close() {
	if c.s != nil {
		C.zu2_session_close(c.s)
		c.s = nil
	}
}

// sessErr turns a failed status into a Go error carrying whatever the
// session had to say about it.
func sessErr(s *C.zu2_session, st C.zu2_status, what string) error {
	var n C.size_t
	msg := C.zu2_session_error(s, &n)
	if msg == nil || n == 0 {
		return fmt.Errorf("zu2: %s failed with status %d", what, int(st))
	}
	return fmt.Errorf("zu2: %s: %s", what, C.GoStringN(msg, C.int(n)))
}

// dbErr is sessErr for the calls made on the database handle.
func dbErr(db *C.zu2_db, st C.zu2_status, what string) error {
	var n C.size_t
	msg := C.zu2_db_error(db, &n)
	if msg == nil || n == 0 {
		return fmt.Errorf("zu2: %s failed with status %d", what, int(st))
	}
	return fmt.Errorf("zu2: %s: %s", what, C.GoStringN(msg, C.int(n)))
}
