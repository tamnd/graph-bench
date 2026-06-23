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
	"github.com/tamnd/graph-bench/workload"
	_ "github.com/tamnd/graph-bench/workload/lsqb"
	_ "github.com/tamnd/graph-bench/workload/micro"
	_ "github.com/tamnd/graph-bench/workload/snb"
)

// newGenerateCmd builds the generate verb: it materializes a synthetic dataset
// to disk in the canonical CSV layout, deterministically from a seed, and prints
// the resulting directory, counts, and checksum. The LDBC fetch-and-verify path
// (--ldbc) lands with the LDBC dataset milestone; this slice ships the five
// synthetic generators. See spec doc 04 section 7 for the verb's contract.
func newGenerateCmd() *cobra.Command {
	var (
		out       string
		cfg       gen.Config
		initiator []float64
	)

	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate a deterministic synthetic dataset in the canonical layout",
		Long: "generate materializes a synthetic graph (uniform, powerlaw, er, grid, or rmat) " +
			"to disk in the canonical CSV layout, with a manifest and a content checksum. " +
			"The same generator, seed, and parameters reproduce byte-identical files on any machine.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if cfg.Kind == "" {
				return fmt.Errorf("generate: --gen is required (one of uniform, powerlaw, er, grid, rmat)")
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
	f.StringVar(&cfg.Kind, "gen", "", "generator: uniform, powerlaw, er, grid, or rmat")
	f.Int64Var(&cfg.Seed, "seed", 0, "PRNG seed; the only source of randomness")
	f.Int64Var(&cfg.N, "n", 0, "node count (uniform, powerlaw, er)")
	f.IntVar(&cfg.Degree, "degree", 0, "out-degree per node (uniform)")
	f.Float64Var(&cfg.Gamma, "gamma", 0, "power-law exponent (powerlaw)")
	f.IntVar(&cfg.MinDeg, "min-deg", 1, "minimum degree (powerlaw)")
	f.IntVar(&cfg.MaxDeg, "max-deg", 0, "maximum degree (powerlaw)")
	f.Float64Var(&cfg.P, "p", 0, "edge probability (er)")
	f.IntVar(&cfg.Rows, "rows", 0, "grid rows (grid)")
	f.IntVar(&cfg.Cols, "cols", 0, "grid columns (grid)")
	f.BoolVar(&cfg.Diagonal, "diagonal", false, "8-neighbor grid instead of 4-neighbor (grid)")
	f.IntVar(&cfg.Scale, "scale", 0, "log2 of the node count; N = 2^scale (rmat)")
	f.IntVar(&cfg.EdgeFactor, "edge-factor", 0, "edges per node (rmat)")
	f.Float64SliceVar(&initiator, "initiator", nil, "RMAT initiator A,B,C,D (rmat); default is the Graph500 values")
	f.BoolVar(&cfg.ComputeInvariants, "compute-invariants", false, "compute optional ground-truth invariants")

	cmd.AddCommand(newGeneratePinCmd())
	cmd.AddCommand(newGenerateCurateCmd())
	return cmd
}

// newGeneratePinCmd builds the generate pin subcommand. It computes a pin JSON
// from a locally downloaded .tar.zst archive: hash the archive, extract it,
// read the manifest, compute the content checksum, and write the pin file.
// Run this once after downloading a new LDBC dataset; commit the result to
// dataset/ldbc/pins/.
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
		Long: "generate pin hashes a local LDBC .tar.zst archive, extracts it to a temp\n" +
			"directory, reads the dataset manifest, and writes a pin JSON file with the\n" +
			"archive checksum and content checksum filled in. Commit the result to\n" +
			"dataset/ldbc/pins/<name>.json.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if archive == "" {
				return fmt.Errorf("generate pin: --archive is required")
			}
			if name == "" {
				return fmt.Errorf("generate pin: --name is required (e.g. snb-sf1)")
			}
			if scale == "" {
				return fmt.Errorf("generate pin: --scale is required (e.g. SF1)")
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
	f.StringVar(&name, "name", "", "pin name, e.g. snb-sf1 (becomes the JSON filename)")
	f.StringVar(&scale, "scale", "", "LDBC scale label, e.g. SF1")
	f.StringVar(&url, "url", "", "primary download URL (stored in the pin, not downloaded)")
	f.StringVar(&mirror, "mirror", "", "fallback download URL")
	f.StringVar(&out, "out", "-", "output path for the pin JSON (- prints to stdout)")
	return cmd
}

// newGenerateCurateCmd builds the generate curate subcommand. It pre-computes
// the curated parameter pools for a dataset (params.json beside manifest.json)
// so the run command does not have to do it on the first run against each
// dataset. Curation is idempotent; running it again with the same seed is safe.
func newGenerateCurateCmd() *cobra.Command {
	var (
		dsPath string
		seed   int64
	)
	cmd := &cobra.Command{
		Use:   "curate",
		Short: "Pre-compute curated parameter pools for a dataset",
		Long: "generate curate reads a materialized dataset directory and writes\n" +
			"params.json beside manifest.json with curated parameter pools for\n" +
			"every registered workload family. Curation is idempotent; repeating\n" +
			"it with the same seed is safe and produces the same output.\n" +
			"The run command curates automatically, but running curate explicitly\n" +
			"on large datasets avoids the one-time cost on the first benchmark run.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if dsPath == "" {
				return fmt.Errorf("generate curate: --dataset-path is required")
			}
			ds, err := dataset.Open(dsPath)
			if err != nil {
				return fmt.Errorf("generate curate: open dataset %s: %w", dsPath, err)
			}
			if err := workload.Curate(ds, seed); err != nil {
				return fmt.Errorf("generate curate: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "curated %s (seed=%d)\n", dsPath, seed)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&dsPath, "dataset-path", "", "path to a materialized dataset directory (required)")
	f.Int64Var(&seed, "seed", 1, "PRNG seed for sampling; use the same seed as --curate-seed in run")
	return cmd
}

// runGenerate generates the dataset into a staging directory, names the final
// directory from the manifest checksum, and either keeps an existing identical
// dataset (a cache hit) or moves the staging directory into place. It prints a
// one-line summary either way.
func runGenerate(cmd *cobra.Command, out string, cfg gen.Config) error {
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}
	stage, err := os.MkdirTemp(out, ".gen-")
	if err != nil {
		return err
	}
	// Clean up the staging directory unless it is moved into place below.
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
		// A dataset with this identity already exists; the recipe is the same, so
		// the bytes are the same. Reuse it rather than overwrite.
		fmt.Fprintf(cmd.OutOrStdout(), "%s\tnodes=%d\tedges=%d\t%s\t(cached)\n",
			final, m.NodeCount, m.EdgeCount, m.Checksum)
		return nil
	}
	if err := os.Rename(stage, final); err != nil {
		return err
	}
	keep = true
	fmt.Fprintf(cmd.OutOrStdout(), "%s\tnodes=%d\tedges=%d\t%s\n",
		final, m.NodeCount, m.EdgeCount, m.Checksum)
	return nil
}
