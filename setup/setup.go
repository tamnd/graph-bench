// Package setup starts and stops container-hosted graph engines for the Bolt
// plane. It is the only package that calls Docker (through the docker CLI or
// daemon API). The rest of the harness speaks Target/Driver; setup is how a
// Target gets its server.
//
// Usage pattern:
//
//	c, err := setup.Start(ctx, setup.Neo4j("neo4j:5.26-community"))
//	if err != nil { ... }
//	defer c.Stop(ctx)
//	// c.BoltURI is ready for the adapter's Setup call
//
// See notes/Spec/2060/bench/02-architecture.md section 2.8 for the contract.
package setup

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"time"
)

// ContainerSpec describes one container to launch.
type ContainerSpec struct {
	// Image is the Docker image reference, e.g. "neo4j:5.26-community".
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
}

// Container is a running container returned by Start.
type Container struct {
	ID      string // Docker container ID
	BoltURI string // bolt://host:port, ready to dial
	spec    ContainerSpec
	ports   map[string]string // container port -> local port
}

// Neo4j returns a ContainerSpec for the given Neo4j image.
// Pass the full image tag, e.g. "neo4j:5.26-community".
func Neo4j(image string) ContainerSpec {
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
		ReadyTimeout: 90 * time.Second,
	}
}

// Memgraph returns a ContainerSpec for the given Memgraph image.
// Pass the full image tag, e.g. "memgraph/memgraph:2.19.0".
func Memgraph(image string) ContainerSpec {
	return ContainerSpec{
		Image: image,
		Env:   map[string]string{},
		Ports: map[string]string{
			"7687/tcp": "0", // Bolt
		},
		ReadyTimeout: 60 * time.Second,
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

	boltPort, ok := ports["7687/tcp"]
	if !ok {
		_ = stopContainer(ctx, id)
		return nil, fmt.Errorf("setup: no 7687/tcp binding for container %s", id[:12])
	}
	c.BoltURI = "bolt://127.0.0.1:" + boltPort

	readyAddr := spec.ReadyAddr
	if readyAddr == "" {
		readyAddr = "127.0.0.1:" + boltPort
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

// Stop sends a docker stop + rm to the container. Always call this (via defer)
// after Start.
func (c *Container) Stop(ctx context.Context) error {
	return stopContainer(ctx, c.ID)
}

// DropCaches issues an OS-level page-cache drop if available (Linux-only).
// On macOS and CI runners without the right privileges it is a no-op.
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
