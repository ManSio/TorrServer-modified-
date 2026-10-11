package torrstor

import "testing"

// backgroundFetchEnd reaches two episodes past the current file, so the next
// ones are there when the viewer moves on. Two file lengths, not a fixed
// megabyte count. Shapes: 4 MB pieces with a 2 GB episode (512 pieces),
// 8 MB pieces with a 4 GB episode (512 pieces).
func TestBackgroundFetchEnd(t *testing.T) {
	const (
		piece4M = int64(4) << 20
		piece8M = int64(8) << 20
		ep2G    = int64(2) << 30
		ep4G    = int64(4) << 30
	)
	cases := []struct {
		name        string
		fileEnd     int
		pieceCount  int
		pieceLength int64
		fileLength  int64
		want        int
	}{
		// 2 GB episode, 4 MB pieces: 2 files ahead is 1024 pieces past.
		{"2GB episode prefetches two ahead", 1000, 18852, piece4M, ep2G, 2024},
		// 4 GB episode, 8 MB pieces: 2 files ahead is 1024 pieces past.
		{"4GB episode prefetches two ahead", 500, 2359, piece8M, ep4G, 1524},
		// Clamped at the torrent end.
		{"clamped at torrent end", 18000, 18852, piece4M, ep2G, 18852},
		{"file is the whole torrent", 512, 512, piece4M, ep2G, 512},
		{"file end past count clamps", 20000, 18852, piece4M, ep2G, 18852},
		// A zero-length file still moves one piece, never backwards.
		{"empty file moves one piece", 100, 18852, piece4M, 0, 101},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := backgroundFetchEnd(c.fileEnd, c.pieceCount, c.pieceLength, c.fileLength)
			if got != c.want {
				t.Errorf("got %d, want %d", got, c.want)
			}
		})
	}
}

// Two episodes ahead in an 18852-piece season is ~5%: the cost of always
// having the next ones ready, bounded and explicit, not a runaway.
func TestBackgroundPrefetchStaysBounded(t *testing.T) {
	end := backgroundFetchEnd(1000, 18852, 4<<20, 2<<30)
	past := end - 1000
	fraction := float64(past) / 18852 * 100
	t.Logf("prefetch %d pieces past file end = %.2f%% of an 18852-piece season", past, fraction)
	if fraction > 10 {
		t.Errorf("prefetch is %.2f%% of the torrent, want under 10%%", fraction)
	}
	if past <= 0 {
		t.Error("prefetch reaches nowhere past the file end")
	}
}
