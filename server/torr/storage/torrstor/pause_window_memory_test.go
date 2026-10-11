package torrstor

import "testing"

// The pause window is downloaded-but-unread data, so it is RAM, not disk
// cache. These cases use the live server's shape: a 3.4 GiB box and a 1 GiB
// requested window.
const (
	liveRAM      = int64(3_650_000_000) // ~3.4 GiB
	requestedWin = 1024                 // MB, the shipped default
)

func TestCapPauseWindowToMemory(t *testing.T) {
	cases := []struct {
		name    string
		pauseMB int
		viewers int
		mem     int64
		want    int
	}{
		{
			// The headline case: 1 GiB on 3.4 GiB of RAM. 25% of RAM is
			// ~870 MB, so the window is trimmed rather than allowed to eat a
			// third of the machine.
			name: "1 GiB window is trimmed to a quarter of RAM", pauseMB: requestedWin,
			viewers: 1, mem: liveRAM, want: 870,
		},
		{
			// The budget is shared, so two viewers watching get half each.
			name: "two viewers share the budget", pauseMB: requestedWin,
			viewers: 2, mem: liveRAM, want: 435,
		},
		{
			name: "three viewers share the budget", pauseMB: requestedWin,
			viewers: 3, mem: liveRAM, want: 290,
		},
		{
			// Small machines must not end up with a zero window, which would
			// mean the feature silently does nothing on a cheap board.
			name: "a tiny machine still gets a window", pauseMB: 512,
			viewers: 1, mem: 512 << 20, want: 128,
		},
		{
			// A window that already fits is returned untouched. The cap only
			// ever trims.
			name: "a window under the cap is unchanged", pauseMB: 64,
			viewers: 1, mem: liveRAM, want: 64,
		},
		{
			// 0 is how the feature is turned off. It must not be raised back
			// up to the budget, or disabling would not disable.
			name: "a disabled window stays disabled", pauseMB: 0,
			viewers: 1, mem: liveRAM, want: 0,
		},
		{
			name: "unknown memory does not cap", pauseMB: requestedWin,
			viewers: 1, mem: 0, want: requestedWin,
		},
		{
			name: "unknown viewer count does not cap", pauseMB: requestedWin,
			viewers: 0, mem: liveRAM, want: requestedWin,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := CapPauseWindowToMemory(c.pauseMB, c.viewers, c.mem)
			if got != c.want {
				t.Errorf("got %d MB, want %d MB", got, c.want)
			}
		})
	}
}

// The cap must never make the window larger than was asked for. Growing would
// mean the RAM check inflated the buffer instead of trimming it.
func TestCapNeverRaisesTheWindow(t *testing.T) {
	for _, mem := range []int64{1 << 30, 8 << 30, 256 << 30} {
		for _, viewers := range []int{1, 2, 8} {
			got := CapPauseWindowToMemory(requestedWin, viewers, mem)
			if got > requestedWin {
				t.Fatalf("mem %d, %d viewers: got %d MB, larger than the requested %d MB",
					mem, viewers, got, requestedWin)
			}
		}
	}
}

// A realistic 4K setup: a machine with plenty of RAM must still be able to ask
// for a 2 GiB window and get it, otherwise the cap turns into a new limit the
// user cannot configure around.
func TestCapAllowsLargeWindowOnLargeRAM(t *testing.T) {
	if got := CapPauseWindowToMemory(2048, 1, 32<<30); got != 2048 {
		t.Errorf("on 32 GiB a 2 GiB window came back as %d MB, want 2048", got)
	}
}

// The shipped default must be something this machine can actually survive.
// 3.4 GiB of RAM is the live server, not a hypothetical.
func TestDefaultPauseWindowFitsTheLiveMachine(t *testing.T) {
	got := CapPauseWindowToMemory(requestedWin, 1, liveRAM)
	if got <= 0 {
		t.Fatal("the default window is trimmed to nothing on the live machine")
	}
	if float64(got) > float64(requestedWin) {
		t.Fatalf("window grew to %d MB, it must only ever be trimmed", got)
	}
	t.Logf("requested %d MB, allowed %d MB on a %.1f GiB machine",
		requestedWin, got, float64(liveRAM)/(1<<30))
}

// AutoPauseWindowMB is what AutoTune feeds the pause logic instead of the
// written setting. These cases pin it to hardware the owner actually has.
func TestAutoPauseWindowMB(t *testing.T) {
	cases := []struct {
		name    string
		mem     int64
		viewers int
		want    int
	}{
		// The live server: 3.45 GiB, one viewer. A quarter of RAM.
		{"live machine one viewer", 3701067776, 1, 882},
		{"live machine two viewers share it", 3701067776, 2, 441},
		// A large box gets a large window with no configuration.
		{"32 GiB one viewer", 32 << 30, 1, 8192},
		// Unknown hardware falls back to the shipped default, not to zero.
		// Zero would mean "no buffer" on a machine that simply reports
		// nothing.
		{"unknown memory falls back", 0, 1, 1024},
		{"unknown viewers fall back", 3701067776, 0, 1024},
		// A tiny board still gets a window, never nothing, and the floor
		// holds when even a quarter of RAM is below it.
		{"tiny machine still gets a window", 256 << 20, 1, 64},
		{"floor holds on a tiny board", 8 << 20, 1, 4},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := AutoPauseWindowMB(c.mem, c.viewers); got != c.want {
				t.Errorf("got %d MB, want %d MB", got, c.want)
			}
		})
	}
}
