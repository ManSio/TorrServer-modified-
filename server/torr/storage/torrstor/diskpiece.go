package torrstor

import (
	"io"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"time"

	"server/log"
	"server/settings"
)

type DiskPiece struct {
	piece *Piece

	name string

	mu sync.RWMutex
}

func NewDiskPiece(p *Piece) *DiskPiece {
	name := filepath.Join(settings.BTsets.TorrentsSavePath, p.cache.hash.HexString(), strconv.Itoa(p.Id))
	ff, err := os.Stat(name)
	if err == nil {
		p.Size = ff.Size()
		p.Complete = ff.Size() == p.cache.pieceLength
		p.Accessed = ff.ModTime().Unix()
		p.cache.filledSize.Add(p.Size)
		p.cache.storage.addFilledSize(p.Size) // Update global counter on load
		if p.Id%100 == 0 {                    // Log every 100th piece to avoid spam
			log.TLogln("[DEBUG] Loaded piece", p.Id, "size=", p.Size, "torrent filled=", p.cache.filledSize.Load())
		}
	}
	return &DiskPiece{piece: p, name: name}
}

func (p *DiskPiece) WriteAt(b []byte, off int64) (n int, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	ff, err := os.OpenFile(p.name, os.O_RDWR|os.O_CREATE, 0o666)
	if err != nil {
		log.TLogln("Error open file:", err)
		return 0, err
	}
	defer ff.Close()

	// Pre-allocate disk space to avoid fragmentation and I/O spikes (Linux only)
	// Note: Fallocate is not available on Windows, skip silently
	// TODO: Use platform-specific build tags if needed

	n, err = ff.WriteAt(b, off)

	p.piece.cache.filledSize.Add(int64(n))
	p.piece.cache.storage.addFilledSize(int64(n)) // Update global counter
	p.piece.Size += int64(n)
	if p.piece.Size > p.piece.cache.pieceLength {
		delta := p.piece.Size - p.piece.cache.pieceLength
		p.piece.cache.filledSize.Add(-delta)
		p.piece.cache.storage.addFilledSize(-delta) // Update global counter
		p.piece.Size = p.piece.cache.pieceLength
	}
	p.piece.Accessed = time.Now().Unix()
	p.piece.cache.updateLRU(p.piece)

	// Hot path: never run cleanup synchronously here (it sorts + deletes
	// under locks). Just signal the background worker.
	if p.piece.cache.storage.getFilledSize() > p.piece.cache.storage.capacity {
		p.piece.cache.storage.requestCleanup()
	}

	return n, err
}

func (p *DiskPiece) ReadAt(b []byte, off int64) (n int, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	ff, err := os.OpenFile(p.name, os.O_RDONLY, 0o666)
	if os.IsNotExist(err) {
		return 0, io.EOF
	}
	if err != nil {
		log.TLogln("Error open file:", err)
		return 0, err
	}
	defer ff.Close()

	n, err = ff.ReadAt(b, off)

	p.piece.Accessed = time.Now().Unix()
	p.piece.cache.updateLRU(p.piece)
	// Hot path: reads must never trigger synchronous cleanup (deadlock risk
	// with client lock + playback stalls). Signal worker instead.
	if int64(len(b))+off >= p.piece.Size {
		if p.piece.cache.storage.getFilledSize() > p.piece.cache.storage.capacity {
			p.piece.cache.storage.requestCleanup()
		}
	}
	return n, err
}

func (p *DiskPiece) Release() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.piece.Size > 0 {
		p.piece.cache.filledSize.Add(-p.piece.Size)
		p.piece.cache.storage.addFilledSize(-p.piece.Size) // Update global counter
	}
	p.piece.Size = 0
	p.piece.Complete = false

	// Non-blocking async deletion via channel (never block holding p.mu)
	select {
	case p.piece.cache.evictChan <- p.name:
	default:
		_ = os.Remove(p.name)
	}
}
