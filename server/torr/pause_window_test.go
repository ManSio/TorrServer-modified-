package torr

import (
	"testing"

	"server/settings"
)

// pauseWindowMB decides whether a silent viewer gets the pause window or the
// playing window. Getting this wrong is expensive in both directions: too eager
// and every seek buffers a gigabyte, too reluctant and the pause that motivated
// the whole feature buffers nothing beyond the tight window.
func TestPauseWindowMB(t *testing.T) {
	const (
		playing = 90
		paused  = 1024
	)
	cases := []struct {
		name      string
		playingMB int
		pauseMB   int
		idleSec   int64
		afterSec  int
		want      int
	}{
		{
			// Normal playback: someone read a moment ago. This must not change,
			// or a 90 MB window becomes a 1 GB one and every seek pays for it.
			name:      "active playback keeps the playing window",
			playingMB: playing, pauseMB: paused, idleSec: 0, afterSec: 15,
			want: playing,
		},
		{
			// Ordinary buffering between segments — silent, but not for long.
			name:      "a short gap below the threshold is not a pause",
			playingMB: playing, pauseMB: paused, idleSec: 14, afterSec: 15,
			want: playing,
		},
		{
			name:      "the threshold itself counts as paused",
			playingMB: playing, pauseMB: paused, idleSec: 15, afterSec: 15,
			want: paused,
		},
		{
			// The whole point: a paused viewer stops at 90 MB and never fetches
			// more (measured 2026-10-11 on the live server).
			name:      "a long silence switches to the pause window",
			playingMB: playing, pauseMB: paused, idleSec: 600, afterSec: 15,
			want: paused,
		},
		{
			// Disabled by configuration: 0 means "no pause preload", which is
			// what the pre-existing configs ask for by not having the key.
			name:      "pause preload disabled by zero",
			playingMB: playing, pauseMB: 0, idleSec: 600, afterSec: 15,
			want: playing,
		},
		{
			// Negative is how a JSON config can end up unset after a bad edit.
			name:      "negative pause window is ignored",
			playingMB: playing, pauseMB: -5, idleSec: 600, afterSec: 15,
			want: playing,
		},
		{
			// Misconfiguration: a pause window smaller than the playing one.
			// Applying it would mean pressing pause *reduces* buffering — the
			// opposite of what the user asked for by pausing.
			name:      "a pause window below the playing window never shrinks it",
			playingMB: playing, pauseMB: 16, idleSec: 600, afterSec: 15,
			want: playing,
		},
		{
			name:      "equal windows stay at that window",
			playingMB: 256, pauseMB: 256, idleSec: 600, afterSec: 15,
			want: 256,
		},
		{
			// afterSec 0 means "any silence counts", which is a legitimate
			// configuration for someone who always pauses deliberately.
			name:      "a zero threshold reacts to one second of silence",
			playingMB: playing, pauseMB: paused, idleSec: 1, afterSec: 0,
			want: paused,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := pauseWindowMB(c.playingMB, c.pauseMB, c.idleSec, c.afterSec)
			if got != c.want {
				t.Errorf("got %d MB, want %d MB", got, c.want)
			}
		})
	}
}

// The defaults must actually be a boost, or shipping the setting changes
// nothing and the paused viewer is still stuck at the playing window.
func TestPauseWindowDefaultIsLargerThanPlaying(t *testing.T) {
	sets := &settings.BTSets{}
	sets.ReaderReadAheadMB = settings.DefaultReadAheadMB
	sets.PausePreloadMB = settings.PausePreloadDefaultMB
	sets.PausePreloadAfterSec = settings.PausePreloadAfterDefaultSec
	if sets.PausePreloadMB <= sets.ReaderReadAheadMB {
		t.Fatalf("default pause window %d MB is not larger than the playing window %d MB",
			sets.PausePreloadMB, sets.ReaderReadAheadMB)
	}
	if sets.PausePreloadAfterSec <= 0 {
		t.Fatalf("default pause threshold %d s makes the feature fire during normal buffering",
			sets.PausePreloadAfterSec)
	}
	// And it must survive the clamps that run on every load, or the shipped
	// default is a value the server would never actually use.
	if sets.PausePreloadMB > settings.MaxReadAheadMB {
		t.Fatalf("default pause window %d MB exceeds MaxReadAheadMB %d MB and would be clamped",
			sets.PausePreloadMB, settings.MaxReadAheadMB)
	}
	t.Logf("playing %d MB, paused %d MB after %d s", sets.ReaderReadAheadMB, sets.PausePreloadMB, sets.PausePreloadAfterSec)
}
