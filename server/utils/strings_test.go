package utils

import (
	"slices"
	"sort"
	"testing"
)

// compareSorted returns a copy of in sorted by CompareStrings.
func compareSorted(in []string) []string {
	out := slices.Clone(in)
	sort.Slice(out, func(i, j int) bool { return CompareStrings(out[i], out[j]) })
	return out
}

// reversed returns a reversed copy, so the tests sort from a known-wrong
// starting order and the comparator has to do the work.
func reversed(in []string) []string {
	out := slices.Clone(in)
	slices.Reverse(out)
	return out
}

// Unpadded episode numbers must come out in numeric order. The common prefix
// of "Episode 1.mp4" and "Episode 10.mp4" ends inside the number, so trimming
// it used to leave ".mp4" against "0.mp4" — the side starting with a digit
// won, and every player walking the playlist started at episode 10.
func TestCompareStringsSortsNumbersNaturally(t *testing.T) {
	want := []string{
		"Show/Episode 1.mp4", "Show/Episode 2.mp4", "Show/Episode 3.mp4",
		"Show/Episode 9.mp4", "Show/Episode 10.mp4", "Show/Episode 11.mp4",
		"Show/Episode 12.mp4", "Show/Episode 20.mp4", "Show/Episode 21.mp4",
	}
	got := compareSorted(reversed(want))
	if !slices.Equal(got, want) {
		t.Fatalf("unpadded numbers sorted wrong:\n got: %q\nwant: %q", got, want)
	}
}

// Regression guard for the names this server actually holds: two-digit padded
// season/episode files, where the common prefix already stops before the
// digits and the old code was correct. The fix must not disturb these, and
// must not make the comparator inconsistent for sort.Slice.
func TestCompareStringsKeepsPaddedEpisodesOrdered(t *testing.T) {
	names := []string{
		"Show/Season_02/s02e01_A.avi", "Show/Season_02/s02e02_B.avi",
		"Show/Season_02/s02e09_I.avi", "Show/Season_02/s02e10_J.avi",
		"Show/Season_02/s02e11_K.avi", "Show/Season_03/s03e01_L.avi",
		"Show/Season_06/s06e13_M.avi",
	}
	want := slices.Clone(names)
	got := compareSorted(reversed(names))
	if !slices.Equal(got, want) {
		t.Fatalf("padded names reordered:\n got: %q\nwant: %q", got, want)
	}
}

// Pairs the old code got right must stay right: the prefix ends before the
// digits, so both sides parse as numbers.
func TestCompareStringsNumberPairs(t *testing.T) {
	cases := []struct {
		less, greater string
	}{
		{"Show/Ep 2.mp4", "Show/Ep 10.mp4"},
		{"Show/s02e09.avi", "Show/s02e10.avi"},
		{"Show/02.mkv", "Show/10.mkv"},
		{"Show/a.mkv", "Show/b.mkv"},
		{"Show/10.mkv", "Show/a.mkv"},
	}
	for _, c := range cases {
		if got, want := CompareStrings(c.less, c.greater), true; got != want {
			t.Errorf("CompareStrings(%q, %q) = %v, want %v", c.less, c.greater, got, want)
		}
		if got, want := CompareStrings(c.greater, c.less), false; got != want {
			t.Errorf("CompareStrings(%q, %q) = %v, want %v", c.greater, c.less, got, want)
		}
	}
}

// Equal numbers fall back to a plain string compare, so "1a" sorts before "1b"
// instead of the comparator claiming both are less than each other.
func TestCompareStringsEqualNumbersFallback(t *testing.T) {
	if !CompareStrings("Show/Ep 1a.mp4", "Show/Ep 1b.mp4") {
		t.Error("1a should sort before 1b")
	}
	if CompareStrings("Show/Ep 1b.mp4", "Show/Ep 1a.mp4") {
		t.Error("1b should not sort before 1a")
	}
}

// sort.Slice panics on a comparator that is not a strict weak ordering, so a
// full shuffle of mixed names is the cheapest guard against the fix making the
// comparator inconsistent.
func TestCompareStringsIsUsableAsSortComparator(t *testing.T) {
	mixed := []string{
		"Show/Episode 1.mp4", "Show/Episode 10.mp4", "Show/Episode 2.mp4",
		"Show/s01e01.avi", "Show/s01e10.avi", "Show/a.mkv", "Show/Ep 1b.mp4",
		"Show/Ep 1a.mp4", "Show/Episode 11.mp4",
	}
	want := compareSorted(slices.Clone(mixed))
	for i := 0; i < len(mixed); i++ { // deterministic rotations, not random
		rot := append(slices.Clone(mixed[i:]), mixed[:i]...)
		if got := compareSorted(rot); !slices.Equal(got, want) {
			t.Fatalf("rotation %d gave a different order:\n got: %q\nwant: %q", i, got, want)
		}
	}
}
