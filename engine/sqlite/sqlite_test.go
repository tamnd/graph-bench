//go:build sqlite

package sqlite

import (
	"context"
	"strings"
	"testing"

	"github.com/tamnd/graph-bench/engine/sqlbase/sqltest"
)

// The shared conformance suite, once per mode. The modes differ only in
// durability and in where the database lives, so they have to agree on
// every answer, and running all three is also the check that the pragmas
// and the shared-cache DSN leave a working database behind.
func TestConformance(t *testing.T) {
	for _, mode := range []Mode{WAL, Sync, Memory} {
		t.Run(string(mode), func(t *testing.T) {
			sqltest.Run(t, &Engine{mode: mode})
		})
	}
}

func TestLoadMethodAndVersion(t *testing.T) {
	sess := sqltest.Start(t, &Engine{mode: WAL})
	stats, err := sess.Load(context.Background(), sqltest.Dataset(t))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if stats.Method != "insert-tx" {
		t.Errorf("load method %q, want insert-tx", stats.Method)
	}
	v, err := sess.Version(context.Background())
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.HasPrefix(v, "3.") {
		t.Errorf("version %q does not look like a SQLite version", v)
	}
}
