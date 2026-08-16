package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/tamnd/graph-bench/engine"
)

// newDoctorCmd builds the doctor verb: it answers "why is this engine
// missing" before a 40-minute run, by probing exactly what Start would —
// environment variables, the zu binary discovery order, Docker, liblbug, the
// Bolt driver, and the pin table (spec 09 §1, §4). Plain table, exit 0
// always: doctor diagnoses, it never gates.
func newDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Probe engines, pins, Docker, the zu binary, and liblbug",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			w := cmd.OutOrStdout()
			doctorEnv(w)
			fmt.Fprintln(w)
			doctorZu(cmd.Context(), w)
			fmt.Fprintln(w)
			doctorDocker(cmd.Context(), w)
			fmt.Fprintln(w)
			doctorLadybug(w)
			fmt.Fprintln(w)
			doctorLibzu(w)
			fmt.Fprintln(w)
			doctorDriver(w)
			fmt.Fprintln(w)
			doctorPins(w)
			fmt.Fprintln(w)
			doctorEngines(w)
			return nil
		},
	}
	return cmd
}

// doctorEnv echoes every environment variable the harness reads (spec 09 §4).
func doctorEnv(w io.Writer) {
	fmt.Fprintln(w, "environment:")
	vars := []string{
		"ZU_BIN",
		"NEO4J_URI", "NEO4J_USER", "NEO4J_PASS",
		"MEMGRAPH_URI",
		"LBUG_INCLUDE", "LBUG_LIB",
		"ZU_INCLUDE", "ZU_LIB",
		"GRAPH_BENCH_DATA",
		"GRAPH_BENCH_SKIP_DOCKER",
	}
	for _, v := range vars {
		val, ok := os.LookupEnv(v)
		switch {
		case !ok:
			val = "(unset)"
		case v == "NEO4J_PASS" && val != "":
			val = "(set, hidden)"
		}
		fmt.Fprintf(w, "  %-24s  %s\n", v, val)
	}
}

// doctorZu walks the zu binary discovery order (spec 04 §2) the same way the
// adapter does and reports the first hit plus its version.
func doctorZu(ctx context.Context, w io.Writer) {
	fmt.Fprintln(w, "zu binary:")
	candidates := []struct{ desc, path string }{
		{"$ZU_BIN", os.Getenv("ZU_BIN")},
		{"PATH", lookPathOr("zu")},
		{"../zu/target/release/zu", "../zu/target/release/zu"},
		{"../zu/target/debug/zu", "../zu/target/debug/zu"},
	}
	for _, c := range candidates {
		if c.path == "" {
			fmt.Fprintf(w, "  %-26s  (unset)\n", c.desc)
			continue
		}
		abs, _ := filepath.Abs(c.path)
		if st, err := os.Stat(c.path); err != nil || st.IsDir() {
			fmt.Fprintf(w, "  %-26s  not found (%s)\n", c.desc, abs)
			continue
		}
		fmt.Fprintf(w, "  %-26s  FOUND %s  version=%s\n", c.desc, abs, probeVersion(ctx, c.path))
		return
	}
	fmt.Fprintln(w, "  => no zu binary; build zu or set ZU_BIN")
}

func lookPathOr(name string) string {
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	return p
}

// probeVersion runs `bin --version` with a short deadline.
func probeVersion(ctx context.Context, bin string) string {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, bin, "--version").Output()
	if err != nil {
		return "(unknown)"
	}
	return strings.TrimSpace(string(out))
}

// doctorDocker probes the Docker client and daemon: the Bolt tier needs both.
func doctorDocker(ctx context.Context, w io.Writer) {
	fmt.Fprintln(w, "docker:")
	bin := lookPathOr("docker")
	if bin == "" {
		fmt.Fprintln(w, "  client                    not on PATH (managed containers unavailable)")
		return
	}
	fmt.Fprintf(w, "  client                    %s\n", bin)
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, bin, "version", "--format", "{{.Server.Version}}").Output()
	if err != nil {
		fmt.Fprintln(w, "  daemon                    not reachable")
		return
	}
	fmt.Fprintf(w, "  daemon                    %s\n", strings.TrimSpace(string(out)))
}

// doctorLadybug looks for the liblbug shared library the ladybug build tag
// links against ($LBUG_LIB first, then the usual prefixes).
func doctorLadybug(w io.Writer) {
	fmt.Fprintln(w, "liblbug:")
	var dirs []string
	if d := os.Getenv("LBUG_LIB"); d != "" {
		dirs = append(dirs, searchDir(d))
	}
	dirs = append(dirs, "/usr/local/lib", "/opt/homebrew/lib", "/usr/lib")
	for _, dir := range dirs {
		for _, name := range []string{"liblbug.dylib", "liblbug.so"} {
			p := filepath.Join(dir, name)
			if _, err := os.Stat(p); err == nil {
				fmt.Fprintf(w, "  %s\n", p)
				return
			}
		}
	}
	fmt.Fprintln(w, "  not found (ladybug build tag will not link)")
}

// doctorLibzu looks for the libzu shared library and header the zuinproc
// build tag links against ($ZU_LIB / $ZU_INCLUDE first, then the sibling
// zu checkout the #cgo lines default to).
func doctorLibzu(w io.Writer) {
	fmt.Fprintln(w, "libzu:")
	libDirs := []string{"../zu/target/release", "../zu/target/debug"}
	if d := os.Getenv("ZU_LIB"); d != "" {
		libDirs = append([]string{searchDir(d)}, libDirs...)
	}
	lib := findFirst(libDirs, []string{"libzu.dylib", "libzu.so"})
	if lib == "" {
		fmt.Fprintln(w, "  library                   not found (zuinproc build tag will not link)")
	} else {
		fmt.Fprintf(w, "  library                   %s\n", lib)
	}

	incDirs := []string{"../zu/crates/zu-capi/include"}
	if d := os.Getenv("ZU_INCLUDE"); d != "" {
		incDirs = append([]string{searchDir(d)}, incDirs...)
	}
	hdr := findFirst(incDirs, []string{"zu.h"})
	if hdr == "" {
		fmt.Fprintln(w, "  header                    not found (build it: cargo build --release -p zu-capi)")
		return
	}
	fmt.Fprintf(w, "  header                    %s\n", hdr)
}

// searchDir turns a path from the environment into a directory to search. The
// cgo lines want $ZU_LIB and friends to name a directory, because they go
// straight into -L and -I, but a reader with the library in front of them
// reaches for its full path, and being wrong about that used to look like the
// variable had been ignored: doctor would report the sibling checkout while
// the environment pointed somewhere else entirely.
func searchDir(p string) string {
	if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
		return filepath.Dir(p)
	}
	return p
}

// findFirst returns the first dir/name pair that exists, or "".
func findFirst(dirs, names []string) string {
	for _, dir := range dirs {
		for _, name := range names {
			p := filepath.Join(dir, name)
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return ""
}

// doctorDriver reports the Bolt driver version compiled into this binary,
// read from build info — never hand-entered.
func doctorDriver(w io.Writer) {
	fmt.Fprintln(w, "bolt driver:")
	info, ok := debug.ReadBuildInfo()
	if !ok {
		fmt.Fprintln(w, "  (no build info)")
		return
	}
	for _, dep := range info.Deps {
		if strings.Contains(dep.Path, "neo4j-go-driver") {
			fmt.Fprintf(w, "  %s %s\n", dep.Path, dep.Version)
			return
		}
	}
	fmt.Fprintln(w, "  not linked (built without -tags bolt)")
}

// doctorPins prints the single pin table (engine/pins.go) — the versions the
// suite is validated against. Live versions come from Session.Version at run
// time; a mismatch is surfaced, never silently accepted.
func doctorPins(w io.Writer) {
	fmt.Fprintln(w, "pins (validated-against versions):")
	for _, p := range engine.Pins {
		fmt.Fprintf(w, "  %-10s  %-22s  %s\n", p.Engine, p.Pinned, p.Source)
	}
}

// doctorEngines lists what this binary registered — the difference between
// the catalog and this list is a build-tag question, answered above.
func doctorEngines(w io.Writer) {
	fmt.Fprintln(w, "registered engines (this binary):")
	for _, e := range engine.All() {
		info := e.Info()
		fmt.Fprintf(w, "  %-10s  plane=%s\n", info.Name, info.Plane)
	}
}
