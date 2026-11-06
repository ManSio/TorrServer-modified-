package torr

import (
	"testing"

	sets "server/settings"
)

// TestMalformedHashDoesNotPanic is the #941 regression: the torrent API used
// metainfo.NewHashFromHex, which panics on anything but 40 hex digits.
func TestMalformedHashDoesNotPanic(t *testing.T) {
	old := sets.BTsets
	sets.BTsets = &sets.BTSets{}
	oldReadOnly := sets.ReadOnly
	sets.ReadOnly = true
	t.Cleanup(func() {
		sets.BTsets = old
		sets.ReadOnly = oldReadOnly
	})

	for _, hash := range []string{"", "xyz", "1234", "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"} {
		if got := GetTorrent(hash); got != nil {
			t.Errorf("GetTorrent(%q) = %v, want nil", hash, got)
		}
		if got := SetTorrent(hash, "", "", "", ""); got != nil {
			t.Errorf("SetTorrent(%q) = %v, want nil", hash, got)
		}
		DropTorrent(hash)
		RemTorrent(hash)
	}
}
