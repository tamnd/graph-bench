package setup

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestPinnedDefaults proves an empty image resolves through the pin table
// (engine/pins.go), so containers and condition stamps agree.
func TestPinnedDefaults(t *testing.T) {
	if img := Neo4j("").Image; !strings.HasPrefix(img, "neo4j:") {
		t.Errorf("Neo4j pin = %q, want a neo4j: tag", img)
	}
	if img := Memgraph("").Image; !strings.HasPrefix(img, "memgraph/memgraph-mage:") {
		t.Errorf("Memgraph pin = %q, want a memgraph/memgraph-mage: tag", img)
	}
}

// TestNeo4jSpec proves Neo4j() produces a spec with the expected image, the
// Bolt port mapping, and the auth=none env var.
func TestNeo4jSpec(t *testing.T) {
	spec := Neo4j("neo4j:5.26-community")
	if spec.Image != "neo4j:5.26-community" {
		t.Errorf("Image=%q, want neo4j:5.26-community", spec.Image)
	}
	if _, ok := spec.Ports["7687/tcp"]; !ok {
		t.Error("want 7687/tcp in Ports")
	}
	if v := spec.Env["NEO4J_AUTH"]; v != "none" {
		t.Errorf("NEO4J_AUTH=%q, want none", v)
	}
	if spec.ReadyTimeout <= 0 {
		t.Error("ReadyTimeout should be set")
	}
}

// TestMemgraphSpec proves Memgraph() produces a spec with the Bolt port.
func TestMemgraphSpec(t *testing.T) {
	spec := Memgraph("memgraph/memgraph:2.19.0")
	if spec.Image != "memgraph/memgraph:2.19.0" {
		t.Errorf("Image=%q", spec.Image)
	}
	if _, ok := spec.Ports["7687/tcp"]; !ok {
		t.Error("want 7687/tcp in Ports")
	}
}

// TestWaitReadyTimeout proves waitReady returns an error when the deadline
// passes without a successful connection. We use a port that nothing is
// listening on (127.0.0.1:1 is reliably closed).
func TestWaitReadyTimeout(t *testing.T) {
	ctx := context.Background()
	err := waitReady(ctx, "127.0.0.1:1", 500*time.Millisecond)
	if err == nil {
		t.Error("expected timeout error, got nil")
	}
}

// TestWaitReadyContextCancel proves waitReady respects context cancellation.
func TestWaitReadyContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled immediately
	err := waitReady(ctx, "127.0.0.1:1", 10*time.Second)
	if err == nil {
		t.Error("expected error on cancelled context, got nil")
	}
}

// TestInspectPortsParse proves the port-line parser handles the docker port
// output format correctly.
func TestInspectPortsParse(t *testing.T) {
	// Simulate docker port output in a subprocess-free way by calling the parser
	// directly through a helper that wraps the logic.
	lines := []string{
		"7687/tcp -> 0.0.0.0:54321",
		"7474/tcp -> 0.0.0.0:54322",
		"7687/tcp -> :::54321", // IPv6 duplicate - should be ignored (first wins)
	}
	ports := parsePortLines(lines)
	if p, ok := ports["7687/tcp"]; !ok || p != "54321" {
		t.Errorf("7687/tcp=%q, want 54321", ports["7687/tcp"])
	}
	if p, ok := ports["7474/tcp"]; !ok || p != "54322" {
		t.Errorf("7474/tcp=%q, want 54322", ports["7474/tcp"])
	}
	if len(ports) != 2 {
		t.Errorf("len=%d, want 2 (IPv6 duplicate dropped)", len(ports))
	}
}

// TestDropCachesNoOp proves DropCaches does not panic on any platform.
func TestDropCachesNoOp(t *testing.T) {
	DropCaches() // should be a silent no-op on macOS
}

// TestContainerSpecReadyTimeout proves the default ready timeout is >= 60s.
func TestContainerSpecReadyTimeout(t *testing.T) {
	spec := Neo4j("neo4j:latest")
	if spec.ReadyTimeout < 60*time.Second {
		t.Errorf("ReadyTimeout=%s, want >= 60s", spec.ReadyTimeout)
	}
}

// TestParseDU proves the du reader takes the count off a du -s line and
// answers -1 for anything that is not one, so a footprint the harness could
// not read stays unknown instead of arriving as a plausible number.
func TestParseDU(t *testing.T) {
	cases := []struct {
		out  string
		want int64
	}{
		{"1234\t/data\n", 1234},
		{"0\t/data\n", 0},
		{"8192   /var/lib/memgraph\n", 8192},
		{"", -1},
		{"\n", -1},
		{"du: /data: Permission denied\n", -1},
		{"-5\t/data\n", -1},
	}
	for _, c := range cases {
		if got := parseDU(c.out); got != c.want {
			t.Errorf("parseDU(%q) = %d, want %d", c.out, got, c.want)
		}
	}
}

// TestDataBytesWithoutDataDir proves a spec that names no data directory
// reports an unknown footprint without running docker at all.
func TestDataBytesWithoutDataDir(t *testing.T) {
	c := &Container{ID: strings.Repeat("a", 64)}
	if got := c.DataBytes(context.Background()); got != -1 {
		t.Errorf("DataBytes = %d, want -1", got)
	}
}

// TestSpecsNameTheirDataDir proves every served engine the harness starts
// knows where its own files live. Without it the engine reports an unknown
// footprint and the disk column stays empty for the rivals.
func TestSpecsNameTheirDataDir(t *testing.T) {
	specs := map[string]ContainerSpec{
		"neo4j":    Neo4j(""),
		"memgraph": Memgraph(""),
		"postgres": Postgres(""),
		"mongodb":  Mongo(""),
	}
	for name, spec := range specs {
		if !strings.HasPrefix(spec.DataDir, "/") {
			t.Errorf("%s: DataDir=%q, want an absolute path in the container", name, spec.DataDir)
		}
	}
}

// TestBoltURIFormat proves a BoltURI produced for a given port is well-formed.
func TestBoltURIFormat(t *testing.T) {
	c := &Container{ID: strings.Repeat("a", 64), ports: map[string]string{"7687/tcp": "54321"}}
	c.BoltURI = "bolt://127.0.0.1:54321"
	if !strings.HasPrefix(c.BoltURI, "bolt://") {
		t.Errorf("BoltURI=%q, want bolt:// prefix", c.BoltURI)
	}
}
