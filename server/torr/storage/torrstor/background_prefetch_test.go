package torrstor

import "testing"

// backgroundFetchEnd is how far past the current file the background fetch
// reaches: one prefetch window into the next episode, so it opens instantly.
// These cases use the live shapes: 4 MB pieces (18852-piece season) and 8 MB
// pieces (2359-piece film).
func TestBackgroundFetchEnd(t *testing.T) {
	cases := []struct {
		name        string
		fileEnd     int
		pieceCount  int
		pieceLength int64
		want        int
	}{
		// 4 MB pieces: 256 MB prefetch is 64 pieces past the file end.
		{"4MB pieces prefetch 64 past", 1000, 18852, 4 << 20, 1064},
		// 8 MB pieces: 256 MB prefetch is 32 pieces past.
		{"8MB pieces prefetch 32 past", 500, 2359, 8 << 20, 532},
		// Clamped at the torrent end: the prefetch must not run past the
		// last piece, even when the file ends near it.
		{"clamped at torrent end", 18800, 18852, 4 << 20, 18852},
		{"file is the whole torrent", 512, 512, 4 << 20, 512},
		// Degenerate inputs never go backwards.
		{"file end past count clamps", 20000, 18852, 4 << 20, 18852},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := backgroundFetchEnd(c.fileEnd, c.pieceCount, c.pieceLength); got != c.want {
				t.Errorf("got %d, want %d", got, c.want)
			}
		})
	}
}

// The prefetch must be a small fraction of a season: 64 pieces past a file
// end in an 18852-piece torrent is 0.3%, not a second full download.
func TestBackgroundPrefetchStaysSmall(t *testing.T) {
	end := backgroundFetchEnd(1000, 18852, 4<<20)
	past := end - 1000
	fraction := float64(past) / 18852 * 100
	t.Logf("prefetch %d pieces past file end = %.2f%% of an 18852-piece season", past, fraction)
	if fraction > 1 {
		t.Errorf("prefetch is %.2f%% of the torrent, want under 1%%", fraction)
	}
}
