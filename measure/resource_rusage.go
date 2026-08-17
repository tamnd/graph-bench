//go:build darwin || linux

package measure

import (
	"runtime"
	"syscall"
)

// readProcUsage reads the kernel's accounting for this process and for the
// children it has reaped, through getrusage, and then the per-process disk
// byte counters where the platform has them.
//
// RUSAGE_SELF covers the harness, which is the engine on an in-process plane.
// RUSAGE_CHILDREN covers every child already waited for, which is the engine on
// a subprocess plane, and stays empty until the session owning the child is
// closed. A Bolt engine appears in neither: its server was never forked here.
//
// ru_maxrss is in bytes on darwin and in kilobytes on Linux, so the Linux value
// is scaled. ru_inblock and ru_oublock are always zero on darwin, which the
// Resource field comments say rather than hide.
func readProcUsage() procUsage {
	var self, kids syscall.Rusage
	if err := syscall.Getrusage(syscall.RUSAGE_SELF, &self); err != nil {
		return procUsage{}
	}
	if err := syscall.Getrusage(syscall.RUSAGE_CHILDREN, &kids); err != nil {
		return procUsage{}
	}
	u := procUsage{
		ok:               true,
		userNs:           self.Utime.Nano(),
		sysNs:            self.Stime.Nano(),
		maxRSS:           rssBytes(int64(self.Maxrss)),
		minorFaults:      int64(self.Minflt),
		majorFaults:      int64(self.Majflt),
		volCtx:           int64(self.Nvcsw),
		involCtx:         int64(self.Nivcsw),
		blockIn:          int64(self.Inblock),
		blockOut:         int64(self.Oublock),
		childUserNs:      kids.Utime.Nano(),
		childSysNs:       kids.Stime.Nano(),
		childMaxRSS:      rssBytes(int64(kids.Maxrss)),
		childMinorFaults: int64(kids.Minflt),
		childMajorFaults: int64(kids.Majflt),
		childVolCtx:      int64(kids.Nvcsw),
		childInvolCtx:    int64(kids.Nivcsw),
		childBlockIn:     int64(kids.Inblock),
		childBlockOut:    int64(kids.Oublock),
	}
	if read, written, ok := readDiskBytes(); ok {
		u.ioOK, u.diskReadBytes, u.diskWriteBytes = true, read, written
	}
	return u
}

// rssBytes normalizes a ru_maxrss field to bytes.
func rssBytes(raw int64) int64 {
	if runtime.GOOS == "linux" {
		return raw * 1024
	}
	return raw
}
