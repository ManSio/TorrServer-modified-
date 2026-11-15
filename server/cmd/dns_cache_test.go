package main

import (
	"testing"
	"time"
)

// TestGetFromCacheExpiresAndCopies covers the two defects of the old cache
// lookup: deleting under RLock (a concurrent map write) and returning the
// stored slice, which a caller could mutate.
func TestGetFromCacheExpiresAndCopies(t *testing.T) {
	d := NewDNSChecker(DefaultDNSConfig())
	d.config.CacheDuration = time.Minute
	d.addToCache("example.org", []string{"1.2.3.4"})

	got, ok := d.getFromCache("example.org")
	if !ok || len(got) != 1 {
		t.Fatalf("fresh entry: got %v ok=%v", got, ok)
	}
	got[0] = "9.9.9.9"
	if again, _ := d.getFromCache("example.org"); again[0] != "1.2.3.4" {
		t.Fatalf("caller mutated the cached slice: %v", again)
	}

	d.mu.Lock()
	d.cacheTime["example.org"] = time.Now().Add(-2 * time.Minute)
	d.mu.Unlock()
	if _, ok := d.getFromCache("example.org"); ok {
		t.Fatal("expired entry was returned")
	}
	d.mu.RLock()
	_, stillThere := d.cache["example.org"]
	d.mu.RUnlock()
	if stillThere {
		t.Fatal("expired entry was not deleted")
	}
}
