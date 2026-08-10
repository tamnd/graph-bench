package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/tamnd/graph-bench/dataset"
	"github.com/tamnd/graph-bench/dataset/gen"
	"github.com/tamnd/graph-bench/dataset/ldbc"
)

// newGenerateCmd builds the generate verb: it materializes a synthetic
// dataset to disk in the canonical CSV layout, deterministically from a seed,
// and prints the resulting directory, counts, and checksum. Ported from v1
// and extended with the v0.3 generators (urand, fin, lb, social).
func newGenerateCmd() *cobra.Command {
	var (
		out       string
		recipe    string
		cfg       gen.Config
		initiator []float64
	)

	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate a deterministic synthetic dataset in the canonical layout",
		Long: "generate materializes a synthetic graph to disk in the canonical CSV layout, " +
			"with a manifest and a content checksum. The same generator, seed, and parameters " +
			"reproduce byte-identical files on any machine. Either name a recipe from the " +
			"table (--recipe, see 'list datasets') or configure a generator directly (--kind).",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if recipe != "" {
				rcfg, ok := recipes[recipe]
				if !ok {
					return fmt.Errorf("generate: unknown recipe %q; see 'graph-bench list datasets'", recipe)
				}
				ds, err := materializeRecipe(cmd.Context(), recipe, rcfg, out)
				if err != nil {
					return err
				}
				m := ds.Manifest()
				fmt.Fprintf(cmd.OutOrStdout(), "%s\tnodes=%d\tedges=%d\t%s\n",
					ds.Dir(), m.Invariants.NodeCount, m.Invariants.EdgeCount, m.Checksum)
				return nil
			}
			if cfg.Kind == "" {
				return fmt.Errorf("generate: --kind is required (uniform, powerlaw, er, grid, rmat, urand, fin, lb, social) unless --recipe is given")
			}
			if len(initiator) == 4 {
				cfg.Initiator = [4]float64{initiator[0], initiator[1], initiator[2], initiator[3]}
			} else if len(initiator) != 0 {
				return fmt.Errorf("generate: --initiator takes exactly 4 values, got %d", len(initiator))
			}
			return runGenerate(cmd, out, cfg)
		},
	}

	f := cmd.Flags()
	f.StringVar(&out, "out", "datasets", "directory the dataset is written under")
	f.StringVar(&recipe, "recipe", "", "named recipe from the table (e.g. rmat-14, fin-10k)")
	f.StringVar(&cfg.Kind, "kind", "", "generator: uniform, powerlaw, er, grid, rmat, urand, fin, lb, or social")
	f.Int64Var(&cfg.Seed, "seed", 0, "PRNG seed; the only source of randomness")
	f.Int64Var(&cfg.N, "n", 0, "node count (uniform, powerlaw, er, lb)")
	f.IntVar(&cfg.Degree, "degree", 0, "out-degree per node (uniform)")
	f.Float64Var(&cfg.Gamma, "gamma", 0, "power-law exponent (powerlaw)")
	f.IntVar(&cfg.MinDeg, "min-deg", 1, "minimum degree (powerlaw)")
	f.IntVar(&cfg.MaxDeg, "max-deg", 0, "maximum degree (powerlaw)")
	f.Float64Var(&cfg.P, "p", 0, "edge probability (er)")
	f.IntVar(&cfg.Rows, "rows", 0, "grid rows (grid)")
	f.IntVar(&cfg.Cols, "cols", 0, "grid columns (grid)")
	f.BoolVar(&cfg.Diagonal, "diagonal", false, "8-neighbor grid instead of 4-neighbor (grid)")
	f.IntVar(&cfg.Scale, "scale", 0, "log2 of the node count; N = 2^scale (rmat, urand)")
	f.IntVar(&cfg.EdgeFactor, "edge-factor", 0, "edges per node (rmat, urand)")
	f.Float64SliceVar(&initiator, "initiator", nil, "RMAT initiator A,B,C,D; default is the Graph500 values")
	f.BoolVar(&cfg.Weighted, "weighted", false, "add the GAP int64 weight property w (rmat)")
	f.Int64Var(&cfg.Accounts, "accounts", 0, "account count (fin)")
	f.IntVar(&cfg.Days, "days", 0, "simulation window in days (fin)")
	f.IntVar(&cfg.TxPerDay, "tx-per-day", 0, "transfers per simulated day (fin)")
	f.Float64Var(&cfg.HubFrac, "hub-frac", 0, "hub account fraction (fin)")
	f.IntVar(&cfg.Persons, "persons", 0, "person count (social)")
	f.IntVar(&cfg.AvgFriends, "avg-friends", 0, "approximate mean KNOWS degree (social)")
	f.IntVar(&cfg.PostsPerPerson, "posts-per-person", 0, "posts authored per person (social)")
	f.BoolVar(&cfg.ComputeInvariants, "compute-invariants", false, "compute optional ground-truth invariants")

	cmd.AddCommand(newGeneratePinCmd())
	return cmd
}

// runGenerate generates the dataset into a staging directory, names the final
// directory from the manifest checksum, and either keeps an existing identical
// dataset (a cache hit) or moves the staging directory into place.
func runGenerate(cmd *cobra.Command, out string, cfg gen.Config) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(out, ".gen-")
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		if !keep {
			os.RemoveAll(stage)
		}
	}()

	w, err := dataset.NewWriter(stage)
	if err != nil {
		return err
	}
	m, err := gen.Generate(cmd.Context(), cfg, w)
	if err != nil {
		return err
	}

	final := filepath.Join(out, dataset.DirName(m))
	if _, statErr := os.Stat(final); statErr == nil {
		fmt.Fprintf(cmd.OutOrStdout(), "%s\tnodes=%d\tedges=%d\t%s\t(cached)\n",
			final, m.Invariants.NodeCount, m.Invariants.EdgeCount, m.Checksum)
		return nil
	}
	if err := os.Rename(stage, final); err != nil {
		return err
	}
	keep = true
	fmt.Fprintf(cmd.OutOrStdout(), "%s\tnodes=%d\tedges=%d\t%s\n",
		final, m.Invariants.NodeCount, m.Invariants.EdgeCount, m.Checksum)
	return nil
}

// newGeneratePinCmd builds the generate pin subcommand: it computes a pin
// JSON from a locally downloaded .tar.zst archive. Run once after downloading
// a new LDBC dataset; commit the result to dataset/ldbc/pins/.
func newGeneratePinCmd() *cobra.Command {
	var (
		archive string
		name    string
		scale   string
		url     string
		mirror  string
		out     string
	)
	cmd := &cobra.Command{
		Use:   "pin",
		Short: "Compute a pin JSON from a local .tar.zst LDBC archive",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if archive == "" || name == "" || scale == "" {
				return fmt.Errorf("generate pin: --archive, --name, and --scale are required")
			}
			pin, err := ldbc.ComputePin(cmd.Context(), archive, name, scale, url, mirror)
			if err != nil {
				return err
			}
			data, err := json.MarshalIndent(pin, "", "  ")
			if err != nil {
				return err
			}
			if out == "" || out == "-" {
				fmt.Fprintf(cmd.OutOrStdout(), "%s\n", data)
				return nil
			}
			if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(out, append(data, '\n'), 0o644); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", out)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&archive, "archive", "", "path to the .tar.zst archive to hash and inspect")
	f.StringVar(&name, "name", "", "pin name, e.g. snb-sf1")
	f.StringVar(&scale, "scale", "", "LDBC scale label, e.g. SF1")
	f.StringVar(&url, "url", "", "primary download URL (stored in the pin, not downloaded)")
	f.StringVar(&mirror, "mirror", "", "fallback download URL")
	f.StringVar(&out, "out", "-", "output path for the pin JSON (- prints to stdout)")
	return cmd
}
