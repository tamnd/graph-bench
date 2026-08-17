//go:build !linux

package measure

// readDiskBytes reports the per-process block layer byte counters as
// unavailable off Linux. darwin has them, in proc_pid_rusage's
// ri_diskio_bytesread and ri_diskio_byteswritten, but reaching that means
// linking libproc through cgo, and the default build of this harness is
// cgo-free on purpose so a plain `go build` produces the same binary
// everywhere. The Resource fields read -1 here, and the store growth figure
// still says what the run left on disk.
func readDiskBytes() (read, written int64, ok bool) { return 0, 0, false }
