//go:build duckdb

package duckdb

import (
	"context"
	"strings"
	"testing"

	"github.com/tamnd/graph-bench/engine/sqlbase/sqltest"
)

// The shared conformance suite, once per mode. Running the memory mode
// through it is also the check that every connection database/sql opens
// from the connector reaches the same in-memory database, which is the
// one way this adapter can go wrong and still start cleanly: the load
// would land in one database and the queries read an empty other one.
func TestConformance(t *testing.T) {
	for _, mode := range []Mode{File, Memory} {
		t.Run(string(mode), func(t *testing.T) {
			sqltest.Run(t, &Engine{mode: mode})
		})
	}
}

func TestLoadMethodAndVersion(t *testing.T) {
	sess := sqltest.Start(t, &Engine{mode: File})
	stats, err := sess.Load(context.Background(), sqltest.Dataset(t))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if stats.Method != "read_csv" {
		t.Errorf("load method %q, want read_csv", stats.Method)
	}
	v, err := sess.Version(context.Background())
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	// The v prefix comes off in Version so the string reads like every
	// other engine's.
	if !strings.HasPrefix(v, "1.") {
		t.Errorf("version %q does not look like a bare DuckDB version", v)
	}
}
