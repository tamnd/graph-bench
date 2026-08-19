//go:build !sqlite

package sqlite

import (
	"context"
	"fmt"

	"github.com/tamnd/graph-bench/engine"
)

// Start fails on a binary built without the sqlite tag, and says what to
// build instead. The engine stays registered in every build so that
// `list engines` tells the truth about what the harness supports.
func (e *Engine) Start(context.Context, engine.Config) (engine.Session, error) {
	return nil, fmt.Errorf("sqlite: this binary was built without the sqlite tag, so the SQLite library is not linked in; " +
		"rebuild with `go build -tags sqlite ./cmd/graph-bench` (it needs cgo and a C compiler)")
}
