package torr

import (
	"strconv"
	"testing"
	"time"

	"server/torrshash"
)

func TestCacheStateFresh(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		at   time.Time
		ttl  time.Duration
		now  time.Time
		want bool
	}{
		{"never computed is not fresh", time.Time{}, time.Second, now, false},
		{"just computed is fresh", now, time.Second, now, true},
		{"inside ttl is fresh", now, time.Second, now.Add(999 * time.Millisecond), true},
		{"at ttl boundary is stale", now, time.Second, now.Add(time.Second), false},
		{"past ttl is stale", now, time.Second, now.Add(2 * time.Second), false},
		{"ttl disabled is never fresh", now, 0, now, false},
		{"negative ttl is never fresh", now, -time.Second, now, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := cacheStateFresh(c.at, c.ttl, c.now); got != c.want {
				t.Errorf("cacheStateFresh(%v, %v, %v) = %v, want %v", c.at, c.ttl, c.now, got, c.want)
			}
		})
	}
}

// The memoised token must be byte-identical to a fresh Pack() of the same
// fields: the point of the cache is to skip work, not to change the value.
func TestTorrsHashTokenMatchesUncachedPack(t *testing.T) {
	tr := &Torrent{}

	cases := []struct {
		name       string
		hash       string
		title      string
		poster     string
		category   string
		size       int64
		trackers   []string
		wantChange bool
	}{
		{"first", "abc123", "Some.Movie", "http://img/poster", "movie", 1 << 30, []string{"http://a/announce"}, true},
		{"same inputs", "abc123", "Some.Movie", "http://img/poster", "movie", 1 << 30, []string{"http://a/announce"}, false},
		{"no trackers", "abc123", "Some.Movie", "http://img/poster", "movie", 1 << 30, nil, true},
		{"tracker order matters", "abc123", "Some.Movie", "http://img/poster", "movie", 1 << 30, []string{"http://b/announce", "http://a/announce"}, true},
		{"title change", "abc123", "Other.Movie", "http://img/poster", "movie", 1 << 30, []string{"http://b/announce", "http://a/announce"}, true},
		{"size change", "abc123", "Other.Movie", "http://img/poster", "movie", 1<<30 + 1, []string{"http://b/announce", "http://a/announce"}, true},
	}

	// Every previously accepted token, so a "changed" input can be checked
	// against all of them rather than just the previous case.
	prev := []string{}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := tr.torrsHashToken(c.hash, c.title, c.poster, c.category, c.size, c.trackers)
			if got == "" {
				t.Fatal("empty token")
			}

			// Independent recomputation of what the old inline code produced.
			want := packForTest(t, c.hash, c.title, c.poster, c.category, c.size, c.trackers)
			if got != want {
				t.Fatalf("cached token %q != fresh Pack %q", got, want)
			}

			switch c.wantChange {
			case true:
				for _, p := range prev {
					if got == p {
						t.Fatalf("token reused after inputs changed: %+v", c)
					}
				}
				prev = append(prev, got)
			case false:
				if len(prev) == 0 {
					t.Fatal("no previous token to compare against")
				}
				if got != prev[len(prev)-1] {
					t.Fatalf("unchanged inputs changed the token: %q -> %q", prev[len(prev)-1], got)
				}
			}
		})
	}
}

// Same arguments must return the same string, and no Pack() may be needed to
// do it: the whole point is that the second call is free.
func TestTorrsHashTokenIsMemoised(t *testing.T) {
	tr := &Torrent{}
	a := tr.torrsHashToken("h", "T", "", "movie", 100, nil)
	b := tr.torrsHashToken("h", "T", "", "movie", 100, nil)
	if a != b {
		t.Fatalf("identical inputs produced %q and %q", a, b)
	}
	if tr.torrsHashKey == "" {
		t.Error("key not recorded, so the memo never hits")
	}
	if tr.torrsHashCache != a {
		t.Errorf("cache field %q != returned %q", tr.torrsHashCache, a)
	}
}

// A title containing the key separator must not be able to forge the key of a
// different (title, poster) pair — otherwise one torrent's token could be
// served for another.
func TestTorrsHashTokenSeparatorCollision(t *testing.T) {
	tr := &Torrent{}
	first := tr.torrsHashToken("h", "a\x00b", "c", "movie", 100, nil)
	second := tr.torrsHashToken("h", "a", "b\x00c", "movie", 100, nil)
	if first == second {
		t.Fatal("two different field sets produced the same token")
	}
}

func packForTest(t *testing.T, hash, title, poster, category string, size int64, trackers []string) string {
	t.Helper()
	th := torrshash.New(hash)
	th.AddField(torrshash.TagTitle, title)
	th.AddField(torrshash.TagPoster, poster)
	th.AddField(torrshash.TagCategory, category)
	th.AddField(torrshash.TagSize, strconv.FormatInt(size, 10))
	for _, tr := range trackers {
		th.AddField(torrshash.TagTracker, tr)
	}
	token, err := torrshash.Pack(th)
	if err != nil {
		t.Fatalf("Pack: %v", err)
	}
	return token
}
