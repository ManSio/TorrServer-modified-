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

// AutoPauseWindowMB derives the pause window from the hardware instead of a
// written setting.
//
// It is the same budget CapPauseWindowToMemory enforces - a quarter of RAM
// shared between viewers - but used as the source rather than the ceiling, so
// the window follows the machine: small on a small box, large on a large one,
// with no configuration. Unknown RAM falls back to 1024 MB, the shipped
// default, because refusing to buffer on a machine that simply reports
// nothing would be worse than a reasonable guess.
func AutoPauseWindowMB(totalMemBytes int64, viewers int) int {
	const fallbackMB = 1024
	if totalMemBytes <= 0 || viewers <= 0 {
		return fallbackMB
	}
	budget := int64(float64(totalMemBytes)*PauseWindowMemoryShare) / int64(viewers)
	budgetMB := int(budget / (1 << 20))
	if budgetMB < 4 {
		return 4
	}
	if budgetMB > 8192 {
		return 8192
	}
	return budgetMB
}

// UnboundedPauseWindowMB sizes a pause window by the disk cache instead of
// megabytes: from the playhead to the end of the file, or until the cache is
// full, whichever comes first.
//
// fileLen is the whole torrent, cacheFree is capacity minus filled, viewers
// split the free space. The result is in MB, floored so a full cache still
// leaves a minimal window rather than zero (zero would read as "disabled"
// downstream), and never below zero.
//
// This is the "load until loaded" behind PausePreloadUnbounded. The data
// lands on disk through the normal piece path; the in-flight portion passes
// through memory, which is the trade the setting documents.
func UnboundedPauseWindowMB(fileLen, cacheFree int64, viewers int) int {
	if viewers <= 0 {
		viewers = 1
	}
	// Never ask for more than the file holds: a window past the end is
	// bytes that do not exist.
	want := fileLen
	// Never ask for more than the cache can still take, split between
	// viewers. Asking past capacity is data eviction deletes again.
	if share := cacheFree / int64(viewers); share < want {
		want = share
	}
	if want < 0 {
		want = 0
	}
	mb := int(want / (1 << 20))
	// Floor at one full piece worth so the window stays meaningful even when
	// the cache is nearly full; the caller clamps it to the piece count.
	if mb < 4 && want > 0 {
		mb = 4
	}
	return mb
}

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
