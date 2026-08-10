package main

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/tamnd/graph-bench/dataset/ldbc"
)

// newFetchCmd builds the fetch verb: download, verify, and cache a pinned
// LDBC artifact (spec 05 §3). The pin table is committed under
// dataset/ldbc/pins/; fetch never trusts bytes the pin does not name.
func newFetchCmd() *cobra.Command {
	var (
		pinName string
		dsDir   string
	)
	cmd := &cobra.Command{
		Use:   "fetch",
		Short: "Fetch and verify a pinned LDBC dataset artifact",
		Long: "fetch downloads the named pinned artifact (--pin, e.g. snb-sf1), verifies " +
			"the archive checksum before extraction and the content checksum after, and " +
			"caches it under the datasets directory. A cached copy that verifies is " +
			"reused without downloading.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if pinName == "" {
				return fmt.Errorf("fetch: --pin is required (e.g. snb-sf1)")
			}
			scale := strings.TrimPrefix(pinName, "snb-")
			pin, err := ldbc.LoadPin(scale)
			if err != nil {
				return fmt.Errorf("fetch: %w", err)
			}
			dir := datasetsDir(dsDir)
			lastPct := int64(-1)
			ds, err := ldbc.Fetch(cmd.Context(), pin, &ldbc.FetchOptions{
				CacheDir: filepath.Join(dir, "ldbc"),
				Progress: func(done, total int64) {
					if total <= 0 {
						return
					}
					pct := done * 100 / total
					if pct != lastPct && pct%10 == 0 {
						fmt.Fprintf(cmd.ErrOrStderr(), "fetch: %d%%\n", pct)
						lastPct = pct
					}
				},
			})
			if err != nil {
				return fmt.Errorf("fetch: %w", err)
			}
			m := ds.Manifest()
			fmt.Fprintf(cmd.OutOrStdout(), "%s\tnodes=%d\tedges=%d\t%s\n",
				ds.Dir(), m.Invariants.NodeCount, m.Invariants.EdgeCount, ds.Checksum())
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&pinName, "pin", "", "pin name (required), e.g. snb-sf1")
	f.StringVar(&dsDir, "datasets-dir", "", "datasets directory (default $GRAPH_BENCH_DATA or ./datasets)")
	return cmd
}
