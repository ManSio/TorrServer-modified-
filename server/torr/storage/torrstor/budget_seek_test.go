package torrstor

import (
	"testing"
	"time"

	"server/settings"
)

// TestBudgetBackwardSeekMovesProtection — резкая перемотка с середины в начало
// при лимите 1 GiB и фильме 5 GiB.
//
// Окно защиты не хранит историю просмотренных участков: оно вычисляется заново
// из r.offset в каждом цикле уборки. Значит перемотка должна одновременно
// (1) снять защиту со старого участка и (2) защитить новый.
//
// Здесь перемотка моделируется напрямую присваиванием rd.offset — именно так Seek и
// поступает после делегирования настоящему клиенту (reader.go:65), но без
// обращения к anacrolix-читателю: он там ждёт доступности куска и в тесте
// без пиров встал бы колом.
func TestBudgetBackwardSeekMovesProtection(t *testing.T) {
	initTestSettings(t)
	settings.BTsets.UseDisk = false

	const (
		pieceLen  = int64(8 << 20)
		torrentSz = int64(5 << 30)
		budget    = int64(1 << 30)
		readahead = int64(90 << 20)
		loaded    = 130
	)
	target := budget * 9 / 10

	file := fakeBigFile(t, torrentSz, pieceLen)

	stor := NewStorage(budget)
	defer stor.Close()
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

	rd := c.NewReader(file)
	rd.offset = 500 << 20
	rd.readahead = readahead
	rd.lastAccess = now
	rd.isUse = true

	mid := rd.getPiecesRange() // окно на середине
	if mid.Start < 1 || mid.End >= loaded {
		t.Fatalf("подготовка: окно %d..%d выходит за скачанное %d", mid.Start, mid.End, loaded)
	}
	t.Logf("окно на середине: куски %d..%d", mid.Start, mid.End)

	aliveIn := func(r Range) int {
		n := 0
		for id := r.Start; id <= r.End; id++ {
			if p := c.getPieces()[id]; p != nil && p.Size > 0 {
				n++
			}
		}
		return n
	}

	// ---- Фаза 1. смотрим середину, кэш выше лимита ----
	// куски вне окна зрителя — старые (в реальности это начало файла:
	// при последовательной загрузке оно самое старое и вытесняется первым)
	for _, p := range c.getPieces() {
		if p.Size > 0 {
			p.Accessed = now - 3600
		}
	}
	stor.triggerGlobalCleanup(target)

	if got := aliveIn(mid); got != mid.End-mid.Start+1 {
		t.Fatalf("окно на середине должно быть целым: %d из %d", got, mid.End-mid.Start+1)
	}
	headBefore := aliveIn(Range{0, 12, file})
	t.Logf("фаза 1: смотрим середину (куски %d..%d) — в начале файла осталось %d из 13 кусков",
		mid.Start, mid.End, headBefore)

	// ---- Фаза 2. резкая перемотка в начало ----
	rd.offset = 0
	rd.lastAccess = time.Now().Unix()
	head := rd.getPiecesRange()
	t.Logf("фаза 2: окно переехало на куски %d..%d, но данные в кэше уже вытеснены (%d из 13)",
		head.Start, head.End, headBefore)
	if head.Start != 0 || head.End != 12 {
		t.Errorf("окно после перемотки ожидалось 0..12, получено %d..%d", head.Start, head.End)
	}

	// клиент узнаёт о пропаже через Release()->UpdateCompletion() и
	// перекачивает голову заново — моделируем повторную загрузку
	for i := 0; i <= head.End; i++ {
		p := c.getPieces()[i]
		if p == nil || p.Size > 0 {
			continue
		}
		p.Size = pieceLen
		p.Complete = true
		p.Accessed = time.Now().Unix()
		p.mPiece.buffer = shared
		c.filledSize.Add(pieceLen)
		stor.addFilledSize(pieceLen)
	}
	if total := stor.totalOnDisk(); total <= target {
		// Фаза 1 не тронула начало файла — перекачивать нечего, и кэш стоит
		// ровно на пороге. Это допустимо: фаза 3 всё равно имеет работу, потому
		// что её цель намеренно ниже (см. phase3Target ниже).
		t.Logf("подготовка фазы 3: кэш %d МБ ровно на пороге, перекачивать нечего", total>>20)
	}
	t.Logf("фаза 3: голова перекачана заново, кэш %d МБ", stor.totalOnDisk()>>20)

	// Цель для фазы 3 опущена намеренно: если перекачивать было нечего (фаза 1
	// могла не тронуть начало файла), кэш уже под порогом, цикл уборки не
	// запустится и утверждение «середина потеряла защиту» окажется
	// непроверяемым. Опуская цель, заставляем цикл снять ровно 10 кусков.
	phase3Target := target - 10*pieceLen

	// ---- Фаза 3. уборка после перемотки ----
	// Новое окно только что записано. Остальное старше 300 c, причём
	// старая середина — самая старая: если бы окно помнило прошлую
	// позицию, вытеснение пошло бы с других кусков и середина уцелела бы.
	for _, p := range c.getPieces() {
		if p.Size == 0 || (p.Id >= head.Start && p.Id <= head.End) {
			continue
		}
		if p.Id >= mid.Start && p.Id <= mid.End {
			p.Accessed = now - 9000
		} else {
			p.Accessed = now - 3600
		}
	}
	stor.lastCleanupTime.Store(0)
	stor.triggerGlobalCleanup(phase3Target)

	gotHead := aliveIn(head)
	gotMid := aliveIn(mid)
	t.Logf("после уборки: голова %d/%d жива, середина %d/%d жива",
		gotHead, head.End-head.Start+1, gotMid, mid.End-mid.Start+1)

	if gotHead != head.End-head.Start+1 {
		t.Errorf("голова зрителя потеряна: %d из %d — защита не переехала на новую позицию",
			gotHead, head.End-head.Start+1)
	}
	if gotMid == mid.End-mid.Start+1 {
		t.Error("старая середина всё ещё защищена — окно не должно помнить прошлые позиции")
	}
	var real int64
	for _, p := range c.getPieces() {
		real += p.Size
	}
	if g := stor.getFilledSize(); g != real {
		t.Errorf("счётчик разошёлся: %d МБ против реальных %d МБ", g>>20, real>>20)
	}
	if total := stor.totalOnDisk(); total > phase3Target {
		t.Errorf("не сошлось к порогу фазы 3: %d МБ > %d МБ", total>>20, phase3Target>>20)
	}
}
