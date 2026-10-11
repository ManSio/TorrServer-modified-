package torrstor

// PauseWindowMemoryShare is the fraction of physical RAM the pause preload is
// allowed to occupy across all viewers.
//
// This matters more than the cache size, and the cache size is the number
// people reach for. The pause window is data that has been downloaded but not
// read yet, so it sits in RAM - it is not the disk cache. A 1 GiB window on a
// 3.4 GiB machine does not fail loudly, it degrades everything: the page cache
// cannot hold the disk cache either, and the whole box starts swapping.
//
// 25% leaves room for the process, the page cache behind the torrent disk
// cache, and the rest of the system.
const PauseWindowMemoryShare = 0.25

// CapPauseWindowToMemory bounds a pause window by the RAM available to it.
//
// A window of 0 means "no pause preload" and must stay 0: 0 is how a user
// turns the feature off, and treating it as "raise me to the limit" would
// silently switch it back on for everyone who disabled it.
//
// Unknown memory - the platform cannot tell us - means no cap rather than a
// guessed one. Guessing wrong in the tight direction would cut the buffer for
// every viewer on a machine that simply reports nothing.
func CapPauseWindowToMemory(pauseMB, viewers int, totalMemBytes int64) int {
	if pauseMB <= 0 {
		return 0
	}
	if totalMemBytes <= 0 || viewers <= 0 {
		return pauseMB
	}

	budget := int64(float64(totalMemBytes)*PauseWindowMemoryShare) / int64(viewers)
	budgetMB := budget / (1 << 20)
	if budgetMB < 0 {
		budgetMB = 0
	}
	// Never raise the window above what was asked for; this only ever trims.
	if int(budgetMB) < pauseMB {
		return int(budgetMB)
	}
	return pauseMB
}
