// Command graph-bench is the front end to the graph-bench suite: it generates
// datasets, fetches pinned LDBC artifacts, runs workloads against one or more
// graph engines, renders and compares the results, and gates them in CI.
// The command surface is specified in notes/Spec/2064g/bench/09-cli-reporting-ci.md.
package main

import (
	"context"
	"errors"
	"os"
	"os/signal"

	"github.com/charmbracelet/fang"
	"github.com/spf13/cobra"
)

// Build metadata. version is the release identity stamped into every
// Condition; commit and date are injected via -ldflags at release time.
var (
	version = "0.3.0"
	commit  = "none"
	date    = "unknown"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	root := newRootCmd()
	err := fang.Execute(ctx, root, fang.WithVersion(version))
	os.Exit(exitCode(err))
}

// newRootCmd builds the command tree. It is a function so tests can execute
// the CLI in process without going through main.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "graph-bench",
		Short: "Fair, reproducible cross-engine benchmark for graph databases",
		Long: "graph-bench measures graph databases against each other on the same data, " +
			"the same queries, and the same machine, and reports the result without spin. " +
			"It treats zu as one target among many, held to the same rules as every other engine.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(
		newRunCmd(),
		newGenerateCmd(),
		newFetchCmd(),
		newCurateCmd(),
		newListCmd(),
		newReportCmd(),
		newCompareCmd(),
		newGateCmd(),
		newNoiseCmd(),
		newABCmd(),
		newDoctorCmd(),
	)
	return root
}

// exitCode maps an error to a process exit code. Commands may attach a
// specific code via the ExitCode interface (the gate verb's 2 and 3 are API);
// everything else is a generic failure.
func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var coder interface{ ExitCode() int }
	if errors.As(err, &coder) {
		return coder.ExitCode()
	}
	return 1
}
