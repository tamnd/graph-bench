package gr

import (
	"context"
	"fmt"
	"os"
	"sort"
	"time"

	grdb "github.com/tamnd/gr"
	"github.com/tamnd/gr/loader"
	"github.com/tamnd/graph-bench/target"
)

// loadCSV ingests a file-backed dataset through gr's four-pass bulk loader. The
// loader builds a fresh gr database file from the canonical CSV files, so the
// flow is: close the empty database opened in Setup, remove its file and
// sidecars, run the loader to build the database at the same path from the
// dataset's node and relationship files, then reopen and swap the driver's
// handle. This is the gr library bulk path the spec's section 6.2 describes; the
// canonical headers are exactly what the loader parses, so there is no
// translation.
//
// The loader writes a real file, so a bulk load needs a disk-backed target. An
// in-memory gr target (the default for the unit tests) cannot take this path and
// returns a clear error pointing at the path configuration.
func (t *Target) loadCSV(ctx context.Context, drv *driver, ds target.Dataset) (target.LoadStats, error) {
	if drv.mem || drv.path == "" {
		return target.LoadStats{}, fmt.Errorf("gr: bulk CSV load needs a disk path; set Config.Values[\"path\"] for dataset %q", ds.Name())
	}

	nodeSrcs, err := nodeSources(ds)
	if err != nil {
		return target.LoadStats{}, err
	}
	relSrcs, err := relSources(ds)
	if err != nil {
		return target.LoadStats{}, err
	}

	// The loader builds the file from scratch; close and remove the empty
	// database (and its -wal/-shm sidecars) that Setup created at this path.
	if err := drv.db.Close(); err != nil {
		return target.LoadStats{}, fmt.Errorf("gr: close before bulk load: %w", err)
	}
	drv.db = nil
	for _, p := range []string{drv.path, drv.path + "-wal", drv.path + "-shm"} {
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return target.LoadStats{}, fmt.Errorf("gr: clear %s before bulk load: %w", p, err)
		}
	}

	start := time.Now()
	l := loader.New(loader.Options{
		Nodes:         nodeSrcs,
		Relationships: relSrcs,
		// The canonical layout fixes the list delimiter at ';' (spec doc 04
		// section 1.4); the field separator is the default comma.
		ArrayDelim: ';',
		// A dangling relationship endpoint is dropped rather than failing the
		// whole load, matching the loader's own default and the RMAT generator's
		// kept-duplicates policy where an endpoint may be an isolated id.
		OnDangling: loader.Skip,
	})
	if err := l.Run(drv.path); err != nil {
		return target.LoadStats{}, fmt.Errorf("gr: bulk load %q: %w", ds.Name(), err)
	}
	dur := time.Since(start)

	db, err := grdb.Open(drv.path, grdb.Options{})
	if err != nil {
		return target.LoadStats{}, fmt.Errorf("gr: reopen after bulk load: %w", err)
	}
	drv.db = db

	stats := l.Stats()
	out := target.LoadStats{
		Duration: dur,
		Nodes:    int64(stats.Nodes),
		Edges:    int64(stats.Rels),
	}
	if info, err := db.Info(); err == nil {
		out.BytesOnDisk = info.SizeBytes
	}
	return out, nil
}

// nodeSources builds one loader.NodeSource per node label from the dataset
// schema, in sorted label order so the load is deterministic. Each source names
// the label (merged with the file's :LABEL column) and the file paths.
func nodeSources(ds target.Dataset) ([]loader.NodeSource, error) {
	labels := sortedKeys(keysOfNodes(ds.Schema().Nodes))
	srcs := make([]loader.NodeSource, 0, len(labels))
	for _, label := range labels {
		files, _, err := ds.NodeFiles(label)
		if err != nil {
			return nil, err
		}
		srcs = append(srcs, loader.NodeSource{Label: label, Files: files})
	}
	return srcs, nil
}

// relSources builds one loader.RelSource per relationship type, in sorted type
// order.
func relSources(ds target.Dataset) ([]loader.RelSource, error) {
	types := sortedKeys(keysOfRels(ds.Schema().Relationships))
	srcs := make([]loader.RelSource, 0, len(types))
	for _, typ := range types {
		files, _, err := ds.RelFiles(typ)
		if err != nil {
			return nil, err
		}
		srcs = append(srcs, loader.RelSource{Type: typ, Files: files})
	}
	return srcs, nil
}

func keysOfNodes(m map[string]target.NodeSchema) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

func keysOfRels(m map[string]target.RelSchema) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

func sortedKeys(ks []string) []string {
	sort.Strings(ks)
	return ks
}
