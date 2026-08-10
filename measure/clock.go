//go:build !windows

package measure

import "time"

// instant is one reading of the service-time stopwatch. Everywhere but
// Windows, time.Now carries a fine-grained monotonic reading, so the
// standard clock is the stopwatch.
type instant struct{ t time.Time }

// tick starts the stopwatch.
func tick() instant { return instant{t: time.Now()} }

// elapsed reads the stopwatch without stopping it.
func (i instant) elapsed() time.Duration { return time.Since(i.t) }
