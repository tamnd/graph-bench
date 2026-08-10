// Package gen holds the deterministic synthetic graph generators: the five
// structural families carried from v1 (uniform, powerlaw, er, grid, rmat) and
// the four benchmark-shaped families added in v0.3 (urand, fin, lb, social).
// Each turns a seed and a small parameter set into a graph in the canonical
// CSV layout, and each is bit-reproducible, so the same generator name,
// version, and config produce byte-identical files on any machine. They are
// the data the regression gate runs on (it needs bytes that are identical
// between the baseline and the candidate) and the data that isolates one
// structural property at a time so a result can be attributed to that
// property rather than to the tangle of a realistic graph.
//
// See notes/Spec/2064g/bench/05-datasets.md §2 for the generator catalog and
// the bit-reproducibility contract this package enforces at the type level:
// Generate takes no clock and no ambient randomness, only Config.Seed.
package gen

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/tamnd/graph-bench/engine"
)

// nodeLabel and relType are the single label and single relationship type
// every bare structural generator emits. Using one pair across all of them
// means a synthetic workload query is written once and runs on any of them;
// the structural difference between datasets is the edge wiring, not the
// schema. The multi-table generators (fin, lb, social) declare their own
// labels and types instead.
const (
	nodeLabel = "Node"
	relType   = "EDGE"
)

// Generator emits a graph in the canonical CSV layout, deterministically from
// its config's seed. One Generator is one structural family.
type Generator interface {
	// Name is the stable generator id recorded in the manifest: "uniform",
	// "powerlaw", "er", "grid", "rmat", "urand", "fin", "lb", or "social".
	Name() string

	// Version is the algorithm version, recorded in the manifest. It bumps
	// whenever the emitted bytes would change for the same config, so the
	// manifest's (Name, Version, Config) triple is an exact reproduction
	// recipe. Every version is a decimal integer string: the checksum recipe
	// folds the v1 integer form (see dataset/checksum.go).
	Version() string

	// Generate writes the graph to w (which lays out nodes/, rels/, and the
	// manifest) and returns the manifest it wrote. It is deterministic: the
	// same Config in produces byte-identical output. It takes no clock and no
	// ambient randomness; all randomness comes from Config.Seed.
	Generate(ctx context.Context, cfg Config, w Writer) (*engine.Manifest, error)
}

// Config is the union of every generator's parameters; a generator reads the
// fields it needs and ignores the rest. Kind selects the generator.
type Config struct {
	Kind string // generator name, see New
	Seed int64  // the only source of randomness

	// Uniform / PowerLaw / ER / LinkBench:
	N int64 // node count (lb: defaults to 10000 when 0)

	// Uniform:
	Degree int // out-degree of every node (k-regular)

	// PowerLaw:
	Gamma  float64 // exponent of the degree distribution, e.g. 2.5
	MinDeg int     // minimum degree
	MaxDeg int     // maximum degree (clamp for the tail)

	// ER (G(n,p)):
	P float64 // independent edge probability

	// Grid:
	Rows, Cols int  // 2D mesh dimensions; N = Rows*Cols
	Diagonal   bool // include diagonal edges (8-neighbor) or not (4-neighbor)

	// RMAT / URand:
	Scale      int        // log2 of the node count; N = 2^Scale
	EdgeFactor int        // edges per node; edges = EdgeFactor * N (urand: defaults to 16 when 0)
	Initiator  [4]float64 // rmat only: (A,B,C,D); zero value means the Graph500 default
	Weighted   bool       // rmat only: add the GAP int64 weight property w in 1..255 (urand always has it)

	// Fin (FinBench-shaped):
	Accounts int64   // account count; defaults to 10000 when 0
	Days     int     // simulation window in days; defaults to 30 when 0
	TxPerDay int     // transfers per simulated day; defaults to 10000 when 0
	HubFrac  float64 // fraction of accounts receiving power-law-concentrated transfers; defaults to 0.01 when 0

	// Social (SNB-shaped):
	Persons        int // person count; defaults to 1000 when 0
	AvgFriends     int // approximate mean KNOWS degree, power-law shaped; defaults to 20 when 0
	PostsPerPerson int // posts authored per person; defaults to 10 when 0

	// ComputeInvariants asks the generator to compute the optional
	// ground-truth invariants (triangle count, diameter) where it can do so
	// cheaply.
	ComputeInvariants bool
}

// RowWriter writes the rows of one canonical CSV file. The header was given to
// the Writer when the file was opened; every Write is one data row whose cells
// align to that header. Close finalizes the file.
type RowWriter interface {
	Write(cells []string) error
	Close() error
}

// Writer is the canonical-layout sink: it opens a node file per label and a
// relationship file per type (with its endpoint labels), writes typed headers
// and rows, and finalizes the manifest with the checksum and totals. The same
// Writer is used by every generator so the on-disk form is identical
// regardless of which generator produced it. The concrete implementation
// lives in the dataset package.
type Writer interface {
	NodeFile(label string, header []engine.Column) (RowWriter, error)
	RelFile(typ, start, end string, header []engine.Column) (RowWriter, error)
	Finalize(partial *engine.Manifest) (*engine.Manifest, error)
}

// New returns the generator for a config's Kind, or an error if the kind is
// unknown. The dispatch is the one place the kind string is mapped to a
// generator, so the CLI and the tests agree on the set.
func New(kind string) (Generator, error) {
	switch kind {
	case "uniform":
		return Uniform{}, nil
	case "powerlaw":
		return PowerLaw{}, nil
	case "er":
		return ER{}, nil
	case "grid":
		return Grid{}, nil
	case "rmat":
		return RMAT{}, nil
	case "urand":
		return URand{}, nil
	case "fin":
		return Fin{}, nil
	case "lb":
		return LB{}, nil
	case "social":
		return Social{}, nil
	default:
		return nil, fmt.Errorf("gen: unknown generator %q", kind)
	}
}

// Generate is the convenience entry the CLI uses: it selects the generator for
// cfg.Kind and runs it into w.
func Generate(ctx context.Context, cfg Config, w Writer) (*engine.Manifest, error) {
	g, err := New(cfg.Kind)
	if err != nil {
		return nil, err
	}
	return g.Generate(ctx, cfg, w)
}

// nodeHeader is the header every bare structural generator writes: an id
// column and a label column, no properties. The structural graphs carry no
// node properties, so this is the whole node schema.
func nodeHeader() []engine.Column {
	return []engine.Column{{Name: "id", Type: "ID"}, {Name: "", Type: "LABEL"}}
}

// relHeader is the header every bare structural generator writes for edges: a
// start id, an end id, and a type, no properties.
func relHeader() []engine.Column {
	return []engine.Column{{Name: "", Type: "START_ID"}, {Name: "", Type: "END_ID"}, {Name: "", Type: "TYPE"}}
}

// i64p returns a pointer to v, for the optional invariant fields.
func i64p(v int64) *int64 { return &v }

// checkCanceled returns ctx.Err() so a long generation honors cancellation at
// the points a generator calls it (once per source node is enough
// granularity).
func checkCanceled(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

// The param helpers format manifest param values in the canonical string form
// the checksum recipe inverts exactly (dataset.Checksum re-types them to the
// v1 typed values before hashing, so a v0.3 generation reproduces the v0.2
// checksum for the same recipe).

// iparam formats an integer param.
func iparam[T int | int64](v T) string { return strconv.FormatInt(int64(v), 10) }

// fparam formats a float param with the exact token json.Marshal emits.
func fparam(v float64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// bparam formats a bool param.
func bparam(v bool) string { return strconv.FormatBool(v) }

// jparam formats a composite param (an array) as compact JSON.
func jparam(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}
