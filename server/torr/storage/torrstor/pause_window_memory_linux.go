//go:build linux

package torrstor

import "syscall"

// TotalMemoryBytes reports physical RAM. Linux answers this exactly, and this
// is the platform the server runs on.
func TotalMemoryBytes() int64 {
	var si syscall.Sysinfo_t
	if err := syscall.Sysinfo(&si); err != nil {
		return 0
	}
	// Totalram is in units of MemUnit on Linux.
	unit := uint64(si.Unit)
	if unit == 0 {
		unit = 1
	}
	return int64(si.Totalram) * int64(unit)
}
