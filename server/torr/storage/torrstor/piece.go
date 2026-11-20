package torrstor

import (
	"container/list"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/storage"
	"server/settings"
)

type Piece struct {
	storage.PieceImpl `json:"-"`

	Id   int   `json:"-"`
	Size int64 `json:"size"`

	Complete bool  `json:"complete"`
	Accessed int64 `json:"accessed"`

	mPiece *MemPiece  `json:"-"`
	dPiece *DiskPiece `json:"-"`

	// useDisk is the backend chosen for this piece at creation. It is a field,
	// not settings.BTsets.UseDisk: the setting can change while the piece is
	// alive (SetSettings switches it before dropping torrents), and WriteAt,
	// ReadAt, Release and the state lock must keep using the same backend.
	useDisk bool `json:"-"`

	cache        *Cache        `json:"-"`
	evictionElem *list.Element `json:"-"`
}

func NewPiece(id int, cache *Cache) *Piece {
	p := &Piece{
		Id:      id,
		cache:   cache,
		useDisk: settings.BTsets != nil && settings.BTsets.UseDisk,
	}

	if !p.useDisk {
		p.mPiece = NewMemPiece(p)
	} else {
		p.dPiece = NewDiskPiece(p)
	}
	return p
}

// WriteAt, ReadAt and Release use the backend this piece was created with, not
// the live UseDisk setting. SetSettings replaces BTsets first and drops the
// torrents after; in between the engine keeps writing and reading the pieces
// that already exist, and the other backend is nil there — the resulting panic
// in the engine goroutine took the whole server down (upstream 9eb0f9f7).
func (p *Piece) WriteAt(b []byte, off int64) (n int, err error) {
	if p.useDisk {
		return p.dPiece.WriteAt(b, off)
	}
	return p.mPiece.WriteAt(b, off)
}

func (p *Piece) ReadAt(b []byte, off int64) (n int, err error) {
	if p.useDisk {
		return p.dPiece.ReadAt(b, off)
	}
	return p.mPiece.ReadAt(b, off)
}

// memCost is what this piece costs the cache budget. In RAM mode that is the allocated buffer
// (a whole piece from the first written byte), not the bytes received; Size stays the logical
// downloaded size used for progress reporting. Disk pieces cost their file size.
func (p *Piece) memCost() int64 {
	if !p.useDisk && p.mPiece != nil {
		return p.mPiece.allocated()
	}
	return p.Size
}

func (p *Piece) MarkComplete() error {
	// Same lock the writers hold: the eviction worker reads Complete while a
	// download may be marking pieces complete, and reading it unlocked is a data
	// race (found by -race, 2026-10-06).
	p.lockState()
	p.Complete = true
	p.unlockState()
	return nil
}

func (p *Piece) MarkNotComplete() error {
	p.lockState()
	p.Complete = false
	p.unlockState()
	return nil
}

// lockState/unlockState take the mutex that WriteAt/Release hold, so that a
// reader can sample the piece fields without racing a concurrent write.
//
// The fields live on Piece but the lock belongs to the storage impl (DiskPiece
// or MemPiece), which is the lock every writer already takes.
func (p *Piece) lockState() {
	switch {
	case p.useDisk && p.dPiece != nil:
		p.dPiece.mu.Lock()
	case p.mPiece != nil:
		p.mPiece.mu.Lock()
	}
}

func (p *Piece) unlockState() {
	switch {
	case p.useDisk && p.dPiece != nil:
		p.dPiece.mu.Unlock()
	case p.mPiece != nil:
		p.mPiece.mu.Unlock()
	}
}

// state samples the fields eviction and accounting need, under the writer's
// lock. Without it the cleanup worker read Piece.Size while a piece was being
// written to disk — a plain data race that `go test -race` rejects.
func (p *Piece) state() (size int64, complete bool, accessed int64) {
	p.lockState()
	size, complete, accessed = p.Size, p.Complete, p.Accessed
	p.unlockState()
	return
}

func (p *Piece) Completion() storage.Completion {
	return storage.Completion{
		Complete: p.Complete,
		Ok:       true,
	}
}

func (p *Piece) Release() {
	// Same rule as WriteAt/ReadAt: the backend the piece was created with.
	if p.useDisk {
		if p.dPiece != nil {
			p.dPiece.Release()
		}
	} else if p.mPiece != nil {
		p.mPiece.Release()
	}
	// Torrent may be nil during init/teardown or in tests — never panic here.
	// (Aug 29 panics: nil pointer via cache.go during stream.)
	if p.cache != nil && p.cache.torrent != nil {
		tp := p.cache.torrent.Piece(p.Id)
		// The storage just dropped these bytes, but the client may still count
		// the piece's chunks as received and serve zeros to a re-reading viewer.
		// Re-pend them before telling the client the piece is not complete
		// (YouROK/TorrServer#925).
		tp.InvalidateStorage()
		tp.SetPriority(torrent.PiecePriorityNone)
		tp.UpdateCompletion()
	}
}
