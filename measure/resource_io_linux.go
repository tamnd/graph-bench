package measure

import (
	"os"
	"strconv"
	"strings"
)

// readDiskBytes reads the per-process block layer byte counters from
// /proc/self/io: read_bytes is what the process fetched through the block
// layer and write_bytes is what it sent to it, both counted by the kernel
// rather than by the harness, so they include the bytes an fsync forces out
// and exclude a write the page cache absorbed and never flushed.
//
// These are the two figures that separate an engine that makes a write durable
// cheaply from one that rewrites a whole column to do it, which no latency
// number shows. The counters are cumulative since process start, so a caller
// diffs two readings.
func readDiskBytes() (read, written int64, ok bool) {
	data, err := os.ReadFile("/proc/self/io")
	if err != nil {
		return 0, 0, false
	}
	var haveRead, haveWritten bool
	for _, line := range strings.Split(string(data), "\n") {
		key, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		if err != nil {
			continue
		}
		switch key {
		case "read_bytes":
			read, haveRead = n, true
		case "write_bytes":
			written, haveWritten = n, true
		}
	}
	return read, written, haveRead && haveWritten
}
