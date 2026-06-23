package main

import (
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	"github.com/tamnd/graph-bench/workload"
)

// newListCmd builds the list verb: it prints the registered workloads and the
// known engines without touching any dataset or engine.
func newListCmd() *cobra.Command {
	var listWhat string

	cmd := &cobra.Command{
		Use:   "list [workloads|engines]",
		Short: "List registered workloads or known engines",
		Long: "list prints what the registry knows. " +
			"'list workloads' shows every registered workload, its query count, and its Mix (if any). " +
			"'list engines' shows the engine names the adapters provide. " +
			"No engines are started and no datasets are touched.",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				listWhat = args[0]
			}
			switch listWhat {
			case "", "workloads":
				return listWorkloads(cmd)
			case "engines":
				return listEngines(cmd)
			default:
				return fmt.Errorf("list: unknown subject %q; use 'workloads' or 'engines'", listWhat)
			}
		},
	}
	return cmd
}

func listWorkloads(cmd *cobra.Command) error {
	all := workload.All()
	if len(all) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "no workloads registered")
		return nil
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "%-20s  %-8s  %-8s  %s\n", "workload", "queries", "mix", "title")
	fmt.Fprintf(w, "%-20s  %-8s  %-8s  %s\n", "--------", "-------", "---", "-----")
	for _, wl := range all {
		mix := "no"
		if len(wl.Mix) > 0 {
			mix = fmt.Sprintf("%d", len(wl.Mix))
		}
		fmt.Fprintf(w, "%-20s  %-8d  %-8s  %s\n", wl.Name, len(wl.Queries), mix, wl.Title)
	}
	return nil
}

func listEngines(cmd *cobra.Command) error {
	// The known engines are the in-process adapters plus the Bolt adapters (if
	// built with -tags bolt). We print a static list; at run time the adapters
	// are resolved by name from the target registry.
	engines := []struct {
		name  string
		plane string
		tag   string
	}{
		{"gr", "inproc", "always"},
		{"gr-bolt", "bolt", "-tags bolt"},
		{"neo4j", "bolt", "-tags bolt"},
		{"memgraph", "bolt", "-tags bolt"},
	}
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "%-12s  %-8s  %s\n", "engine", "plane", "build tag")
	fmt.Fprintf(w, "%-12s  %-8s  %s\n", "------", "-----", "---------")
	for _, e := range engines {
		fmt.Fprintf(w, "%-12s  %-8s  %s\n", e.name, e.plane, e.tag)
	}
	return nil
}
