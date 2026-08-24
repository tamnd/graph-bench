//go:build linux

package measure

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// ProcessTreeBytes is the resident set of this process and every descendant
// alive at the moment of the call, in bytes, or -1 when /proc could not be
// read.
//
// Descendants are counted because on a subprocess plane the engine is one,
// and a probe that read only this process would report the engine as free.
// A descendant that comes and goes between two readings is missed, which is
// the price of sampling; the reaped-children rusage keeps the peak that
// sampling cannot see, and the two rows sit beside each other for that
// reason.
func ProcessTreeBytes() int64 {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return -1
	}
	type proc struct {
		ppid int
		rss  int64
	}
	procs := make(map[int]proc, len(entries))
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		ppid, rss, ok := readStat(pid)
		if !ok {
			continue
		}
		procs[pid] = proc{ppid: ppid, rss: rss}
	}
	self := os.Getpid()
	if _, ok := procs[self]; !ok {
		return -1
	}
	// Walk down from this process rather than up from each one, so a pid
	// whose parent died and was reparented to init is not counted.
	tree := map[int]bool{self: true}
	for grew := true; grew; {
		grew = false
		for pid, p := range procs {
			if !tree[pid] && tree[p.ppid] {
				tree[pid] = true
				grew = true
			}
		}
	}
	page := int64(syscall.Getpagesize())
	var total int64
	for pid := range tree {
		total += procs[pid].rss * page
	}
	return total
}

// readStat reads the parent pid and the resident set, in pages, out of one
// /proc/[pid]/stat line. The comm field is parenthesized and may itself hold
// spaces and parentheses, so the fields are counted from the last close
// parenthesis rather than from the start of the line.
func readStat(pid int) (ppid int, rssPages int64, ok bool) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return 0, 0, false
	}
	line := string(data)
	end := strings.LastIndex(line, ")")
	if end < 0 {
		return 0, 0, false
	}
	// After the comm field the fields are state, ppid, pgrp, ... and rss is
	// the 24th field of the whole line, which is the 21st of these.
	fields := strings.Fields(line[end+1:])
	if len(fields) < 22 {
		return 0, 0, false
	}
	ppid, err = strconv.Atoi(fields[1])
	if err != nil {
		return 0, 0, false
	}
	rssPages, err = strconv.ParseInt(fields[21], 10, 64)
	if err != nil {
		return 0, 0, false
	}
	return ppid, rssPages, true
}
