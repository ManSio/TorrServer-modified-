package torrstor

import "testing"

// backgroundFetchEnd reaches two files ahead by index when the list resolves
// them: a 10 GB episode followed by 2 GB ones prefetches 4 GB, not 20 GB.
// When the list is unavailable it falls back to two lengths.
func TestBackgroundFetchEnd(t *testing.T) {
	const (
		piece4M = int64(4) << 20
		ep2G    = int64(2) << 30
		ep10G   = int64(10) << 30
	)
	cases := []struct {
		name        string
		fileEnd     int
		pieceCount  int
		pieceLength int64
		fileLength  int64
		nextEnd     int
		want        int
	}{
		// Exact file index wins: episodes 2+3 end at piece 2024.
		{"two files by index", 1000, 18852, piece4M, ep2G, 2024, 2024},
		// No list (0): two lengths heuristic, 2 GB file is 1024 pieces.
		{"fallback to two lengths", 1000, 18852, piece4M, ep2G, 0, 2024},
		// 10 GB episode by index: next two files end at 3000, not 20 GB out.
		{"10GB episode bounded by files", 500, 18852, piece4M, ep10G, 3000, 3000},
		// Same 10 GB without a list falls back to lengths (5120 pieces),
		// showing exactly why the index matters.
		{"10GB without list overreaches", 500, 18852, piece4M, ep10G, 0, 5620},
		{"clamped at torrent end", 18000, 18852, piece4M, ep2G, 19999, 18852},
		{"file is the whole torrent", 512, 512, piece4M, ep2G, 0, 512},
		{"empty file moves one piece", 100, 18852, piece4M, 0, 0, 101},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := backgroundFetchEnd(c.fileEnd, c.pieceCount, c.pieceLength, c.fileLength, c.nextEnd)
			if got != c.want {
				t.Errorf("got %d, want %d", got, c.want)
			}
		})
	}
}

// Two files ahead in a season is bounded and explicit: even a 10 GB episode
// pulls at most the next two files, never the whole 79 GB.
func TestBackgroundPrefetchStaysBounded(t *testing.T) {
	// Worst realistic shape: 10 GB episode at piece 500 in an 18852-piece
	// season, next two files end at 3000.
	end := backgroundFetchEnd(500, 18852, 4<<20, 10<<30, 3000)
	if end != 3000 {
		t.Fatalf("got %d, want exactly the second file's end 3000", end)
	}
	past := end - 500
	fraction := float64(past) / 18852 * 100
	t.Logf("two files ahead = %d pieces = %.2f%% of season", past, fraction)
	if fraction > 15 {
		t.Errorf("prefetch is %.2f%% of the torrent, want under 15%%", fraction)
	}
}
