package torznab

import "testing"

// buildAPIURL must collapse a host that already carries the "/api" segment
// instead of producing ".../api/api".
func TestBuildAPIURLNormalizesHost(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"http://indexer:9117", "http://indexer:9117/api"},
		{"http://indexer:9117/", "http://indexer:9117/api"},
		{"http://indexer:9117/api", "http://indexer:9117/api"},
		{"http://indexer:9117/api/", "http://indexer:9117/api"},
		{"  http://indexer:9117/api/  ", "http://indexer:9117/api"},
		{"indexer:9117/torznab", "http://indexer:9117/torznab/api"},
	}
	for _, c := range cases {
		u, err := buildAPIURL(c.in)
		if err != nil {
			t.Fatalf("buildAPIURL(%q): %v", c.in, err)
		}
		if u.String() != c.want {
			t.Errorf("buildAPIURL(%q) = %q, want %q", c.in, u.String(), c.want)
		}
	}
}
