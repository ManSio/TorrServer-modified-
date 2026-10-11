package api

import (
	"encoding/json"
	"sync"
	"testing"

	sets "server/settings"
)

// The web client polls /settings with action=get: 9437 of the last 20000
// requests on the live server. If a poll re-encodes the configuration every
// time, the poll costs as much as the work it is supposed to observe.
func TestSettingsJSONIsEncodedOnceWhileUnchanged(t *testing.T) {
	invalidateSettingsJSON()
	sets.BTsets = &sets.BTSets{CacheSize: 64 << 20, ReaderReadAheadMB: 16}

	first := settingsJSONBytes()
	if len(first) == 0 {
		t.Fatal("no JSON produced")
	}

	// The cached bytes must be reused, not rebuilt. Comparing the backing
	// array catches a re-encode that happens to produce equal bytes.
	second := settingsJSONBytes()
	if &first[0] != &second[0] {
		t.Error("settings JSON was re-encoded on a second poll; the cache is not being used")
	}
}

// A poll must never return settings that were already changed. This is the bug
// the cache is capable of introducing, so it is the one that matters.
func TestSettingsJSONReflectsChangesAfterInvalidation(t *testing.T) {
	sets.BTsets = &sets.BTSets{CacheSize: 64 << 20, ReaderReadAheadMB: 16}
	invalidateSettingsJSON()
	_ = settingsJSONBytes()

	sets.BTsets = &sets.BTSets{CacheSize: 128 << 20, ReaderReadAheadMB: 93}
	invalidateSettingsJSON()

	var got sets.BTSets
	if err := json.Unmarshal(settingsJSONBytes(), &got); err != nil {
		t.Fatalf("cached JSON does not parse: %v", err)
	}
	if got.ReaderReadAheadMB != 93 {
		t.Errorf("ReaderReadAheadMB is %d after a change, want 93", got.ReaderReadAheadMB)
	}
	if got.CacheSize != 128<<20 {
		t.Errorf("CacheSize is %d after a change, want %d", got.CacheSize, 128<<20)
	}
}

// Without this, the first poll after a change would keep serving the bytes
// captured before it - the classic stale cache.
func TestInvalidationForcesReEncode(t *testing.T) {
	sets.BTsets = &sets.BTSets{ConnectionsLimit: 25}
	invalidateSettingsJSON()
	before := settingsJSONBytes()

	sets.BTsets = &sets.BTSets{ConnectionsLimit: 60}
	invalidateSettingsJSON()
	after := settingsJSONBytes()

	if &before[0] == &after[0] {
		t.Error("invalidation did not force a re-encode; the poll would return stale settings")
	}
}

// Polls arrive concurrently from every open tab. A data race here would return
// a half-written buffer to a client.
func TestSettingsJSONConcurrentPolls(t *testing.T) {
	sets.BTsets = &sets.BTSets{CacheSize: 64 << 20}
	invalidateSettingsJSON()

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%8 == 0 {
				invalidateSettingsJSON()
			}
			buf := settingsJSONBytes()
			if len(buf) == 0 {
				t.Error("concurrent poll returned an empty body")
				return
			}
			var probe sets.BTSets
			if err := json.Unmarshal(buf, &probe); err != nil {
				t.Errorf("concurrent poll returned invalid JSON: %v", err)
			}
		}(i)
	}
	wg.Wait()
}
