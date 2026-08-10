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

	// Size the build pager's pool to the input so the four-pass builder keeps the
	// column and adjacency pages it is filling resident instead of evicting and
	// re-faulting them (a quarter of the SF1 load went to pager eviction). The
	// input CSV bytes are a proxy for the output size, close enough to size a pool
	// that holds the working set under the same cap as the query pool.
	buildPool := poolPagesFor(sourceBytes(nodeSrcs, relSrcs), poolCapBytesFrom(drv.config))

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
		OnDangling:   loader.Skip,
		MaxPoolPages: buildPool,
	})
	if err := l.Run(drv.path); err != nil {
		return target.LoadStats{}, fmt.Errorf("gr: bulk load %q: %w", ds.Name(), err)
	}
	dur := time.Since(start)

	// Reopen with a pool sized to the database that was just built (the file now
	// exists at full size), so the queries that follow read from memory.
	db, err := grdb.Open(drv.path, grdb.Options{MaxPoolPages: configuredPoolPages(drv.config, drv.path)})
	if err != nil {
		return target.LoadStats{}, fmt.Errorf("gr: reopen after bulk load: %w", err)
	}
	drv.db = db

	if err := createIDIndexes(ctx, db, ds); err != nil {
		return target.LoadStats{}, err
	}

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

// createIDIndexes builds a label-property index on each node label's id column,
// the same index a real deployment of any engine stands up before serving point
// lookups and id-anchored traversals. Without it every MATCH (n:Label {id:...})
// is a full label scan, which dominates the point-read and traversal classes and
// measures the absence of an index rather than the engine. The id property name
// comes from the dataset schema; a label with no declared id is skipped. The
// index is created IF NOT EXISTS so a reload over an existing database is safe.
func createIDIndexes(ctx context.Context, db *grdb.DB, ds target.Dataset) error {
	for _, label := range sortedKeys(keysOfNodes(ds.Schema().Nodes)) {
		idProp := ds.Schema().Nodes[label].ID
		if idProp == "" {
			continue
		}
		stmt := fmt.Sprintf("CREATE INDEX IF NOT EXISTS FOR (n:%s) ON (n.%s)", label, idProp)
		res, err := db.Run(ctx, stmt, nil)
		if err != nil {
			return fmt.Errorf("gr: create id index on %s(%s): %w", label, idProp, err)
		}
		for res.Next() {
		}
		err = res.Err()
		_ = res.Close()
		if err != nil {
			return fmt.Errorf("gr: create id index on %s(%s): %w", label, idProp, err)
		}
	}
	return nil
}

// nodeSources builds one loader.NodeSource per node label from the dataset
// schema, in sorted label order so the load is deterministic. Each source names
// the label (merged with the file's :LABEL column) and the file paths.
func nodeSources(ds target.Dataset) ([]loader.NodeSource, error) {
	labels := sortedKeys(keysOfNodes(ds.Schema().Nodes))
	srcs := make([]loader.NodeSource, 0, len(labels))
	for _, label := range labels {
		ns := ds.Schema().Nodes[label]
		files, _, err := ds.NodeFiles(label)
		if err != nil {
			return nil, err
		}
		// Keep the canonical :ID as a queryable property under its schema name
		// (usually "id"). Without this the loader consumes :ID into gr's internal
		// element id and drops it, so every MATCH (n:Label {id: ...}) and n.id
		// read comes back empty and the native algorithms have no id to report.
		// createIDIndexes below stands up the index on the same property name.
		//
		// But the loader stores the :ID it exposes as a string, and when the file
		// already carries an explicit typed property of the schema id name (the LDBC
		// repack emits an id:int column so {id: $param} matches the integer the
		// queries pass), exposing :ID under that same name would write the key twice,
		// the string clobbering the typed column. So skip IDProperty when an explicit
		// id property exists and let the typed column be the sole id; the :ID is then
		// consumed as gr's element id only, which is all the edge wiring needs.
		idProp := ns.ID
		if idProp != "" && hasProperty(ns, idProp) {
			idProp = ""
		}
		srcs = append(srcs, loader.NodeSource{Label: label, IDProperty: idProp, Files: files})
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

// sourceBytes sums the on-disk size of every node and relationship input file, the
// proxy the loader's pool is sized against. A file that cannot be stat'd is skipped
// rather than failing the load; an undercount only sizes the pool a little small.
func sourceBytes(nodes []loader.NodeSource, rels []loader.RelSource) int64 {
	var total int64
	add := func(files []string) {
		for _, f := range files {
			if fi, err := os.Stat(f); err == nil {
				total += fi.Size()
			}
		}
	}
	for _, n := range nodes {
		add(n.Files)
	}
	for _, r := range rels {
		add(r.Files)
	}
	return total
}

// hasProperty reports whether a node schema lists a property column of the given
// name, the signal that the file carries an explicit typed value under that name
// rather than relying on the :ID exposure.
func hasProperty(ns target.NodeSchema, name string) bool {
	for _, c := range ns.Properties {
		if c.Name == name {
			return true
		}
	}
	return false
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
