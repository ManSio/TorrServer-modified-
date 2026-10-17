package torr

import (
	"testing"

	"server/torr/state"
)

// Regression test for the drop-vs-preload race (upstream YouROK/TorrServer
// issue #916). Torrent.drop() sets t.Torrent = nil while leaving Stat at
// TorrentWorking ("drop all torrents" in SetSettings does exactly that), and
// Preload used to check only Stat before dereferencing t.Files()[0] — an
// index-out-of-range panic on the nil slice. Preload must be a silent no-op.
//
// Verified to fail (panic) before the guard and pass after it.
func TestPreloadNoPanicWhenTorrentDropped(t *testing.T) {
	tr := &Torrent{}
	tr.Stat = state.TorrentWorking

	tr.Preload(1, 1<<20)
}
