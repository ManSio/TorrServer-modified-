package settings

import (
	"testing"
	"time"
)

func TestCacheStateTTLDuration(t *testing.T) {
	cases := []struct {
		name string
		ttl  int
		want time.Duration
	}{
		{"unset means default", 0, time.Second},
		{"positive passes through", 5, 5 * time.Second},
		{"one second", 1, time.Second},
		{"negative disables", -1, 0},
		{"very negative disables", -999, 0},
		{"clamped to max", CacheStateTTLMaxSec + 100, CacheStateTTLMaxSec * time.Second},
		{"exactly max", CacheStateTTLMaxSec, CacheStateTTLMaxSec * time.Second},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &BTSets{CacheStateTTL: c.ttl}
			if got := s.CacheStateTTLDuration(); got != c.want {
				t.Errorf("CacheStateTTLDuration(%d) = %v, want %v", c.ttl, got, c.want)
			}
		})
	}
}

// A nil BTSets must not panic: it happens in tests and in early startup, before
// the DB has been read.
func TestCacheStateTTLDurationNilSets(t *testing.T) {
	var s *BTSets
	if got := s.CacheStateTTLDuration(); got != 0 {
		t.Errorf("nil sets CacheStateTTLDuration() = %v, want 0", got)
	}
}

// Existing configs deserialise CacheStateTTL as 0 (the field did not exist).
// That must mean "use the default", not "coalescing is off" — otherwise the
// optimisation would silently do nothing for every installed server.
func TestCacheStateTTLDefaultIsNonZero(t *testing.T) {
	if DefaultCacheStateTTLSec <= 0 {
		t.Fatalf("DefaultCacheStateTTLSec = %d, must be positive", DefaultCacheStateTTLSec)
	}
	s := &BTSets{}
	if d := s.CacheStateTTLDuration(); d <= 0 {
		t.Errorf("zero value resolves to %v, want the default %v", d, DefaultCacheStateTTLSec*time.Second)
	}
}
