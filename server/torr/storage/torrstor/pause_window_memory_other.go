//go:build !linux

package torrstor

// TotalMemoryBytes reports 0 on platforms where this build does not probe
// RAM. A 0 means "unknown", and CapPauseWindowToMemory treats unknown as
// "no cap" rather than inventing a limit.
func TotalMemoryBytes() int64 { return 0 }
