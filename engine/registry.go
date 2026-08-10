package engine

import (
	"errors"
	"fmt"
	"sort"
	"sync"
)

// ErrNoTransactions is returned by Begin on engines without transaction
// support.
var ErrNoTransactions = errors.New("engine does not support transactions")

var (
	regMu    sync.Mutex
	registry = map[string]Engine{}
)

// Register adds an engine under its Info().Name. It panics on a duplicate
// or empty name: registration happens in init functions and a bad
// registration is a programming error, not a runtime condition.
func Register(e Engine) {
	regMu.Lock()
	defer regMu.Unlock()
	name := e.Info().Name
	if name == "" {
		panic("engine: Register with empty name")
	}
	if _, dup := registry[name]; dup {
		panic(fmt.Sprintf("engine: duplicate registration %q", name))
	}
	registry[name] = e
}

// Lookup returns the registered engine by name.
func Lookup(name string) (Engine, error) {
	regMu.Lock()
	defer regMu.Unlock()
	e, ok := registry[name]
	if !ok {
		return nil, fmt.Errorf("engine %q is not registered in this build (check build tags; see graph-bench list engines)", name)
	}
	return e, nil
}

// All returns registered engines sorted by name.
func All() []Engine {
	regMu.Lock()
	defer regMu.Unlock()
	names := make([]string, 0, len(registry))
	for n := range registry {
		names = append(names, n)
	}
	sort.Strings(names)
	out := make([]Engine, 0, len(names))
	for _, n := range names {
		out = append(out, registry[n])
	}
	return out
}
