package postgres

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/tamnd/graph-bench/engine/sqlbase/sqltest"
)

// The conformance suite needs a server, and a unit test has no business
// starting one: a container takes tens of seconds and `go test ./...`
// should stay fast and offline. So the test runs against whatever server
// the operator points it at and skips otherwise, which is the same
// discovery order the adapter itself uses.
//
//	docker run --rm -d -p 5432:5432 -e POSTGRES_USER=bench \
//	  -e POSTGRES_PASSWORD=bench -e POSTGRES_DB=bench postgres:18.6
//	GRAPH_BENCH_PG_DSN='postgres://bench:bench@127.0.0.1:5432/bench?sslmode=disable' \
//	  go test ./engine/postgres/
func requireServer(t *testing.T) {
	t.Helper()
	if os.Getenv("GRAPH_BENCH_PG_DSN") == "" && os.Getenv("DATABASE_URL") == "" {
		t.Skip("no PostgreSQL server: set GRAPH_BENCH_PG_DSN or DATABASE_URL")
	}
}

func TestConformance(t *testing.T) {
	requireServer(t)
	sqltest.Run(t, New())
}

func TestLoadMethodAndVersion(t *testing.T) {
	requireServer(t)
	sess := sqltest.Start(t, New())
	stats, err := sess.Load(context.Background(), sqltest.Dataset(t))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if stats.Method != "copy" {
		t.Errorf("load method %q, want copy", stats.Method)
	}
	v, err := sess.Version(context.Background())
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	if !strings.HasPrefix(v, "1") {
		t.Errorf("version %q does not look like a PostgreSQL version", v)
	}
}
