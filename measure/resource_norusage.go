//go:build !darwin && !linux

package measure

// readProcUsage reports the kernel's accounting as unavailable on a platform
// with no getrusage path here, which makes every CPU, fault, switch and
// resident figure read -1. The heap, GC and disk fields are still captured, so
// a run on such a platform still says how much memory the engine settled at
// and how much disk the store takes.
func readProcUsage() procUsage { return procUsage{} }
