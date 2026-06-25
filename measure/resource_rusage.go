//go:build darwin || linux

package measure

import (
	"runtime"
	"syscall"
)

// maxRSSBytes returns the process peak resident set size in bytes via getrusage.
// The raw ru_maxrss field is in bytes on macOS and in kilobytes on Linux, so the
// Linux value is scaled up. Returns -1 if the syscall fails.
func maxRSSBytes() int64 {
	var ru syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &ru); err != nil {
		return -1
	}
	rss := int64(ru.Maxrss)
	if runtime.GOOS == "linux" {
		return rss * 1024
	}
	return rss
}
