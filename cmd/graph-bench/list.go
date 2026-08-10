package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tamnd/graph-bench/engine"
	"github.com/tamnd/graph-bench/workload"
)

// newListCmd builds the list verb: it prints what the registries know —
// workloads, engines (with dialect chains and capabilities), dataset recipes,
// and pins — without starting any engine or touching any dataset.
func newListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list [workloads|engines|datasets|pins]",
		Short: "List registered workloads, engines, dataset recipes, or pins",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			what := "workloads"
			if len(args) > 0 {
				what = args[0]
			}
			switch what {
			case "workloads":
				return listWorkloads(cmd)
			case "engines":
				return listEngines(cmd)
			case "datasets":
				return listDatasets(cmd)
			case "pins":
				return listPins(cmd)
			default:
				return fmt.Errorf("list: unknown subject %q; use workloads, engines, datasets, or pins", what)
			}
		},
	}
	return cmd
}

func listWorkloads(cmd *cobra.Command) error {
	all := workload.All()
	w := cmd.OutOrStdout()
	if len(all) == 0 {
		fmt.Fprintln(w, "no workloads registered")
		return nil
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Name < all[j].Name })
	fmt.Fprintf(w, "%-16s  %-10s  %-14s  %-7s  %-5s  %-15s  %s\n",
		"workload", "family", "dataset", "queries", "mix", "fidelity", "title")
	for _, wl := range all {
		mix := "no"
		if wl.Mix != nil {
			mix = fmt.Sprintf("%d", len(wl.Mix.Weights))
		}
		if wl.Analytics {
			mix = "anlyt"
		}
		fmt.Fprintf(w, "%-16s  %-10s  %-14s  %-7d  %-5s  %-15s  %s\n",
			wl.Name, wl.Family, wl.Dataset, len(wl.Queries), mix, wl.Fidelity, wl.Title)
	}
	return nil
}

func listEngines(cmd *cobra.Command) error {
	w := cmd.OutOrStdout()
	all := engine.All()
	if len(all) == 0 {
		fmt.Fprintln(w, "no engine adapters registered")
		return nil
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Info().Name < all[j].Info().Name })
	fmt.Fprintf(w, "%-10s  %-11s  %-16s  %s\n", "engine", "plane", "dialects", "capabilities")
	for _, e := range all {
		info := e.Info()
		dialects := make([]string, 0, len(info.Dialects))
		for _, d := range info.Dialects {
			dialects = append(dialects, string(d))
		}
		fmt.Fprintf(w, "%-10s  %-11s  %-16s  %s\n",
			info.Name, info.Plane, strings.Join(dialects, ","), capsSummary(info.Caps))
	}
	return nil
}

// capsSummary renders a Capabilities struct as a compact honest-facts cell.
func capsSummary(c engine.Capabilities) string {
	var parts []string
	add := func(ok bool, name string) {
		if ok {
			parts = append(parts, name)
		}
	}
	add(c.Transactions, "tx")
	add(c.BulkLoad, "bulk")
	add(c.Deletes, "del")
	add(c.VarLengthPaths, "varlen")
	add(c.ShortestPaths, "sp")
	add(c.Persistent, "persist")
	if len(c.Algorithms) > 0 {
		parts = append(parts, "algos:"+strings.Join(c.Algorithms, "+"))
	}
	if c.MaxConcurrency > 0 {
		parts = append(parts, fmt.Sprintf("conc<=%d", c.MaxConcurrency))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " ")
}

func listDatasets(cmd *cobra.Command) error {
	w := cmd.OutOrStdout()
	names := make([]string, 0, len(recipes))
	for n := range recipes {
		names = append(names, n)
	}
	sort.Strings(names)
	fmt.Fprintf(w, "%-14s  %-10s  %-14s\n", "recipe", "kind", "smoke-variant")
	for _, n := range names {
		fmt.Fprintf(w, "%-14s  %-10s  %-14s\n", n, recipes[n].Kind, smokeVariant[n])
	}
	pinList := make([]string, 0, len(pinNames))
	for n := range pinNames {
		pinList = append(pinList, n)
	}
	sort.Strings(pinList)
	for _, n := range pinList {
		fmt.Fprintf(w, "%-14s  %-10s  %-14s\n", n, "ldbc-pin", smokeVariant[n])
	}
	return nil
}

func listPins(cmd *cobra.Command) error {
	w := cmd.OutOrStdout()
	fmt.Fprintf(w, "%-10s  %-22s  %s\n", "engine", "pinned", "source")
	for _, p := range engine.Pins {
		fmt.Fprintf(w, "%-10s  %-22s  %s\n", p.Engine, p.Pinned, p.Source)
	}
	return nil
}
