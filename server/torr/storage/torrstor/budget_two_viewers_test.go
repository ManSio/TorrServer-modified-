package torrstor

import (
	"testing"
	"time"

	"server/settings"
)

// scenarioTwoReaders готовит кэш с одним файлом на 5 GiB, лимит 1 GiB,
// 130 скачанных кусков по 8 MiB и ДВУМЯ зрителями на разных позициях.
type twoReaderScenario struct {
	stor   *Storage
	c      *Cache
	rd1    *Reader
	rd2    *Reader
	win1   Range
	win2   Range
	shared []byte
}

func newTwoReaderScenario(t *testing.T) *twoReaderScenario {
	t.Helper()
	initTestSettings(t)
	settings.BTsets.UseDisk = false

	const (
		pieceLen  = int64(8 << 20)
		torrentSz = int64(5 << 30)
		budget    = int64(1 << 30)
		loaded    = 130
	)
	file := fakeBigFile(t, torrentSz, pieceLen)

	stor := NewStorage(budget)
	t.Cleanup(func() { stor.Close() })
	stor.lastCleanupTime.Store(0)

	c := NewCache(budget, stor)
	c.pieceLength = pieceLen
	c.pieceCount = int(torrentSz / pieceLen)
	c.pieces = make(map[int]*Piece, c.pieceCount)

	now := time.Now().Unix()
	shared := make([]byte, pieceLen)
	for i := 0; i < loaded; i++ {
		p := NewPiece(i, c)
		p.Size = pieceLen
		p.Complete = true
		p.Accessed = now
		p.mPiece.buffer = shared
		c.pieces[i] = p
		c.filledSize.Add(pieceLen)
		stor.addFilledSize(pieceLen)
	}
	stor.caches[[20]byte{7}] = c

	s := &twoReaderScenario{stor: stor, c: c, shared: shared}

	// телевизор 1 — начало файла
	s.rd1 = c.NewReader(file)
	s.rd1.offset = 100 << 20
	s.rd1.readahead = 90 << 20
	s.rd1.lastAccess = now
	s.rd1.isUse = true

	// телевизор 2 — дальше по файлу, но в пределах скачанного (иначе окно
	// окажется за пределами кэша и тест будет проверять пустоту)
	s.rd2 = c.NewReader(file)
	s.rd2.offset = 700 << 20
	s.rd2.readahead = 90 << 20
	s.rd2.lastAccess = now
	s.rd2.isUse = true

	s.win1 = s.rd1.getPiecesRange()
	s.win2 = s.rd2.getPiecesRange()
	if s.win1.End >= loaded || s.win2.End >= loaded {
		t.Fatalf("окна должны лежать в скачанных кусках 0..%d, получено %d..%d и %d..%d — тест ничего не проверяет",
			loaded-1, s.win1.Start, s.win1.End, s.win2.Start, s.win2.End)
	}
	return s
}

func (s *twoReaderScenario) aliveIn(r Range) int {
	n := 0
	for id := r.Start; id <= r.End; id++ {
		if p := s.c.getPieces()[id]; p != nil && p.Size > 0 {
			n++
		}
	}
	return n
}

func (s *twoReaderScenario) age(id int, accessed int64) {
	if p := s.c.getPieces()[id]; p != nil && p.Size > 0 {
		p.Accessed = accessed
	}
}

func (s *twoReaderScenario) cleanup(target int64) {
	s.stor.lastCleanupTime.Store(0)
	s.stor.triggerGlobalCleanup(target)
}

// TestTwoViewersDifferentParts — один фильм смотрят два телевизора в разных
// частях. Окно защиты вычисляется mergeRange: пересекающиеся окна сливаются,
// разнесённые остаются отдельными. Проверяем, что защищены оба окна, а провал
// между ними — нет (иначе 3 GiB файла были бы объявлены защищёнными).
func TestTwoViewersDifferentParts(t *testing.T) {
	s := newTwoReaderScenario(t)
	target := int64(1<<30) * 9 / 10
	now := time.Now().Unix()

	t.Logf("окно ТВ1: куски %d..%d, окно ТВ2: куски %d..%d",
		s.win1.Start, s.win1.End, s.win2.Start, s.win2.End)
	if s.win1.End >= s.win2.Start {
		t.Fatalf("окна должны быть разнесены, иначе тест не проверяет слияние: %d..%d и %d..%d",
			s.win1.Start, s.win1.End, s.win2.Start, s.win2.End)
	}

	gapStart, gapEnd := s.win1.End+1, s.win2.Start-1
	for i := gapStart; i <= gapEnd; i++ {
		s.age(i, now-9000) // провал между телевизорами — самый старый
	}
	for i := 0; i < 130; i++ {
		if i < s.win1.Start || i > s.win1.End {
			if i < s.win2.Start || i > s.win2.End {
				if i < gapStart || i > gapEnd {
					s.age(i, now-3600)
				}
			}
		}
	}

	s.cleanup(target)

	a1, a2 := s.aliveIn(s.win1), s.aliveIn(s.win2)
	t.Logf("после уборки: ТВ1 %d/%d, ТВ2 %d/%d, провал %d..%d",
		a1, s.win1.End-s.win1.Start+1, a2, s.win2.End-s.win2.Start+1, gapStart, gapEnd)

	if a1 != s.win1.End-s.win1.Start+1 {
		t.Errorf("окно ТВ1 потеряно: %d из %d", a1, s.win1.End-s.win1.Start+1)
	}
	if a2 != s.win2.End-s.win2.Start+1 {
		t.Errorf("окно ТВ2 потеряно: %d из %d", a2, s.win2.End-s.win2.Start+1)
	}

	// провал обязан быть вытесняемым: если mergeRange слил разнесённые окна,
	// вытеснение шло бы в другом месте, и куски провала остались бы нетронутыми
	gapAlive, gapEvicted := 0, 0
	for i := gapStart; i <= gapEnd; i++ {
		if p := s.c.getPieces()[i]; p != nil && p.Size > 0 {
			gapAlive++
		} else {
			gapEvicted++
		}
	}
	t.Logf("провал %d..%d: вытеснено %d, уцелело %d", gapStart, gapEnd, gapEvicted, gapAlive)
	if gapEvicted == 0 {
		t.Errorf("провал между окнами не вытесняется ни один кусок — защита накрыла весь файл между зрителями")
	}
	if a1 == s.win1.End-s.win1.Start+1 && a2 == s.win2.End-s.win2.Start+1 && gapEvicted > 0 {
		t.Log("✅ оба окна защищены, провал между ними — нет")
	}
	if total := s.stor.totalOnDisk(); total > target {
		t.Errorf("не сошлось к порогу: %d МБ > %d МБ", total>>20, target>>20)
	}
}

// TestTwoViewersOnePaused — второй телевизор на паузе дольше 60 с при двух
// зрителях в кэше. checkReader паркует его (isUse=false), после чего его окно
// перестаёт быть защищённым, и куски старше 5 минут вытесняются — то есть на
// паузе телевизор теряет уже скачанное. Это класс инцидента «E03 @ 0/605».
func TestTwoViewersOnePaused(t *testing.T) {
	s := newTwoReaderScenario(t)
	target := int64(1<<30) * 9 / 10
	now := time.Now().Unix()

	// ТВ2 на паузе: lastAccess 2 минуты назад, чтений нет
	s.rd2.lastAccess = now - 120
	for i := s.win2.Start; i <= s.win2.End; i++ {
		s.age(i, now-9000) // и куски её окна — самые старые
	}
	for i := 0; i < 130; i++ {
		if i < s.win2.Start || i > s.win2.End {
			s.age(i, now-3600)
		}
	}

	// checkReader может дойти до настоящего Seek клиента — не вешаем тест
	done := make(chan struct{})
	go func() { s.rd2.checkReader(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Log("ВНИМАНИЕ: checkReader завис на Seek анаrolix-читателя (тест продолжается)")
	}
	t.Logf("после checkReader: ТВ2 isUse=%v (lastAccess %d c назад)", s.rd2.isUse, 120)

	s.cleanup(target)

	a1, a2 := s.aliveIn(s.win1), s.aliveIn(s.win2)
	t.Logf("после уборки: ТВ1 (смотрит) %d/%d, ТВ2 (на паузе) %d/%d",
		a1, s.win1.End-s.win1.Start+1, a2, s.win2.End-s.win2.Start+1)

	if a1 != s.win1.End-s.win1.Start+1 {
		t.Errorf("окно играющего ТВ1 потеряно: %d из %d", a1, s.win1.End-s.win1.Start+1)
	}
	if a2 != s.win2.End-s.win2.Start+1 {
		t.Errorf("пауза ТВ2 стоила окна: живы %d из %d — зритель на паузе обязан остаться защищённым (ViewerIdleGraceSec)",
			a2, s.win2.End-s.win2.Start+1)
	} else {
		t.Log("✅ пауза не стоила окна: зритель на паузе защищён")
	}
}
