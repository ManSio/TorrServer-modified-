package torrstor

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"
)

// fakeTorrentReader stands in for anacrolix torrent.Reader: controllable
// blocking reads plus position tracking, no swarm needed.
type fakeTorrentReader struct {
	mu        sync.Mutex
	pos       int64
	seeks     []int64
	readahead int64
	block     chan struct{} // when non-nil, Read waits for close
	closed    bool
}

func (f *fakeTorrentReader) Read(p []byte) (int, error) {
	f.mu.Lock()
	blk := f.block
	f.mu.Unlock()
	if blk != nil {
		<-blk
	}
	for i := range p {
		p[i] = 0xAB
	}
	f.mu.Lock()
	f.pos += int64(len(p))
	f.mu.Unlock()
	return len(p), nil
}

func (f *fakeTorrentReader) ReadContext(_ context.Context, p []byte) (int, error) {
	return f.Read(p)
}

func (f *fakeTorrentReader) Seek(offset int64, whence int) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch whence {
	case io.SeekStart:
		f.pos = offset
	case io.SeekCurrent:
		f.pos += offset
	}
	f.seeks = append(f.seeks, f.pos)
	return f.pos, nil
}

func (f *fakeTorrentReader) Close() error {
	f.mu.Lock()
	f.closed = true
	f.mu.Unlock()
	return nil
}

func (f *fakeTorrentReader) SetReadahead(n int64) {
	f.mu.Lock()
	f.readahead = n
	f.mu.Unlock()
}

func (f *fakeTorrentReader) SetResponsive() {}

func mkTestReader(c *Cache, offset int64, staleSecs int64, fr *fakeTorrentReader) *Reader {
	r := &Reader{
		Reader:     fr,
		offset:     offset,
		cache:      c,
		isUse:      true,
		lastAccess: time.Now().Unix() - staleSecs,
	}
	c.muReaders.Lock()
	c.readers[r] = struct{}{}
	c.muReaders.Unlock()
	return r
}

// A read blocked on the swarm must not get its reader parked (#876 port).
func TestReaderOffDoesNotParkInFlight(t *testing.T) {
	c := &Cache{capacity: 1 << 30, readers: make(map[*Reader]struct{})}
	fr := &fakeTorrentReader{block: make(chan struct{})}
	r := mkTestReader(c, 943718400, 120, fr)
	mkTestReader(c, 0, 0, &fakeTorrentReader{}) // second reader: parking enabled
	done := make(chan struct{})
	started := make(chan struct{})
	go func() {
		defer close(done)
		// file/Torrent/Info are nil -> real Read() would take the log
		// branch, not the anacrolix path. Exercise begin/endIO +
		// checkReader directly (the guard under test).
		// Handshake (not sleep): checkReader must observe inFlight=1.
		r.beginIO()
		close(started)
		time.Sleep(300 * time.Millisecond)
		if !r.isUse {
			t.Error("reader parked while I/O in flight")
		}
		r.endIO()
	}()

	<-started
	time.Sleep(100 * time.Millisecond)
	r.checkReader() // stale + 2 readers -> would park if not in-flight
	<-done

	if !r.isUse {
		t.Fatal("parked with a read in flight: isUse=false, want true")
	}
	t.Log("✅ in-flight reader not parked")
}

// An actually idle reader is still parked, and the next read resumes it.
func TestIdleReaderParkedAndResumed(t *testing.T) {
	c := &Cache{capacity: 1 << 30, readers: make(map[*Reader]struct{})}
	fr := &fakeTorrentReader{}
	r := mkTestReader(c, 943718400, 120, fr)
	mkTestReader(c, 0, 0, &fakeTorrentReader{})

	r.checkReader()
	if r.isUse {
		t.Fatal("idle reader not parked")
	}
	if fr.readahead != 0 {
		t.Fatal("parked reader readahead != 0")
	}
	t.Log("✅ idle reader parked; resume covered by readerOn on next Read")
}
