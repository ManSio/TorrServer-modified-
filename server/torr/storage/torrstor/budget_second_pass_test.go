package torrstor

import (
	"testing"
	"time"

	"server/settings"
)

// TestSecondPassConvergesDuringDownload — загрузка идёт, кэш переполнен.
//
// Правило «не трогать куски моложе 5 минут» при активной загрузке означает,
// что у каждого только что записанного куска Accessed=сейчас, поэтому первый
// проход не находит ничего и кэч остаётся выше лимита (вместо честной
// сходимости). Второй проход смягчает свежесть, но только по полностью
// скачанным кускам: частичный кусок удалять нельзя, иначе клиент засчитает
// эти байты полученными, читатель получит нули, а проверка хеша провалится.
func TestSecondPassConvergesDuringDownload(t *testing.T) {
	initTestSettings(t)
	settings.BTsets.UseDisk = false

	const (
		pieceLen = int64(8 << 20)
		budget   = int64(1 << 30)
		loaded   = 130
	)
	target := budget * 9 / 10
	now := time.Now().Unix()

	stor := NewStorage(budget)
	defer stor.Close()
	stor.lastCleanupTime.Store(0)

	c := NewCache(budget, stor)
	c.pieceLength = pieceLen
	c.pieceCount = loaded
	c.pieces = make(map[int]*Piece, loaded)

	shared := make([]byte, pieceLen)
	// Первые 100 кусков полностью скачаны, последние 30 пишутся прямо сейчас:
	// Accessed=now у всех — как при активной загрузке.
	for i := 0; i < loaded; i++ {
		p := NewPiece(i, c)
		p.Size = pieceLen
		p.Complete = i < 100
		p.Accessed = now
		p.mPiece.buffer = shared
		c.pieces[i] = p
		c.filledSize.Add(pieceLen)
		stor.addFilledSize(pieceLen)
	}
	stor.caches[[20]byte{3}] = c

	before := stor.totalOnDisk()
	stor.triggerGlobalCleanup(target)
	after := stor.totalOnDisk()

	completeAlive, partialAlive := 0, 0
	for i := 0; i < loaded; i++ {
		p := c.getPieces()[i]
		if p == nil || p.Size == 0 {
			continue
		}
		if p.Complete {
			completeAlive++
		} else {
			partialAlive++
		}
	}
	t.Logf("было %d МБ, стало %d МБ (порог %d МБ); полных кусков живо %d, частичных %d",
		before>>20, after>>20, target>>20, completeAlive, partialAlive)

	if after >= before {
		t.Errorf("второй проход не сработал: %d МБ → %d МБ при загрузке", before>>20, after>>20)
	}
	if after > target {
		t.Errorf("не сошлось к порогу при активной загрузке: %d МБ > %d МБ", after>>20, target>>20)
	}
	if partialAlive != 30 {
		t.Errorf("частичные куски тронуты: %d из 30 — их удаление даёт нули и провал хеша", 30-partialAlive)
	}
	if completeAlive == 0 {
		t.Error("все полные куски вытеснены — второй проход должен сохранять хоть что-то")
	}
}
