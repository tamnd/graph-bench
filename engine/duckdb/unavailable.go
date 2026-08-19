//go:build !duckdb

package duckdb

import (
	"context"
	"fmt"

	"github.com/tamnd/graph-bench/engine"
)

// Start fails on a binary built without the duckdb tag, and says what to
// build instead. The engine stays registered in every build so that
// `list engines` tells the truth about what the harness supports.
func (e *Engine) Start(context.Context, engine.Config) (engine.Session, error) {
	return nil, fmt.Errorf("duckdb: this binary was built without the duckdb tag, so the DuckDB library is not linked in; " +
		"rebuild with `go build -tags duckdb ./cmd/graph-bench` (it needs cgo and downloads a prebuilt DuckDB library)")
}
