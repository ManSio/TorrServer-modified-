package torrstor

import (
	"testing"
	"time"

	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"server/settings"
)

// TestProtectionCanExceedBudget — структурный дефект масштабирования защиты.
//
// Окно зрителя считается как readahead, ограниченный долей объёма этого кэша
// (torrent.go:236: cacheCapacity/readers), но cacheCapacity — это ГЛОБАЛЬНЫЙ
// лимит кэша. Поэтому каждый фильм со своим зрителем получает окно, посчитанное
// от полного лимита, и сумма окон растёт с числом зрителей:
//
//	1 зритель  → защищено ≈ 1 × лимит
//	2 зрителя  → защищено ≈ 2 × лимит
//
// Вытеснение не может опуститься ниже суммы защищённых байт, поэтому кэш
// застревает выше лимита настолько, насколько много зрителей смотрят одновременно.
// Этот тест фиксирует текущее (нежелательное) поведение цифрами — он будет
// падать, когда защита станет глобально ограниченной.
// windowAlive counts how many pieces of a viewer window are still in cache.
func windowAlive(c *Cache, r Range) int {
	n := 0
	for i := r.Start; i <= r.End; i++ {
		if p := c.getPieces()[i]; p != nil && p.Size > 0 {
			n++
		}
	}
	return n
}

func TestProtectionCanExceedBudget(t *testing.T) {
	initTestSettings(t)
	settings.BTsets.UseDisk = false

	const (
		pieceLen  = int64(8 << 20)
		torrentSz = int64(5 << 30)
		budget    = int64(1 << 30) // 1 GiB
		perFilm   = 130            // по 130 кусков = 1016 МиБ на фильм
	)
	target := budget * 9 / 10
	now := time.Now().Unix()

	stor := NewStorage(budget)
	defer stor.Close()
	stor.lastCleanupTime.Store(0)

	shared := make([]byte, pieceLen)

	// три фильма, три телевизера — каждый в своей части файла
	type film struct {
		c    *Cache
		rd   *Reader
		win  Range
		hash metainfo.Hash
	}

	var films []film

	for n := 0; n < 3; n++ {
		name := "film-" + string(rune('A'+n))
		info := metainfo.Info{
			Name:        name,
			PieceLength: pieceLen,
			Length:      torrentSz,
			Pieces:      make([]byte, 20*int(torrentSz/pieceLen)),
		}
		mi := metainfo.MetaInfo{InfoBytes: bencode.MustMarshal(info)}

		cl, err := newTestClient(t)
		if err != nil {
			t.Fatalf("клиент для %s: %v", name, err)
		}
		tor, err := cl.AddTorrent(&mi)
		if err != nil {
			t.Fatalf("AddTorrent %s: %v", name, err)
		}
		file := tor.Files()[0]

		c := NewCache(budget, stor)
		c.pieceLength = pieceLen
		c.pieceCount = int(torrentSz / pieceLen)
		c.pieces = make(map[int]*Piece, c.pieceCount)
		for i := 0; i < perFilm; i++ {
			p := NewPiece(i, c)
			p.Size = pieceLen
			p.Complete = true
			p.Accessed = now - 3600 // всё старше 5-минутного guard'а
			p.mPiece.buffer = shared
			c.pieces[i] = p
			c.filledSize.Add(pieceLen)
			stor.addFilledSize(pieceLen)
		}
		key := metainfo.Hash{}
		key[0] = byte(n + 1)
		stor.caches[key] = c

		// зритель с окном ~600 МиБ: как при ReaderReadAheadMB=1024 и малом лимите
		rd := c.NewReader(file)
		rd.offset = 500 << 20
		rd.readahead = 592 << 20
		rd.lastAccess = now
		rd.isUse = true

		films = append(films, film{c: c, rd: rd, win: rd.getPiecesRange(), hash: key})
	}

	// считаем защищённые байты так же, как это делает вытеснение
	var protectedBytes int64
	for _, f := range films {
		protectedBytes += int64(f.win.End-f.win.Start+1) * pieceLen
	}
	t.Logf("лимит %d МБ, порог %d МБ", budget>>20, target>>20)
	t.Logf("всего на диске: %d МБ", stor.totalOnDisk()>>20)
	t.Logf("защищено зрителями: %d МБ (%d фильмов × окно %d МБ)",
		protectedBytes>>20, len(films), protectedBytes>>20/int64(len(films)))

	// Прогоняем циклы уборки до сходимости, как это делает сервер раз в 5 c:
	// один цикл ограничен 2 ГиБ (maxRemoveBytesPerCycle), поэтому замер за один
	// проход показывал бы не предел системы, а предел итерации.
	var removed, freed int
	var total int64
	for cycle := 0; cycle < 12; cycle++ {
		stor.lastCleanupTime.Store(0)
		before := stor.totalOnDisk()
		stor.triggerGlobalCleanup(target)
		total = stor.totalOnDisk()
		if total <= target || total == before {
			break
		}
		freed += int(before - total)
		removed++
	}
	t.Logf("циклов уборки: %d, освобождено %d МБ", removed, freed>>20)
	overshoot := total - target
	t.Logf("после уборки: %d МБ — это %d %% от лимита (перебор %d МБ)",
		total>>20, total*100/budget, overshoot>>20)

	for _, f := range films {
		alive := 0
		for i := f.win.Start; i <= f.win.End; i++ {
			if p := f.c.getPieces()[i]; p != nil && p.Size > 0 {
				alive++
			}
		}
		t.Logf("  окно фильма: %d/%d кусков живо", alive, f.win.End-f.win.Start+1)
	}

	if total > budget {
		t.Errorf("cache stayed ABOVE LIMIT: %d MB with limit %d MB (viewers %d) - protection cap did not bind",
			total>>20, budget>>20, len(films))
	}
	if protectedBytes > target {
		t.Logf("initially: window sum %d MB > target %d MB - without the cap eviction cannot reach the limit",
			protectedBytes>>20, target>>20)
	}
	if overshoot := total - target; overshoot > 0 {
		t.Logf("residue above target: %d MB = per-viewer floor plus file head/tail protection",
			overshoot>>20)
	}
	for i, f := range films {
		if windowAlive(f.c, f.win) == 0 {
			t.Errorf("film %d: viewer window wiped entirely - every viewer must keep a minimum", i)
		}
	}
	t.Log("OK: cache inside the limit with three viewers on three films")
}
