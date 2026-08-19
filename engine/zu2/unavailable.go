//go:build !zu2inproc

package zu2

import (
	"context"
	"fmt"

	"github.com/tamnd/graph-bench/engine"
)

// Start fails on a binary built without the zu2inproc tag, and says what
// to build instead. The engine stays registered in every build so that
// `list engines` tells the truth about what the harness supports, and so
// that a run against a binary missing the tag reports a build problem
// rather than an unknown engine name.
func (e *Engine) Start(ctx context.Context, cfg engine.Config) (engine.Session, error) {
	return nil, fmt.Errorf("zu2: this binary was built without the zu2inproc tag, so libzu2 is not linked in; " +
		"build it with `cargo build --release -p zu2-capi` in the zu repo, then rebuild with " +
		"`go build -tags zu2inproc ./cmd/graph-bench`")
}
