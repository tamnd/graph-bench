// Package setup starts and stops container-hosted graph engines for the
// Bolt plane. It is the only package that calls Docker. The rest of the
// harness speaks the engine SPI; setup is how a Bolt-plane engine gets
// its server (spec 09 §5).
//
// Usage pattern:
//
//	c, err := setup.Start(ctx, setup.Neo4j(""))
//	if err != nil { ... }
//	defer c.Stop(ctx)
//	// c.BoltURI is ready for the adapter's Start config
//
// Image tags default to the single pin table (engine/pins.go); passing an
// explicit image overrides the pin and the run must disclose it.
package setup

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/tamnd/graph-bench/engine"
)

// ContainerSpec describes one container to launch.
type ContainerSpec struct {
	// Image is the Docker image reference, e.g. "neo4j:2026.06.0-community".
	Image string
	// Name is an optional container name for debugging; empty means random.
	Name string
	// Env is the environment variables passed with -e. Keys with empty values
	// are skipped.
	Env map[string]string
	// Ports is a map from container port (e.g. "7687/tcp") to the local bind
	// port. If the value is "0" or empty, a free port is picked automatically.
	Ports map[string]string
	// ReadyAddr is the host:port to wait on for TCP readiness. It is derived
	// from Ports when left empty.
	ReadyAddr string
	// ReadyTimeout is how long to wait for ReadyAddr to accept connections.
	// Default 60s.
	ReadyTimeout time.Duration

	// Primary is the container port the adapter connects to, "7687/tcp"
	// when left empty. It is what Container.Addr reports and what the URI
	// template is filled with. Bolt was the only wire this package spoke
	// when it was written, which is why the default is Bolt's port.
	Primary string
	// URI is a template for the adapter's connection string with one %s
	// where the host:port goes, "bolt://%s" when left empty. A Postgres
	// spec carries a libpq URL here and a Mongo spec a mongodb:// one, so
	// the run verb hands the adapter a string it can dial rather than
	// reassembling one from a port.
	URI string

	// DataDir is the path inside the container where the server keeps its
	// database files. Container.DataBytes measures it, which is the only
	// way to size a served engine: its files are in a container this
	// process cannot walk, which is why every such engine has reported an
	// unknown footprint. Empty means unknown and DataBytes answers -1.
	DataDir string
}

// Container is a running container returned by Start.
type Container struct {
	ID string // Docker container ID
	// URI is the spec's template with the mapped host:port filled in: the
	// string the adapter connects with, whatever wire it speaks.
	URI string
	// BoltURI is URI when the primary port is Bolt's and empty otherwise.
	// It is the older name for the same thing, kept because the Bolt
	// adapters read it.
	BoltURI string
	// Addr is the host:port the primary container port is published on.
	Addr  string
	spec  ContainerSpec
	ports map[string]string // container port -> local port
}

// Port returns the host port a container port is published on, or the
// empty string when that port was not mapped.
func (c *Container) Port(containerPort string) string { return c.ports[containerPort] }

// pinnedImage returns the pin-table image for an engine, or def when the
// engine has no pin entry.
func pinnedImage(name, def string) string {
	if p, ok := engine.PinFor(name); ok && p.Pinned != "" {
		return p.Pinned
	}
	return def
}

// Neo4j returns a ContainerSpec for the given Neo4j image; an empty image
// means the pinned one.
func Neo4j(image string) ContainerSpec {
	if image == "" {
		image = pinnedImage("neo4j", "neo4j:2026.06.0-community")
	}
	return ContainerSpec{
		Image: image,
		Env: map[string]string{
			"NEO4J_AUTH":                                  "none",
			"NEO4J_PLUGINS":                               `["apoc"]`,
			"NEO4J_server_memory_heap_initial__size":      "512m",
			"NEO4J_server_memory_heap_max__size":          "1g",
			"NEO4J_server_memory_pagecache_size":          "512m",
			"NEO4J_dbms_security_procedures_unrestricted": "apoc.*",
		},
		Ports: map[string]string{
			"7687/tcp": "0", // Bolt: free port
			"7474/tcp": "0", // HTTP UI: free port (not required, helps debugability)
		},
		DataDir:      "/data",
		ReadyTimeout: 90 * time.Second,
	}
}

// Memgraph returns a ContainerSpec for the given Memgraph image; an empty
// image means the pinned MAGE one.
func Memgraph(image string) ContainerSpec {
	if image == "" {
		image = pinnedImage("memgraph", "memgraph/memgraph-mage:3.10.0")
	}
	return ContainerSpec{
		Image: image,
		Env:   map[string]string{},
		Ports: map[string]string{
			"7687/tcp": "0", // Bolt
		},
		DataDir:      "/var/lib/memgraph",
		ReadyTimeout: 60 * time.Second,
	}
}

// Postgres returns a ContainerSpec for the given PostgreSQL image; an
// empty image means the pinned one. Nothing is configured beyond the
// credentials: the server runs on its own defaults, which is the
// configuration a run without a tuning claim has to measure.
//
// Readiness here is TCP only, and a Postgres container accepts a
// connection some way before it will answer a query, because initdb runs
// a server on a unix socket first and restarts it. The adapter retries
// its first connection rather than trusting this.
func Postgres(image string) ContainerSpec {
	if image == "" {
		image = pinnedImage("postgres", "postgres:18.6")
	}
	return ContainerSpec{
		Image: image,
		Env: map[string]string{
			"POSTGRES_USER":     "bench",
			"POSTGRES_PASSWORD": "bench",
			"POSTGRES_DB":       "bench",
		},
		Ports:   map[string]string{"5432/tcp": "0"},
		Primary: "5432/tcp",
		URI:     "postgres://bench:bench@%s/bench?sslmode=disable",
		// The parent of PGDATA rather than PGDATA itself, because the
		// image moved it under a version directory and both layouts sit
		// below this one.
		DataDir:      "/var/lib/postgresql",
		ReadyTimeout: 90 * time.Second,
	}
}

// Mongo returns a ContainerSpec for the given MongoDB image; an empty
// image means the pinned one. Authentication is off, which is the
// default for a container with no root credentials set, and the server
// otherwise runs on its own defaults.
func Mongo(image string) ContainerSpec {
	if image == "" {
		image = pinnedImage("mongodb", "mongo:8.3.8")
	}
	return ContainerSpec{
		Image:        image,
		Env:          map[string]string{},
		Ports:        map[string]string{"27017/tcp": "0"},
		Primary:      "27017/tcp",
		URI:          "mongodb://%s",
		DataDir:      "/data/db",
		ReadyTimeout: 90 * time.Second,
	}
}

// Start launches a container from spec, waits for it to accept connections, and
// returns a Container with the Bolt URI. The container is stopped and removed
// only when the caller calls Stop.
func Start(ctx context.Context, spec ContainerSpec) (*Container, error) {
	if _, err := exec.LookPath("docker"); err != nil {
		return nil, fmt.Errorf("setup: docker not found in PATH: %w", err)
	}

	args := []string{"run", "--rm", "-d"}
	if spec.Name != "" {
		args = append(args, "--name", spec.Name)
	}
	for k, v := range spec.Env {
		if v != "" {
			args = append(args, "-e", k+"="+v)
		}
	}
	for cport := range spec.Ports {
		// Bind to a free port by mapping container port to host port 0.
		args = append(args, "-p", "0:"+strings.TrimSuffix(cport, "/tcp"))
	}
	args = append(args, spec.Image)

	out, err := exec.CommandContext(ctx, "docker", args...).Output()
	if err != nil {
		return nil, fmt.Errorf("setup: docker run: %w", err)
	}
	id := strings.TrimSpace(string(out))
	if id == "" {
		return nil, fmt.Errorf("setup: docker run returned empty container ID")
	}

	ports, err := inspectPorts(ctx, id)
	if err != nil {
		_ = stopContainer(ctx, id)
		return nil, fmt.Errorf("setup: inspect ports: %w", err)
	}

	c := &Container{ID: id, spec: spec, ports: ports}

	primary, uri := spec.Primary, spec.URI
	if primary == "" {
		primary = "7687/tcp"
	}
	if uri == "" {
		uri = "bolt://%s"
	}
	port, ok := ports[primary]
	if !ok {
		_ = stopContainer(ctx, id)
		return nil, fmt.Errorf("setup: no %s binding for container %s", primary, id[:12])
	}
	c.Addr = "127.0.0.1:" + port
	c.URI = fmt.Sprintf(uri, c.Addr)
	if primary == "7687/tcp" {
		c.BoltURI = c.URI
	}

	readyAddr := spec.ReadyAddr
	if readyAddr == "" {
		readyAddr = c.Addr
	}
	timeout := spec.ReadyTimeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}

	if err := waitReady(ctx, readyAddr, timeout); err != nil {
		_ = stopContainer(ctx, id)
		return nil, fmt.Errorf("setup: container %s not ready after %s: %w", id[:12], timeout, err)
	}

	return c, nil
}

// DataBytes is the size of the server's database directory right now,
// measured with du inside the container, or -1 when the spec named no data
// directory or du would not answer. Call it after the load and again after
// the measured run: the difference is the durable footprint the run added,
// which for a served engine is otherwise unknowable from out here.
//
// The reading is what is on disk at that instant and not what the engine
// will eventually settle at. A server that checkpoints on its own clock has
// work in flight at any moment one asks, so a footprint taken while it runs
// is a lower bound on a write workload and an upper bound on nothing.
func (c *Container) DataBytes(ctx context.Context) int64 {
	if c.spec.DataDir == "" {
		return -1
	}
	// Apparent size, so the number means the same thing as the one the
	// embedded planes report for their own directories by adding up file
	// sizes. A growth figure is only a difference when both ends were
	// measured the same way. An image without GNU coreutils has neither
	// flag, hence the fallback to kilobyte blocks.
	if n := c.du(ctx, "-sb"); n >= 0 {
		return n
	}
	if n := c.du(ctx, "-sk"); n >= 0 {
		return n * 1024
	}
	return -1
}

// du runs one du inside the container and returns the count it printed, or
// -1 when the command failed.
func (c *Container) du(ctx context.Context, flags string) int64 {
	out, err := exec.CommandContext(ctx, "docker", "exec", c.ID, "du", flags, c.spec.DataDir).Output()
	if err != nil {
		return -1
	}
	return parseDU(string(out))
}

// parseDU reads the count off a du -s line, which is a number, a tab and the
// path. Anything else is -1, including a negative number, since a directory
// cannot hold fewer than no bytes and a parse that produces one has read
// something other than du output.
func parseDU(out string) int64 {
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return -1
	}
	n, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || n < 0 {
		return -1
	}
	return n
}

// MemoryBytes is what the container is charged for right now, read out of
// its own cgroup, or -1 when neither cgroup layout answered.
//
// It is the counterpart of a resident set for an engine this process did not
// fork: getrusage has no field for a server in a container, so the only way
// to a peak is to ask on an interval while it runs. The figure includes the
// page cache the container caused, which is the fair comparison against an
// embedded engine, since a resident set counts the file pages that engine
// mapped in the same way.
func (c *Container) MemoryBytes(ctx context.Context) int64 {
	// cgroup v2 first, which is what a current daemon gives a container,
	// then the v1 file for an older host.
	for _, path := range []string{
		"/sys/fs/cgroup/memory.current",
		"/sys/fs/cgroup/memory/memory.usage_in_bytes",
	} {
		out, err := exec.CommandContext(ctx, "docker", "exec", c.ID, "cat", path).Output()
		if err != nil {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
		if err == nil && n >= 0 {
			return n
		}
	}
	return -1
}

// Stop sends a docker stop to the container (run with --rm, so stop also
// removes it). Always call this (via defer) after Start.
func (c *Container) Stop(ctx context.Context) error {
	return stopContainer(ctx, c.ID)
}

// DropCaches issues an OS-level page-cache drop if available (Linux-only).
// On macOS and CI runners without the right privileges it is a no-op; the
// cold protocol in the condition stamp records which drop actually ran.
func DropCaches() {
	// On Linux: echo 3 > /proc/sys/vm/drop_caches requires root.
	// We skip silently rather than failing the harness: the cold-run timing is
	// still the first query after a container restart, which is cold enough for
	// the intended measurement.
	_ = exec.Command("sh", "-c", "echo 3 | sudo tee /proc/sys/vm/drop_caches > /dev/null 2>&1").Run()
}

// inspectPorts runs docker port and parses the output into a map from container
// port (e.g. "7687/tcp") to the local host port. First binding wins when the
// daemon emits both IPv4 and IPv6 lines for the same container port.
func inspectPorts(ctx context.Context, id string) (map[string]string, error) {
	out, err := exec.CommandContext(ctx, "docker", "port", id).Output()
	if err != nil {
		return nil, fmt.Errorf("docker port: %w", err)
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return parsePortLines(lines), nil
}

// parsePortLines parses the lines produced by "docker port <id>" into a map
// from container-port to host-port. Exported for testing.
func parsePortLines(lines []string) map[string]string {
	ports := map[string]string{}
	for _, line := range lines {
		// Format: "7687/tcp -> 0.0.0.0:54321" or "7687/tcp -> :::54321"
		parts := strings.SplitN(line, " -> ", 2)
		if len(parts) != 2 {
			continue
		}
		cport := strings.TrimSpace(parts[0])
		addr := strings.TrimSpace(parts[1])
		_, hostPort, err := net.SplitHostPort(addr)
		if err != nil {
			continue
		}
		if _, exists := ports[cport]; !exists {
			ports[cport] = hostPort
		}
	}
	return ports
}

// waitReady polls addr until it accepts a TCP connection or the deadline passes.
func waitReady(ctx context.Context, addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
		if err == nil {
			conn.Close()
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("timed out waiting for %s", addr)
}

func stopContainer(ctx context.Context, id string) error {
	if err := exec.CommandContext(ctx, "docker", "stop", id).Run(); err != nil {
		return fmt.Errorf("docker stop %s: %w", id[:12], err)
	}
	return nil
}
