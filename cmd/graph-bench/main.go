// Command graph-bench is the front end to the graph-bench suite: it generates
// datasets, runs workloads against one or more graph databases, compares the
// results, and gates them in CI. See the spec at notes/Spec/2060/bench for the
// full design. The verbs are stubs in this scaffold; each lands in its own slice.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"github.com/charmbracelet/fang"
	"github.com/spf13/cobra"
)

// Build metadata, injected via -ldflags at release time.
var (
	version = "dev"
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

// newRootCmd builds the command tree. It is a function so tests can execute the
// CLI in process without going through main.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "graph-bench",
		Short: "Fair, reproducible cross-engine benchmark for graph databases",
		Long: "graph-bench measures graph databases against each other on the same data, " +
			"the same queries, and the same machine, and reports the result without spin. " +
			"It treats gr as one target among many, held to the same rules as every other engine.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(
		newGenerateCmd(),
		newRunCmd(),
		newCompareCmd(),
		newReportCmd(),
		newGateCmd(),
	)
	return root
}

func newRunCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "run",
		Short: "Run a workload against one or more engines and record the measurement",
		RunE:  notImplemented("run"),
	}
}

func newCompareCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "compare",
		Short: "Compare recorded runs as a per-engine matrix",
		RunE:  notImplemented("compare"),
	}
}

func newReportCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "report",
		Short: "Render a recorded run or comparison in a chosen format",
		RunE:  notImplemented("report"),
	}
}

func newGateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "gate",
		Short: "Check a run against its budgets and a stored baseline, for CI",
		RunE:  notImplemented("gate"),
	}
}

// notImplemented returns a RunE that reports the verb is not wired up yet. The
// scaffold ships the command tree; each verb arrives with its own slice.
func notImplemented(verb string) func(*cobra.Command, []string) error {
	return func(cmd *cobra.Command, _ []string) error {
		return fmt.Errorf("%s: not implemented yet in this build", verb)
	}
}

// exitCode maps an error to a process exit code. Commands may attach a specific
// code via the ExitCode interface; everything else is a generic failure.
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
