package torrstor

import (
	"reflect"
	"testing"
	"time"
	"unsafe"

	"github.com/anacrolix/torrent"
)

// The protection window used to be cache.capacity * ReaderReadAHead%, i.e. a
// percentage of the whole cache. With a 91 GiB cache and ReadAhead=81% one
// reader protected ~74 GiB — more than the entire limit — so eviction could
// never converge while anything was playing. The window now follows the
// reader's own readahead.

const windowPieceLen = 4 << 20

func mkFakeFile(offset, length int64) *torrent.File {
	f := new(torrent.File)
	v := reflect.ValueOf(f).Elem()
	setUnexported(v, "offset", reflect.ValueOf(offset))
	setUnexported(v, "length", reflect.ValueOf(length))
	return f
}

func setUnexported(v reflect.Value, name string, val reflect.Value) {
	f := v.FieldByName(name)
	reflect.NewAt(f.Type(), unsafe.Pointer(f.UnsafeAddr())).Elem().Set(val)
}

func newReaderForWindow(capacity int64) *Reader {
	c := &Cache{
		capacity:    capacity,
		pieceLength: windowPieceLen,
		readers:     make(map[*Reader]struct{}),
		pieces:      make(map[int]*Piece),
	}
	r := &Reader{cache: c, offset: 40 << 30, isUse: true, lastAccess: time.Now().Unix()}
	c.readers[r] = struct{}{}
	return r
}

// The window must not scale with the cache size: a 100 GiB cache with the
// default 81% ReadAhead must not protect ~80 GiB.
func TestReaderWindowDoesNotScaleWithCache(t *testing.T) {
	const capacity = int64(100) << 30
	r := newReaderForWindow(capacity)
	r.file = mkFakeFile(0, 100<<30)
	r.readahead = 16 << 20

	start, end := r.getOffsetRange()
	width := end - start
	t.Logf("cache=%d GB, protected window=%d MB", capacity>>30, width>>20)
	if width > 64<<20 {
		t.Errorf("window is %d MB — it must follow the reader readahead (16 MB), "+
			"not the cache size", width>>20)
	}
}

// A reader with readahead 0 (parked) still protects the piece it sits on.
func TestReaderWindowCoversCurrentPieceWithoutReadahead(t *testing.T) {
	r := newReaderForWindow(1 << 30)
	r.file = mkFakeFile(0, 100<<30)
	r.readahead = 0

	start, end := r.getOffsetRange()
	if start < 0 || end < windowPieceLen {
		t.Fatalf("window [%d..%d] does not cover one piece", start, end)
	}
}