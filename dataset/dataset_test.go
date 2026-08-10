package dataset

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tamnd/graph-bench/dataset/gen"
	"github.com/tamnd/graph-bench/engine"
)

// generate materializes a config into a fresh temp directory and returns the
// dataset directory and the manifest. It is the test's stand-in for the
// generate verb's staging-and-name flow.
func generate(t *testing.T, cfg gen.Config) (string, *engine.Manifest) {
	t.Helper()
	dir := t.TempDir()
	w, err := NewWriter(dir)
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	m, err := gen.Generate(context.Background(), cfg, w)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return dir, m
}

// TestWriteAndOpen is the round trip: generate a dataset, open it back, and
// check the manifest, schema, and file accessors all reflect what was
// written.
func TestWriteAndOpen(t *testing.T) {
	dir, m := generate(t, gen.Config{Kind: "grid", Rows: 10, Cols: 12})

	ds, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if ds.Name() != m.Name {
		t.Errorf("Name = %q, want %q", ds.Name(), m.Name)
	}
	if ds.Checksum() != m.Checksum || m.Checksum == "" {
		t.Errorf("Checksum = %q, manifest %q", ds.Checksum(), m.Checksum)
	}
	if ds.Manifest().Invariants.NodeCount != 120 {
		t.Errorf("NodeCount = %d, want 120", ds.Manifest().Invariants.NodeCount)
	}
	if ds.Manifest().Kind != "synthetic" || ds.Manifest().Scale != "10x12" {
		t.Errorf("Kind/Scale = %q/%q, want synthetic/10x12", ds.Manifest().Kind, ds.Manifest().Scale)
	}

	files, err := ds.NodeFiles("Node")
	if err != nil {
		t.Fatalf("NodeFiles: %v", err)
	}
	if len(files) != 1 || filepath.Base(files[0]) != "Node.csv" {
		t.Errorf("NodeFiles = %v, want one Node.csv", files)
	}
	if !filepath.IsAbs(files[0]) {
		t.Errorf("NodeFiles path %q is not absolute", files[0])
	}
	// The node header is id:ID,:LABEL, so two columns and an ID column
	// present, mirrored by the schema.
	cols, err := ReadHeader(files[0])
	if err != nil {
		t.Fatalf("ReadHeader: %v", err)
	}
	if len(cols) != 2 || cols[0].Type != "ID" || cols[1].Type != "LABEL" {
		t.Errorf("node header = %v, want [id:ID :LABEL]", cols)
	}
	ns := ds.Schema().Nodes["Node"]
	if ns.ID != (engine.Column{Name: "id", Type: "ID"}) {
		t.Errorf("schema id column = %v, want id:ID", ns.ID)
	}
	rs := ds.Schema().Rels["EDGE"]
	if rs.Start != "Node" || rs.End != "Node" {
		t.Errorf("EDGE endpoints = %q->%q, want Node->Node", rs.Start, rs.End)
	}

	if _, err := ds.RelFiles("EDGE"); err != nil {
		t.Fatalf("RelFiles: %v", err)
	}
	if _, err := ds.NodeFiles("Nope"); err == nil {
		t.Error("NodeFiles(unknown) returned nil error")
	}
}

// TestMultiTableSchema checks a multi-label generator's endpoints survive the
// writer: fin's rel tables connect distinct labels.
func TestMultiTableSchema(t *testing.T) {
	dir, _ := generate(t, gen.Config{Kind: "fin", Seed: 2, Accounts: 100, Days: 2, TxPerDay: 50})
	ds, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	rs := ds.Schema().Rels["OWN"]
	if rs.Start != "Person" || rs.End != "Account" {
		t.Errorf("OWN endpoints = %q->%q, want Person->Account", rs.Start, rs.End)
	}
	if len(ds.Schema().Nodes) != 3 {
		t.Errorf("node tables = %d, want 3", len(ds.Schema().Nodes))
	}
	acc := ds.Schema().Nodes["Account"]
	if len(acc.Properties) != 2 || acc.Properties[0].Name != "createTime" || acc.Properties[1].Type != "BOOL" {
		t.Errorf("Account properties = %v, want [createTime:INT64 isBlocked:BOOL]", acc.Properties)
	}
}

// TestChecksumStable confirms the checksum is reproducible: regenerating the
// same recipe into a different directory yields the same checksum, and a
// different recipe yields a different one.
func TestChecksumStable(t *testing.T) {
	_, a := generate(t, gen.Config{Kind: "uniform", Seed: 3, N: 200, Degree: 4})
	_, b := generate(t, gen.Config{Kind: "uniform", Seed: 3, N: 200, Degree: 4})
	if a.Checksum != b.Checksum {
		t.Errorf("same recipe gave different checksums: %s vs %s", a.Checksum, b.Checksum)
	}
	_, c := generate(t, gen.Config{Kind: "uniform", Seed: 4, N: 200, Degree: 4})
	if a.Checksum == c.Checksum {
		t.Error("different seed gave the same checksum")
	}
}

// TestChecksumV1Golden pins the checksum algorithm to v1's bytes: these
// constants were computed by the v0.2 codebase for the same recipes, so a
// v0.3 generation reproduces v0.2 checksums exactly and v1-materialized
// datasets keep verifying (spec 05 §1: carried from v1 unchanged).
func TestChecksumV1Golden(t *testing.T) {
	cases := []struct {
		cfg  gen.Config
		want string
	}{
		{gen.Config{Kind: "uniform", Seed: 3, N: 200, Degree: 4},
			"sha256:6281a789824539eddce66ff9cf06572c7e829702631a4f3acc5d7319a4ab89c5"},
		{gen.Config{Kind: "grid", Seed: 1, Rows: 5, Cols: 5},
			"sha256:4a90c29c904c11faceb50d805bdda188e50192b7950b7ec9c65ae5366044abdd"},
		{gen.Config{Kind: "rmat", Seed: 123, Scale: 6, EdgeFactor: 4},
			"sha256:c8f3dfa5acf6c5389598137d71f3b73cbeb8f1e1991af3090218946c15d58de9"},
		{gen.Config{Kind: "powerlaw", Seed: 7, N: 100, Gamma: 2.5, MinDeg: 1, MaxDeg: 20},
			"sha256:1159c6da994c6734855d158df20526061598f2ca873a3085aadeabf1e584a069"},
		{gen.Config{Kind: "er", Seed: 99, N: 100, P: 0.05},
			"sha256:70cc84e21d8a6d550013622ef52f471fffb8b5b64515bc6470abe8e51c33bf33"},
	}
	for _, c := range cases {
		_, m := generate(t, c.cfg)
		if m.Checksum != c.want {
			t.Errorf("%s: checksum = %s, want the v1 value %s", m.Name, m.Checksum, c.want)
		}
	}
}

// v1ManifestRMAT is a manifest.json exactly as v0.2 wrote it for
// {Kind: "rmat", Seed: 123, Scale: 6, EdgeFactor: 4}: integer
// generatorVersion, typed params, top-level counts, and the old schema block
// ("relationships", "file", string ids).
const v1ManifestRMAT = `{
  "name": "rmat-s6-e4",
  "kind": "synthetic",
  "generator": "rmat",
  "generatorVersion": 1,
  "seed": 123,
  "params": {
    "duplicates": "kept",
    "edgeFactor": 4,
    "initiator": [
      0.57,
      0.19,
      0.19,
      0.05
    ],
    "scale": 6
  },
  "listDelimiter": ";",
  "null": "empty",
  "checksum": "sha256:c8f3dfa5acf6c5389598137d71f3b73cbeb8f1e1991af3090218946c15d58de9",
  "nodeCount": 64,
  "edgeCount": 256,
  "schema": {
    "nodes": {
      "Node": {
        "file": [
          "nodes/Node.csv"
        ],
        "id": "id",
        "properties": null,
        "labels": [
          "Node"
        ]
      }
    },
    "relationships": {
      "EDGE": {
        "file": [
          "rels/EDGE.csv"
        ],
        "properties": null,
        "start": "Node",
        "end": "Node"
      }
    }
  },
  "invariants": {
    "nodeCount": 64,
    "edgeCount": 256
  }
}
`

// TestOpenV1Manifest proves a dataset materialized by v0.2 still opens and
// verifies: the data files are byte-identical between the versions (the
// generators are ports), so overwriting the manifest with the exact v1 form
// reproduces a v1 directory, and Open must both parse it and re-derive the
// same checksum from its typed recipe.
func TestOpenV1Manifest(t *testing.T) {
	dir, m := generate(t, gen.Config{Kind: "rmat", Seed: 123, Scale: 6, EdgeFactor: 4})
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(v1ManifestRMAT), 0o644); err != nil {
		t.Fatalf("write v1 manifest: %v", err)
	}
	ds, err := Open(dir)
	if err != nil {
		t.Fatalf("Open on a v1-format dataset: %v", err)
	}
	got := ds.Manifest()
	if got.Checksum != m.Checksum {
		t.Errorf("checksum = %s, want %s", got.Checksum, m.Checksum)
	}
	if got.GeneratorVersion != "1" {
		t.Errorf("GeneratorVersion = %q, want \"1\" (normalized from the v1 integer)", got.GeneratorVersion)
	}
	want := map[string]string{
		"scale": "6", "edgeFactor": "4",
		"initiator": "[0.57,0.19,0.19,0.05]", "duplicates": "kept",
	}
	for k, v := range want {
		if got.Params[k] != v {
			t.Errorf("param %s = %q, want %q", k, got.Params[k], v)
		}
	}
	if got.Invariants.NodeCount != 64 || got.Invariants.EdgeCount != 256 {
		t.Errorf("counts = %d/%d, want 64/256", got.Invariants.NodeCount, got.Invariants.EdgeCount)
	}
	ns := got.SchemaDef.Nodes["Node"]
	if len(ns.Files) != 1 || ns.Files[0] != "nodes/Node.csv" {
		t.Errorf("node files = %v, want [nodes/Node.csv] (v1 \"file\" tag)", ns.Files)
	}
	if ns.ID != (engine.Column{Name: "id", Type: "ID"}) {
		t.Errorf("id column = %v, want id:ID (normalized from the v1 string)", ns.ID)
	}
	rs, ok := got.SchemaDef.Rels["EDGE"]
	if !ok || rs.Start != "Node" {
		t.Errorf("rels = %v, want EDGE from the v1 \"relationships\" key", got.SchemaDef.Rels)
	}
}

// TestVerifyDetectsTampering confirms Open fails when a data file is altered
// after the manifest's checksum was written, which is the corruption guard F2
// relies on.
func TestVerifyDetectsTampering(t *testing.T) {
	dir, _ := generate(t, gen.Config{Kind: "grid", Rows: 5, Cols: 5})
	// Append a row to a data file so the content no longer matches the
	// manifest.
	path := filepath.Join(dir, "rels", "EDGE.csv")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open for tamper: %v", err)
	}
	if _, err := f.WriteString("999,999,EDGE\n"); err != nil {
		t.Fatalf("tamper write: %v", err)
	}
	f.Close()

	if _, err := Open(dir); err == nil {
		t.Error("Open succeeded on a tampered dataset, want a checksum mismatch")
	}
}

// TestDirName checks the <name>-<checksum8> directory name derivation.
func TestDirName(t *testing.T) {
	m := &engine.Manifest{Name: "grid-10x12", Checksum: "sha256:9f2bc4d1deadbeef"}
	if got := DirName(m); got != "grid-10x12-9f2bc4d1" {
		t.Errorf("DirName = %q, want grid-10x12-9f2bc4d1", got)
	}
	// No checksum yet: the bare name.
	if got := DirName(&engine.Manifest{Name: "x"}); got != "x" {
		t.Errorf("DirName without checksum = %q, want x", got)
	}
}

// TestParseHeader covers the typed-header grammar: named property, named id,
// and the bare structural columns.
func TestParseHeader(t *testing.T) {
	cols, err := ParseHeader([]string{"id:ID", "name:STRING", "age:INT64", ":LABEL"})
	if err != nil {
		t.Fatalf("ParseHeader: %v", err)
	}
	want := []engine.Column{
		{Name: "id", Type: "ID"},
		{Name: "name", Type: "STRING"},
		{Name: "age", Type: "INT64"},
		{Name: "", Type: "LABEL"},
	}
	for i, c := range cols {
		if c != want[i] {
			t.Errorf("col %d = %+v, want %+v", i, c, want[i])
		}
	}
	// FormatHeader is the inverse.
	got := FormatHeader(cols)
	if got[0] != "id:ID" || got[3] != ":LABEL" {
		t.Errorf("FormatHeader = %v", got)
	}
}

// TestParamsPoolRoundTrip covers the params.json read/write pair: written
// pools come back in order with scalar types normalized, unknown keys stay
// nil, and a second write merges rather than clobbering.
func TestParamsPoolRoundTrip(t *testing.T) {
	dir, _ := generate(t, gen.Config{Kind: "grid", Rows: 4, Cols: 4})
	path := filepath.Join(dir, "params.json")

	pools := map[string]Pool{
		"micro-khop": {
			{"seed": int64(0), "hops": int64(2)},
			{"seed": int64(4), "hops": int64(3)},
		},
	}
	if err := WriteParamsPool(path, pools); err != nil {
		t.Fatalf("WriteParamsPool: %v", err)
	}
	// Merge a second key; the first must survive.
	if err := WriteParamsPool(path, map[string]Pool{
		"micro-sp": {{"src": "0", "dst": "8", "weight": 1.5}},
	}); err != nil {
		t.Fatalf("WriteParamsPool merge: %v", err)
	}

	ds, err := Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	got, err := ds.Params("micro-khop")
	if err != nil {
		t.Fatalf("Params: %v", err)
	}
	if len(got) != 2 || got[0]["seed"] != int64(0) || got[1]["hops"] != int64(3) {
		t.Errorf("micro-khop pool = %v, want the two written draws with int64 values", got)
	}
	sp, err := ds.Params("micro-sp")
	if err != nil {
		t.Fatalf("Params: %v", err)
	}
	if len(sp) != 1 || sp[0]["src"] != "0" || sp[0]["weight"] != 1.5 {
		t.Errorf("micro-sp pool = %v, want the merged draw", sp)
	}
	missing, err := ds.Params("nope")
	if err != nil || missing != nil {
		t.Errorf("Params(unknown) = %v, %v; want nil, nil", missing, err)
	}
}
