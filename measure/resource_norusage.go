//go:build !darwin && !linux

package measure

// maxRSSBytes reports peak RSS as unavailable on platforms without a getrusage
// path here. The other Resource fields (heap, GC, disk) are still captured.
func maxRSSBytes() int64 { return -1 }
