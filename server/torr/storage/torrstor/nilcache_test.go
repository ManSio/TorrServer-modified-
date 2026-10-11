package torrstor

import (
	"testing"

	"github.com/anacrolix/torrent/metainfo"
)

// GetCache returns nil for a hash the storage does not know. A nil *Cache
// reaching SetTorrent used to panic and take the stream handler with it, which
// presented as "seeking does nothing": the player opens a new range request,
// the handler panics, no bytes are served from the new position.
//
// Storage has no cache here on purpose - an unknown hash is exactly the case
// that produced the panic.
func TestSetTorrentOnNilCacheDoesNotPanic(t *testing.T) {
	var c *Cache
	c.SetTorrent(nil) // must not panic
}

// The same nil-receiver guard the other accessors on Cache already have.
func TestNilCacheAccessorsAreSafe(t *testing.T) {
	var c *Cache
	c.SetTorrent(nil)
	if got := c.Readers(); got != 0 {
		t.Errorf("Readers on nil cache: got %d, want 0", got)
	}
	if got := c.GetCapacity(); got != 0 {
		t.Errorf("GetCapacity on nil cache: got %d, want 0", got)
	}
	if got := c.MaxReaderIdleSec(); got != 0 {
		t.Errorf("MaxReaderIdleSec on nil cache: got %d, want 0", got)
	}
}

// GetCache must be able to answer "I do not know this hash" with nil, and the
// caller must survive it. This pins the shape of the bug: the lookup returning
// nil is correct behaviour, the panic was in what the caller did with it.
func TestGetCacheReturnsNilForUnknownHash(t *testing.T) {
	stor := NewStorage(64 << 20)
	defer stor.Close()

	unknown := metainfo.Hash{0xde, 0xad, 0xbe, 0xef}
	if got := stor.GetCache(unknown); got != nil {
		t.Errorf("GetCache for an unknown hash returned %v, want nil", got)
	}
	// Surviving the lookup and calling through the result is the regression.
	var c *Cache = stor.GetCache(unknown)
	c.SetTorrent(nil)
}
