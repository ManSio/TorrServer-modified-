package torrstor

import (
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/anacrolix/torrent"

	"server/log"
)

type Reader struct {
	torrent.Reader
	offset    int64
	readahead int64
	file      *torrent.File

	cache    *Cache
	isClosed bool

	///Preload
	lastAccess int64
	isUse      bool
	mu         sync.Mutex
	// inFlight counts Reads/Seeks currently inside the anacrolix reader.
	// Guarded by mu — the same mutex readerOff takes. A read blocked on
	// the swarm stamps lastAccess only after it returns, so without this
	// checkReader mistakes a busy reader for an abandoned one and
	// readerOff seeks it to 0 mid-read (upstream PR #876 port).
	inFlight int
	// Debug telemetry. Atomic: read from /stat while the stream goroutine
	// writes them, so no reader mutex is involved.
	readCalls atomic.Uint64
	readBytes atomic.Uint64
}

func newReader(file *torrent.File, cache *Cache) *Reader {
	r := new(Reader)
	r.file = file
	r.Reader = file.NewReader()

	r.SetReadahead(0)
	r.cache = cache
	r.isUse = true

	cache.muReaders.Lock()
	cache.readers[r] = struct{}{}
	cache.muReaders.Unlock()
	return r
}

func (r *Reader) Seek(offset int64, whence int) (n int64, err error) {
	if r.isClosed {
		return 0, io.EOF
	}
	switch whence {
	case io.SeekStart:
		r.offset = offset
	case io.SeekCurrent:
		r.offset += offset
	case io.SeekEnd:
		r.offset = r.file.Length() + offset
	}
	r.readerOn()
	r.beginIO()
	n, err = r.Reader.Seek(offset, whence)
	r.endIO()
	r.offset = n
	r.lastAccess = time.Now().Unix()
	// Priorities are recomputed here instead of waiting for the 5 s watchdog:
	// right after a seek the client would otherwise keep requesting pieces
	// around the old position while the viewer waits on a hole.
	go r.cache.getRemPieces()
	return
}

func (r *Reader) Read(p []byte) (n int, err error) {
	err = io.EOF
	if r.isClosed {
		return
	}
	if r.file.Torrent() != nil && r.file.Torrent().Info() != nil {
		r.readerOn()
		r.beginIO()
		n, err = r.Reader.Read(p)
		r.endIO()
		r.readCalls.Add(1)
		r.readBytes.Add(uint64(max(n, 0)))

		// samsung tv fix xvid/divx
		//if r.offset == 0 && len(p) >= 192 {
		//	str := strings.ToLower(string(p[112:116]))
		//	if str == "xvid" || str == "divx" {
		//		p[112] = 0x4D // M
		//		p[113] = 0x50 // P
		//		p[114] = 0x34 // 4
		//		p[115] = 0x56 // V
		//	}
		//	str = strings.ToLower(string(p[188:192]))
		//	if str == "xvid" || str == "divx" {
		//		p[188] = 0x4D // M
		//		p[189] = 0x50 // P
		//		p[190] = 0x34 // 4
		//		p[191] = 0x56 // V
		//	}
		//}

		r.offset += int64(n)
		r.lastAccess = time.Now().Unix()
	} else {
		log.TLogln("Torrent closed and readed")
	}
	return
}

func (r *Reader) SetReadahead(length int64) {
	if r.cache != nil && length > r.cache.capacity {
		length = r.cache.capacity
	}
	if r.isUse {
		r.Reader.SetReadahead(length)
	}
	r.readahead = length
}

// setClientReadahead changes only how much the anacrolix reader buffers,
// leaving our own protection width alone.
//
// SetReadahead cannot be used for parking: it also rewrites r.readahead, and
// the protection window is derived from r.readahead. Parking a reader
// therefore collapsed the window to ~2 pieces, and eviction was then free to
// delete the paused viewer's data within seconds — measured: a TV paused for
// 120 s kept 3 of 14 window pieces instead of all 14.
func (r *Reader) setClientReadahead(length int64) {
	if r.isUse {
		r.Reader.SetReadahead(length)
	}
}

func (r *Reader) Offset() int64 {
	return r.offset
}

func (r *Reader) Readahead() int64 {
	return r.readahead
}

func (r *Reader) Close() {
	// file reader close in gotorrent
	// this struct close in cache
	r.isClosed = true
	if len(r.file.Torrent().Files()) > 0 {
		r.Reader.Close()
	}
	go r.cache.getRemPieces()
}

func (r *Reader) getPiecesRange() Range {
	startOff, endOff := r.getOffsetRange()
	return Range{r.getPieceNum(startOff), r.getPieceNum(endOff), r.file}
}

func (r *Reader) getReaderPiece() int {
	return r.getPieceNum(r.offset)
}

func (r *Reader) getReaderRAHPiece() int {
	return r.getPieceNum(r.offset + r.readahead)
}

func (r *Reader) getPieceNum(offset int64) int {
	return int((offset + r.file.Offset()) / r.cache.pieceLength)
}

// getOffsetRange is the window of the file a reader is about to need, and
// therefore the window that eviction must not touch.
//
// It used to be cache.capacity * ReaderReadAHead%, i.e. with a 98 GiB cache
// and ReadAhead=81% a single reader protected ~79 GiB — more than the whole
// limit, so the LRU could never bring the cache back down while anything was
// playing (reproduced: 976 MB stuck above a 921 MB target).
//
// The meaningful scale is what this reader actually asked for: its readahead
// window (16 MB by default, see Torrent.updateRA), plus one piece of margin
// on each side for the piece being served and the one after it.
func (r *Reader) getOffsetRange() (int64, int64) {
	back := r.cache.pieceLength
	fwd := r.readahead + r.cache.pieceLength

	beginOffset := r.offset - back
	endOffset := r.offset + fwd

	if beginOffset < 0 {
		beginOffset = 0
	}
	if endOffset > r.file.Length() {
		endOffset = r.file.Length()
	}
	return beginOffset, endOffset
}

func (r *Reader) checkReader() {
	if time.Now().Unix() > r.lastAccess+60 && r.cache.Readers() > 1 {
		r.readerOff()
	} else {
		r.readerOn()
	}
}

func (r *Reader) readerOn() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.isUse {
		if pos, err := r.Reader.Seek(0, io.SeekCurrent); err == nil && pos == 0 {
			r.Reader.Seek(r.offset, io.SeekStart)
		}
		r.SetReadahead(r.readahead)
		r.isUse = true
	}
}

func (r *Reader) readerOff() {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Never park a reader with I/O in flight: a read blocked on the
	// swarm would otherwise be seeked to 0 mid-read and the stream
	// would continue from the file head (upstream PR #876 port).
	if r.inFlight > 0 {
		return
	}
	if r.isUse {
		// Stop buffering, but keep the window width: the protection window is
		// computed from r.readahead and a paused viewer is still protected.
		r.setClientReadahead(0)
		r.isUse = false
		if r.offset > 0 {
			r.Reader.Seek(0, io.SeekStart)
		}
	}
}

// beginIO/endIO bracket the anacrolix Read/Seek so readerOff can tell a
// busy reader apart from an abandoned one. Same mutex readerOff takes.
func (r *Reader) beginIO() {
	r.mu.Lock()
	r.inFlight++
	r.mu.Unlock()
}

func (r *Reader) endIO() {
	r.mu.Lock()
	r.inFlight--
	r.mu.Unlock()
}
