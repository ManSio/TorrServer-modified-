package torrstor

import (
	"testing"
)

// The allocator is the "smart model" behind viewer protection: it decides who
// gets how much of a shared cache. These tests drive the pure function, so they
// are deterministic and need no torrent, client or disk.

func win(want, floor int64, active bool) viewerWindow {
	return viewerWindow{
		rng:    Range{Start: 0, End: 100},
		active: active,
		want:   want,
		floor:  floor,
	}
}

func sum(v []int64) int64 {
	var t int64
	for _, x := range v {
		t += x
	}
	return t
}

// The whole point of the model: eviction can always reach its target.
func TestAllocateNeverExceedsBudget(t *testing.T) {
	const mb = int64(1 << 20)
	cases := []struct {
		name    string
		budget  int64
		windows []viewerWindow
	}{
		{"one viewer wants everything", 100 * mb, []viewerWindow{win(900*mb, 16*mb, true)}},
		{"three equal", 100 * mb, []viewerWindow{
			win(600*mb, 16*mb, true), win(600*mb, 16*mb, true), win(600*mb, 16*mb, true)}},
		{"ten viewers", 100 * mb, func() []viewerWindow {
			var out []viewerWindow
			for i := 0; i < 10; i++ {
				out = append(out, win(400*mb, 8*mb, true))
			}
			return out
		}()},
		{"budget smaller than one window", 8 * mb, []viewerWindow{win(600*mb, 16*mb, true)}},
	}
	for _, c := range cases {
		got := allocateViewerBudget(c.windows, c.budget)
		if s := sum(got); s > c.budget {
			t.Errorf("%s: выдано %d МБ при бюджете %d МБ — сумма превысила бюджет",
				c.name, s>>20, c.budget>>20)
		}
	}
}

// Nobody may be starved below the floor while any budget is left.
func TestAllocateGrantsFloorFirst(t *testing.T) {
	const mb = int64(1 << 20)
	ws := []viewerWindow{win(500*mb, 32*mb, true), win(500*mb, 32*mb, true), win(500*mb, 32*mb, true)}
	got := allocateViewerBudget(ws, 200*mb) // 96 МБ на floors, 104 МБ остаётся
	for i, g := range got {
		if g < 32*mb {
			t.Errorf("зритель %d получил %d МБ — меньше floor 32 МБ", i, g>>20)
		}
	}
	if sum(got) > 200*mb {
		t.Errorf("выдано %d МБ при бюджете 200 МБ", sum(got)>>20)
	}
	t.Logf("floors 96 МБ + остаток: выдано %v МБ", func() []int64 {
		out := make([]int64, len(got))
		for i, g := range got {
			out[i] = g >> 20
		}
		return out
	}())
}

// Active playback outranks a paused viewer: the paused one keeps its floor and
// whatever is left, never the lion's share.
func TestAllocateActiveBeforeIdle(t *testing.T) {
	const mb = int64(1 << 20)
	ws := []viewerWindow{
		win(400*mb, 16*mb, true),  // активный
		win(400*mb, 16*mb, false), // пауза
		win(400*mb, 16*mb, false), // пауза
	}
	// Бюдет на 1.5 окна: активный должен получить своё целиком.
	got := allocateViewerBudget(ws, 600*mb)
	if got[0] != 400*mb {
		t.Errorf("активный получил %d МБ вместо 400 — пауза не должна отнимать у него буфер", got[0]>>20)
	}
	for _, i := range []int{1, 2} {
		if got[i] < 16*mb {
			t.Errorf("зритель на паузе %d получил %d МБ — меньше floor", i, got[i]>>20)
		}
	}
	t.Logf("активный %d МБ, на паузе %d и %d МБ (окно 400, бюджет 600)",
		got[0]>>20, got[1]>>20, got[2]>>20)
}

// Max-min fairness: a viewer that needs little takes only what it needs, and
// the rest goes to the ones that can use it.
func TestAllocateFairToSmallWant(t *testing.T) {
	const mb = int64(1 << 20)
	ws := []viewerWindow{
		win(20*mb, 8*mb, true), // маленькое желание
		win(400*mb, 8*mb, true),
		win(400*mb, 8*mb, true),
	}
	got := allocateViewerBudget(ws, 420*mb) // хватает на 20 + 400
	if got[0] > 20*mb {
		t.Errorf("малое окно получило %d МБ при желании 20 — лишнее не распределяется", got[0]>>20)
	}
	if sum(got) > 420*mb {
		t.Errorf("выдано %d МБ при бюджете 420 МБ", sum(got)>>20)
	}
	t.Logf("малый %d МБ (хотел 20), остальные %d и %d МБ (хотели по 400)",
		got[0]>>20, got[1]>>20, got[2]>>20)
}

// If the budget cannot even cover the floors, everyone still gets the floor and
// the shortfall is reported rather than hidden.
func TestAllocateFloorBound(t *testing.T) {
	const mb = int64(1 << 20)
	ws := []viewerWindow{
		win(100*mb, 32*mb, true), win(100*mb, 32*mb, true),
		win(100*mb, 32*mb, true), win(100*mb, 32*mb, true),
	}
	got := allocateViewerBudget(ws, 50*mb) // 128 МБ нужны, есть 50
	if sum(got) > 50*mb {
		t.Errorf("выдано %d МБ при бюджете 50 МБ", sum(got)>>20)
	}
	// Никто не должен остаться с нулем: это значило бы, что кусок под позицией
	// зрителя можно вытеснить и поток встанет.
	for i, g := range got {
		if g <= 0 {
			t.Errorf("зритель %d получил 0 — при нехватке бюджета делим поровну", i)
		}
	}
	t.Logf("бюджет 50 МБ при 4 floors по 32 МБ: выдано %v — зрителям без полного floors",
		func() []int64 {
			out := make([]int64, len(got))
			for i, g := range got {
				out[i] = g >> 20
			}
			return out
		}())
}

// No contention: everyone gets what they asked for and the rest stays unused.
func TestAllocateNoContention(t *testing.T) {
	const mb = int64(1 << 20)
	ws := []viewerWindow{win(100*mb, 16*mb, true), win(50*mb, 16*mb, false)}
	got := allocateViewerBudget(ws, 900*mb)
	if got[0] != 100*mb || got[1] != 50*mb {
		t.Errorf("без спора получили %d и %d МБ вместо 100 и 50", got[0]>>20, got[1]>>20)
	}
}

// Same input, same output — otherwise windows would grow and shrink every cycle.
func TestAllocateDeterministic(t *testing.T) {
	const mb = int64(1 << 20)
	ws := []viewerWindow{
		win(333*mb, 7*mb, true), win(111*mb, 7*mb, true),
		win(777*mb, 7*mb, false), win(55*mb, 7*mb, true),
	}
	first := allocateViewerBudget(ws, 500*mb)
	for i := 0; i < 5; i++ {
		again := allocateViewerBudget(ws, 500*mb)
		for k := range first {
			if first[k] != again[k] {
				t.Fatalf("распределение нестабильно: %d != %d", first[k], again[k])
			}
		}
	}
	t.Logf("стабильно: %d %d %d %d МБ", first[0]>>20, first[1]>>20, first[2]>>20, first[3]>>20)
}
