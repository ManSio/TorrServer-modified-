package torrstor

import (
	"io"
	"sync"
	"time"
)

type MemPiece struct {
	piece *Piece

	buffer []byte
	mu     sync.RWMutex
}

func NewMemPiece(p *Piece) *MemPiece {
	return &MemPiece{piece: p}
}

func (p *MemPiece) WriteAt(b []byte, off int64) (n int, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.buffer == nil {
		p.buffer = make([]byte, p.piece.cache.pieceLength)
		p.piece.cache.filledSize.Add(int64(len(p.buffer)))
		p.piece.cache.storage.addFilledSize(int64(len(p.buffer))) // Update global counter

		// Hot path: only signal background worker, never clean synchronously.
		if p.piece.cache.storage.getFilledSize() > p.piece.cache.storage.capacity {
			p.piece.cache.storage.requestCleanup()
		}
	}
	p.piece.cache.updateLRU(p.piece)
	n = copy(p.buffer[off:], b[:])
	p.piece.Size += int64(n)
	if p.piece.Size > p.piece.cache.pieceLength {
		p.piece.Size = p.piece.cache.pieceLength
	}
	p.piece.Accessed = time.Now().Unix()
	return
}

func (p *MemPiece) ReadAt(b []byte, off int64) (n int, err error) {
	p.mu.RLock()
	defer p.mu.RUnlock()

	size := len(b)
	if size+int(off) > len(p.buffer) {
		size = len(p.buffer) - int(off)
		if size < 0 {
			size = 0
		}
	}
	if len(p.buffer) < int(off) || len(p.buffer) < int(off)+size {
		return 0, io.EOF
	}
	n = copy(b, p.buffer[int(off) : int(off)+size][:])
	p.piece.Accessed = time.Now().Unix()
	p.piece.cache.updateLRU(p.piece)
	if int64(len(b))+off >= p.piece.Size {
		// R4: per-torrent cleanPieces() is gone (global worker owns
		// eviction now) — just signal it, never clean synchronously.
		p.piece.cache.storage.requestCleanup()
	}
	if n == 0 {
		return 0, io.EOF
	}
	return n, nil
}

func (p *MemPiece) Release() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.buffer != nil {
		p.piece.cache.filledSize.Add(-int64(len(p.buffer)))
		p.piece.cache.storage.addFilledSize(-int64(len(p.buffer))) // Update global counter
		p.buffer = nil
	}
	p.piece.Size = 0
	p.piece.Complete = false
}
