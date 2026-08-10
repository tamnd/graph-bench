//go:build windows

package measure

import (
	"sync"
	"syscall"
	"time"
	"unsafe"
)

// Go's monotonic clock on Windows reads the interrupt time, which
// advances every 0.5ms to 15.6ms, so a service time under one tick
// reads as zero and a whole micro workload flattens to 0.0us. The
// stopwatch here reads QueryPerformanceCounter instead, which resolves
// to well under a microsecond. Only the stopwatch uses it; schedule
// arrivals and wall timestamps stay on the standard clock.

var (
	qpcOnce sync.Once
	qpcProc *syscall.LazyProc
	qpcFreq int64
)

func qpcInit() {
	k32 := syscall.NewLazyDLL("kernel32.dll")
	qpcProc = k32.NewProc("QueryPerformanceCounter")
	var freq int64
	_, _, _ = k32.NewProc("QueryPerformanceFrequency").Call(uintptr(unsafe.Pointer(&freq)))
	qpcFreq = freq
}

func qpcNow() int64 {
	qpcOnce.Do(qpcInit)
	var t int64
	_, _, _ = qpcProc.Call(uintptr(unsafe.Pointer(&t)))
	return t
}

// instant is one reading of the service-time stopwatch, in QPC ticks.
type instant struct{ ticks int64 }

// tick starts the stopwatch.
func tick() instant { return instant{ticks: qpcNow()} }

// elapsed reads the stopwatch without stopping it. The tick delta is
// split into whole seconds and remainder before scaling so the
// conversion cannot overflow however long the stopwatch runs.
func (i instant) elapsed() time.Duration {
	if qpcFreq == 0 {
		return 0
	}
	d := qpcNow() - i.ticks
	sec := d / qpcFreq
	rem := d % qpcFreq
	return time.Duration(sec)*time.Second + time.Duration(rem*int64(time.Second)/qpcFreq)
}
