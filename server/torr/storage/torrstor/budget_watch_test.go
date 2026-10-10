package torrstor

import (
	"testing"
	"time"

	"github.com/anacrolix/torrent"
	"github.com/anacrolix/torrent/bencode"
	"github.com/anacrolix/torrent/metainfo"

	"server/settings"
)

// newTestClient поднимает изолированный клиент без сети: вытеснение зовёт
// file.Length()/file.Offset(), поэтому подделать структурой нельзя — нужен
// настоящий объект клиента.
func newTestClient(t *testing.T) (*torrent.Client, error) {
	t.Helper()
	stor := NewStorage(1 << 30)
	// The storage owns a cleanupLoop goroutine that reads settings.BTsets on
	// every tick. Left open it outlives the test and the next test's writes of
	// that global race with it — which is what the race detector reported.
	t.Cleanup(func() { stor.Close() })
	return torrent.NewClient(&torrent.ClientConfig{
		DataDir:                 t.TempDir(),
		DisableTCP:              true,
		DisableUTP:              true,
		DisableIPv4:             true,
		DisableIPv6:             true,
		DisableTrackers:         true,
		NoDHT:                   true,
		NoDefaultPortForwarding: true,
		Seed:                    false,
		DefaultStorage:          stor,
	})
}

// fakeBigFile создаёт настоящий *torrent.File длиной length через
// настоящий anacrolix-клиент: evictPiecesTo вызывает r.getPiecesRange(),
// а тому нужен живой file.Length()/file.Offset() — подделать его структурой
// нельзя, только настоящим объектом клиента.
func fakeBigFile(t *testing.T, length, pieceLen int64) *torrent.File {
	t.Helper()

	info := metainfo.Info{
		Name:        "movie-5gib.mkv",
		PieceLength: pieceLen,
		Length:      length,
		Pieces:      make([]byte, 20*int(length/pieceLen)),
	}
	mi := metainfo.MetaInfo{InfoBytes: bencode.MustMarshal(info)}

	cl, err := newTestClient(t)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer cl.Close()

	tor, err := cl.AddTorrent(&mi)
	if err != nil {
		t.Fatalf("AddTorrent: %v", err)
	}

	files := tor.Files()
	if len(files) != 1 {
		t.Fatalf("ожидался 1 файл, получено %d", len(files))
	}
	if files[0].Length() != length {
		t.Fatalf("длина файла %d, ожидалась %d", files[0].Length(), length)
	}
	return files[0]
}

// TestBudgetOneGiBWhileWatching — вопрос «лимит 1 GiB, фильм 5 GiB, идёт
// просмотр и скачивание»: что делает вытеснение?
//
// Три проверки, каждая на реальном вызове triggerGlobalCleanup:
//
//	A. куски только что записаны (свежие) → вытеснение заблокировано
//	    Guard-ом «не трогать моложе 5 минут», кэш остаётся выше лимита;
//	B. те же куски состарились → вытеснение идёт и сходится к порогу;
//	C. окно зрителя — САМЫЕ старые куски, поэтому LRU начал бы с них;
//	    Guard защиты читателя обязан удержать их при любом порядке.
//
// Окно защиты задаётся абсолютными мегабайтами readahead, а не долей от
// лимита, поэтому при бюджете меньше фильма оно ничтожно мало — тест это
// фиксирует числами.
func TestBudgetOneGiBWhileWatching(t *testing.T) {
	initTestSettings(t)
	settings.BTsets.UseDisk = false

	const (
		pieceLen  = int64(8 << 20) // 8 MiB — размер куска как в вопросе
		torrentSz = int64(5 << 30) // 5 GiB — «фильм»
		budget    = int64(1 << 30) // 1 GiB — лимит кэша
		readahead = int64(90 << 20)
		loaded    = 130 // 130 * 8 MiB = 1016 MiB скачано
	)
	target := budget * 9 / 10 // порог 90 %

	file := fakeBigFile(t, torrentSz, pieceLen)
	numPieces := int(torrentSz / pieceLen)

	stor := NewStorage(budget)
	defer stor.Close()
	stor.lastCleanupTime.Store(0)

	c := NewCache(budget, stor)
	c.pieceLength = pieceLen
	c.pieceCount = numPieces
	c.pieces = make(map[int]*Piece, numPieces)

	// ---- A. свежие куски: неполными их не трогают даже под давлением ----
	//
	// Помечаем куски НЕполными (Complete=false): это и есть случай «идёт
	// загрузка, всё свежее». Первый проход блокирует их правилом 5 минут,
	// второй проход — правилом «только полностью скачанные». Поэтому при
	// переполнении не остаётся ничего, и сервер честно пишет OVER LIMIT, а не
	// удаляет байты, которые клиент считает полученными (это дало бы нули и
	// провал хеша). Свежие ПОЛНЫЕ куски при этом вытесняются — отдельный
	// случай, см. TestSecondPassConvergesDuringDownload.
	now := time.Now().Unix()
	shared := make([]byte, pieceLen)
	for i := 0; i < loaded; i++ {
		p := NewPiece(i, c)
		p.Size = pieceLen
		p.Complete = false
		p.Accessed = now // только что записано — как при активной загрузке
		p.mPiece.buffer = shared
		c.pieces[i] = p
		c.filledSize.Add(pieceLen)
		stor.addFilledSize(pieceLen)
	}
	stor.caches[[20]byte{7}] = c

	// зритель смотрит с середины файла
	rd := c.NewReader(file)
	rd.offset = 500 << 20
	rd.readahead = readahead
	rd.lastAccess = now
	rd.isUse = true

	// окно защиты считает сам код: getPiecesRange = [offset-piece, offset+ra+piece]
	win := rd.getPiecesRange()
	t.Logf("окно защиты: куски %d..%d (из %d), readahead %d МБ, лимит %d МБ",
		win.Start, win.End, numPieces, readahead>>20, budget>>20)
	if win.End <= win.Start {
		t.Fatalf("окно защиты выродилось: %d..%d", win.Start, win.End)
	}
	if win.End >= loaded {
		t.Fatalf("окно (%d..%d) выходит за пределы скачанного (%d) — тест ничего не проверяет",
			win.Start, win.End, loaded)
	}

	alive := func() (inWin, outWin int, total int64) {
		for _, p := range c.getPieces() {
			if p.Size == 0 {
				continue
			}
			total += p.Size
			if p.Id >= win.Start && p.Id <= win.End {
				inWin++
			} else {
				outWin++
			}
		}
		return
	}

	// ---- A. свежие куски: вытеснение обязано быть заблокировано ----
	if total := stor.totalOnDisk(); total <= target {
		t.Fatalf("подготовка: %d МБ должно быть выше порога %d МБ", total>>20, target>>20)
	}
	_, _, before := alive()
	stor.triggerGlobalCleanup(target)
	inWinA, outWinA, afterA := alive()
	if afterA != before {
		t.Errorf("A: вытеснено %d МБ свежих кусков — Guard 5 минут не сработал",
			(before-afterA)>>20)
	}
	if afterA <= target {
		t.Errorf("A: кэш %d МБ не должен опуститься при свежих кусках (порог %d МБ)",
			afterA>>20, target>>20)
	}
	t.Logf("A: свежие куски — вытеснение заблокировано, кэш %d МБ при лимите %d МБ (окно %d, вне %d)",
		stor.totalOnDisk()>>20, budget>>20, inWinA, outWinA)

	// ---- B. куски состарились: вытеснение идёт и сходится к порогу ----
	// окно зрителя — САМЫЕ старые, чтобы LRU начал именно с него
	for _, p := range c.getPieces() {
		if p.Size > 0 {
			p.Complete = true // загрузка закончилась, куски полные
			p.Accessed = now - 3600
			if p.Id < win.Start || p.Id > win.End {
				p.Accessed = now - 600 // вне окна — но тоже старше 300 c
			}
		}
	}
	_, _, beforeB := alive()
	stor.lastCleanupTime.Store(0) // дебаунс 5 c между циклами иначе проглотит вызов
	stor.triggerGlobalCleanup(target)
	inWin, outWin, total := alive()
	freed := beforeB - total
	if freed == 0 {
		t.Fatal("B: ничего не вытеснено — вытеснение не работает на старых кусках")
	}
	if total > target {
		t.Errorf("B: не сошлось к порогу: %d МБ > %d МБ", total>>20, target>>20)
	}
	t.Logf("B: вытеснено %d МБ, осталось %d МБ (окно %d, вне окна %d)",
		freed>>20, total>>20, inWin, outWin)

	// ---- C. окно зрителя выжило, хотя было самым старым ----
	if inWin != win.End-win.Start+1 {
		t.Errorf("C: окно зрителя потеряно: живы %d из %d кусков (диапазон %d..%d)",
			inWin, win.End-win.Start+1, win.Start, win.End)
	}
	for id := win.Start; id <= win.End; id++ {
		if p := c.getPieces()[id]; p != nil && p.Size == 0 {
			t.Errorf("C: кусок %d в окне зрителя вытеснен", id)
		}
	}
	if outWin == 0 {
		t.Error("C: вне окна не вытеснено ничего — вытеснение шло только по окну?")
	}
	// счётчик обязан совпадать с реальностью
	var real int64
	for _, p := range c.getPieces() {
		real += p.Size
	}
	if got := stor.getFilledSize(); got != real {
		t.Errorf("C: счётчик разошёлся: счётчик=%d МБ реально=%d МБ", got>>20, real>>20)
	}
	t.Logf("C: окно зрителя цело (%d кусков), вытеснено %d, счётчик сошёлся", inWin, outWin)
}
