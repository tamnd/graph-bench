//go:build zuinproc

package zu

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tamnd/graph-bench/engine"
)

// startInproc loads a four-edge graph through a real zu binary and hands
// back an open in-process session. It skips when no zu binary is around,
// since Load has to run the copy verb for real.
func startInproc(t *testing.T) *Session {
	t.Helper()
	if _, err := discoverBinary(engine.Config{}); err != nil {
		t.Skipf("no zu binary: %v", err)
	}
	dir := t.TempDir()
	relFile := filepath.Join(dir, "EDGE.csv")
	csv := ":START_ID,:END_ID,:TYPE\n0,1,EDGE\n1,2,EDGE\n2,3,EDGE\n0,2,EDGE\n"
	if err := os.WriteFile(relFile, []byte(csv), 0o644); err != nil {
		t.Fatal(err)
	}

	sess, err := New().Start(context.Background(), engine.Config{})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	s := sess.(*Session)
	t.Cleanup(func() { s.Close(context.Background()) })

	stats, err := s.Load(context.Background(), &fileDataset{dir: dir, relFile: relFile})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if stats.Edges != 4 {
		t.Fatalf("Edges = %d, want 4", stats.Edges)
	}
	return s
}

// exec runs one query and drains it into rows.
func execRows(t *testing.T, s *Session, text string, params map[string]engine.Value) ([]string, [][]engine.Value) {
	t.Helper()
	res, err := s.Exec(context.Background(), engine.Op{
		QueryID: "test", Class: engine.PointRead, Dialect: engine.ZuQL,
		Text: text, Params: params,
	})
	if err != nil {
		t.Fatalf("Exec %q: %v", text, err)
	}
	defer res.Close()
	var rows [][]engine.Value
	for res.Next() {
		rows = append(rows, append([]engine.Value(nil), res.Row()...))
	}
	if err := res.Err(); err != nil {
		t.Fatalf("drain %q: %v", text, err)
	}
	return res.Columns(), rows
}

func TestInprocLoadAndExec(t *testing.T) {
	s := startInproc(t)

	cols, rows := execRows(t, s, `MATCH (n:node) RETURN count(n) AS n`, nil)
	if len(cols) != 1 || cols[0] != "n" {
		t.Errorf("columns = %v, want [n]", cols)
	}
	if len(rows) != 1 || rows[0][0] != int64(4) {
		t.Errorf("count = %v, want 4 nodes", rows)
	}
}

func TestInprocParamBinding(t *testing.T) {
	s := startInproc(t)

	// Node 0 has two out-edges, node 3 has none. The same statement is
	// reused across both, which is what the bind path is for.
	text := `MATCH (a:node {id: $seed})-[:edge]->(b:node) RETURN count(b) AS n`
	for _, tc := range []struct {
		seed int64
		want int64
	}{{0, 2}, {1, 1}, {3, 0}} {
		_, rows := execRows(t, s, text, map[string]engine.Value{"seed": tc.seed})
		if len(rows) != 1 || rows[0][0] != tc.want {
			t.Errorf("seed %d: got %v, want %d", tc.seed, rows, tc.want)
		}
	}
	if len(s.stmts) != 1 {
		t.Errorf("statement cache has %d entries, want 1 for one text", len(s.stmts))
	}
}

func TestInprocValueDecode(t *testing.T) {
	s := startInproc(t)

	// A bool column (comparison), an int column (id), and a string
	// column all decode through the columnar reads.
	_, rows := execRows(t, s,
		`MATCH (a:node {id: $src})-[:edge]->(b:node {id: $dst}) WITH count(*) AS c RETURN c > 0 AS found`,
		map[string]engine.Value{"src": int64(0), "dst": int64(1)})
	if len(rows) != 1 || rows[0][0] != true {
		t.Errorf("found = %v, want true", rows)
	}

	_, rows = execRows(t, s,
		`MATCH (n:node {id: $id}) RETURN n.id AS id`,
		map[string]engine.Value{"id": int64(2)})
	if len(rows) != 1 || rows[0][0] != int64(2) {
		t.Errorf("id = %v, want 2", rows)
	}

	_, rows = execRows(t, s, `MATCH (n:node) RETURN count(n) AS n, min(n.id) AS lo, max(n.id) AS hi`, nil)
	if len(rows) != 1 || rows[0][1] != int64(0) || rows[0][2] != int64(3) {
		t.Errorf("stats = %v, want lo 0 hi 3", rows)
	}
}

// A bool used to be the one Go value the bind had nowhere to put, and the
// adapter turned it into an error rather than a string that looked like one.
// libzu grew zu_bind_bool_z, so it binds now, and this holds the round trip:
// what goes in as true comes back as true.
func TestInprocBoolParamBinds(t *testing.T) {
	s := startInproc(t)
	for _, want := range []bool{true, false} {
		_, rows := execRows(t, s, `RETURN $flag AS flag`, map[string]engine.Value{"flag": want})
		if len(rows) != 1 || rows[0][0] != want {
			t.Errorf("flag = %v, want %v", rows, want)
		}
	}
}

func TestInprocCloseIdempotent(t *testing.T) {
	s := startInproc(t)
	execRows(t, s, `MATCH (n:node) RETURN count(n) AS n`, nil)
	for range 3 {
		if err := s.Close(context.Background()); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
	if _, err := s.Exec(context.Background(), engine.Op{Text: `MATCH (n:node) RETURN count(n) AS n`}); err == nil {
		t.Error("Exec on a closed session succeeded, want an error")
	}
}

func TestInprocInfo(t *testing.T) {
	info := New().Info()
	if info.Name != "zu" {
		t.Errorf("Name = %q, want zu", info.Name)
	}
	if info.Plane != engine.InProc {
		t.Errorf("Plane = %v, want inproc", info.Plane)
	}
	if len(info.Dialects) != 1 || info.Dialects[0] != engine.ZuQL {
		t.Errorf("Dialects = %v, want [zuql]", info.Dialects)
	}
	if info.Caps.MaxConcurrency != 1 {
		t.Errorf("MaxConcurrency = %d, want 1 (a libzu session is not thread safe)", info.Caps.MaxConcurrency)
	}
}

func TestInprocVersion(t *testing.T) {
	v, err := (&Session{}).Version(context.Background())
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if v == "" {
		t.Error("Version is empty, want the linked libzu version")
	}
}
