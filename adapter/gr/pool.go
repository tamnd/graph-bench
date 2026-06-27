package gr

import (
	"os"
	"strconv"

	"github.com/tamnd/graph-bench/target"
)

// gr's buffer pool defaults to a few megabytes, which thrashes on eviction once a
// database outgrows it (the load profile on SF1 spends a quarter of its time in
// the pager's evict). The adapter sizes the pool to the database instead, so a run
// measures gr's query and load cost rather than disk re-reads under a starved
// cache. A real deployment of any engine sizes its buffer pool to its working set;
// leaving gr at its small default would be a misconfiguration, not a fair number.

const (
	// grPageSize is gr's default page size. The pool is configured in pages, so
	// the byte budget converts back through this constant. It matches
	// format.DefaultPageSize in gr.
	grPageSize = 4096
	// poolCapBytes caps the pool so a database larger than this does not try to
	// pin more than this in RAM. SF1 loads to about 1 GiB, well under the cap; a
	// larger scale falls back to a partly resident pool rather than exhausting
	// memory. Override with the pool_max_bytes config key.
	poolCapBytes = 4 << 30
)

// poolPagesFor returns a buffer pool size in pages large enough to hold a database
// (or a build output) of sizeBytes, with a quarter extra for growth and the WAL,
// clamped to capBytes. A non-positive size returns 0, which leaves gr's small
// built-in default in place (the right choice when the size is unknown).
func poolPagesFor(sizeBytes, capBytes int64) int {
	if sizeBytes <= 0 {
		return 0
	}
	want := sizeBytes + sizeBytes/4
	if want > capBytes {
		want = capBytes
	}
	return int(want / grPageSize)
}

// configuredPoolPages resolves the pool size for opening a database file. An
// explicit pool_pages config value wins; otherwise the pool is auto-sized from the
// file's current size on disk, which is the full database on the cached-reuse and
// reopen-after-load paths and zero (default pool) when the file does not exist yet.
func configuredPoolPages(config target.Config, path string) int {
	if n := explicitPoolPages(config); n != 0 {
		return n
	}
	fi, err := os.Stat(path)
	if err != nil {
		return 0
	}
	return poolPagesFor(fi.Size(), poolCapBytesFrom(config))
}

// explicitPoolPages reads the pool_pages override, an exact page count that
// bypasses auto-sizing. Zero (unset or unparseable) means auto-size.
func explicitPoolPages(config target.Config) int {
	s := config.Values["pool_pages"]
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// poolCapBytesFrom reads the pool_max_bytes override, the ceiling on the
// auto-sized pool. Zero or unparseable falls back to poolCapBytes.
func poolCapBytesFrom(config target.Config) int64 {
	s := config.Values["pool_max_bytes"]
	if s == "" {
		return poolCapBytes
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return poolCapBytes
	}
	return n
}
