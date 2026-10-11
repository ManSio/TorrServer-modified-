package torrstor

import (
	"testing"

	"github.com/anacrolix/torrent"
)

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

// nextTwoFilesEndPiece resolves the boundary itself, from real file spans.
// It previously took []*torrent.File, whose offset/length are unexported and
// have no constructor outside the library — so no test could ever call it and
// the two-files-ahead rule was unprotected. The mutation check caught this:
// neutering the file-list branch left the whole suite green.
//
// Season shape: 2 GB, then the 10 GB episode being watched, then two 2 GB
// ones. The bound must land at the end of the FIRST of the two 2 GB files.
func TestNextTwoFilesEndPiece(t *testing.T) {
	const g2 = int64(2) << 30
	const g10 = int64(10) << 30
	const p4m = int64(4) << 20

	// Offsets are back to back, like a real torrent's file list.
	spans := []fileSpan{
		{offset: 0, length: g2},
		{offset: g2, length: g10},        // the watched one, index 1
		{offset: g2 + g10, length: g2},   // index 2
		{offset: 2*g2 + g10, length: g2}, // index 3  <- boundary lands here
		{offset: 3*g2 + g10, length: g2}, // index 4, must NOT be reached
	}

	// The 10 GB episode ends at 12 GB; two files ahead ends at 16 GB.
	want := int((2*g2 + g10 + g2) / p4m)
	got := nextTwoFilesEndPiece(spans, g2, g10, p4m)
	if got != want {
		t.Fatalf("two files ahead = %d pieces, want %d", got, want)
	}
	// The length heuristic would have said 12 + 20 = 32 GB. This is the bug.
	heuristic := int((g2 + g10 + g10 + 2*g10) / p4m)
	if got >= heuristic {
		t.Fatalf("index bound %d is not tighter than the length heuristic %d", got, heuristic)
	}

	cases := []struct {
		name        string
		spans       []fileSpan
		off, ln, pl int64
		want        int
	}{
		{"first file of two", spans[:2], 0, g2, p4m, int((g2 + g10) / p4m)},
		{"last file clamps to it", spans, 3*g2 + g10, g2, p4m, int((4*g2 + g10) / p4m)},
		{"file absent from list", spans, 999 * g2, g2, p4m, 0},
		{"empty list falls back", nil, 0, g2, p4m, 0},
		{"nil file in list is skipped", []fileSpan{{}, {offset: g2, length: g10}, {offset: g2 + g10, length: g2}}, g2, g10, p4m, int((g2 + g10 + g2) / p4m)},
		{"zero piece length", spans, g2, g10, 0, 0},
	}
	for _, c := range cases {
		if got := nextTwoFilesEndPiece(c.spans, c.off, c.ln, c.pl); got != c.want {
			t.Errorf("%s: got %d, want %d", c.name, got, c.want)
		}
	}
}

// torrentFileSpans must project a real file list without dereferencing nil.
func TestTorrentFileSpansNilSafe(t *testing.T) {
	if got := torrentFileSpans(nil); got != nil {
		t.Errorf("nil list should project to nil, got %v", got)
	}
	// A nil element inside a list must not panic: teardown can clear files
	// while setLoadPriority is walking readers.
	got := torrentFileSpans([]*torrent.File{nil, nil})
	if len(got) != 2 {
		t.Fatalf("got %d spans, want 2 (length must survive nil elements)", len(got))
	}
	for i, s := range got {
		if s != (fileSpan{}) {
			t.Errorf("span %d = %+v, want zero value for a nil file", i, s)
		}
	}
}
