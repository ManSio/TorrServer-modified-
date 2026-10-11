package settings

import (
	"encoding/json"
	"testing"
)

// An existing install has no PausePreloadMB key in its stored JSON, so the
// field decodes to 0 - and 0 means "no pause window". Upgrading without the
// migration therefore ships the feature switched off on every server that was
// configured before it existed, which looks exactly like a feature that does
// nothing. This test pins the migration, not the default.
func TestPausePreloadMigratesConfigsThatPredateIt(t *testing.T) {
	// A stored config from before the setting existed: no PausePreload keys.
	stored, err := json.Marshal(map[string]any{
		"ReaderReadAHead": 90,
		"CacheSize":       105109258240,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	got := new(BTSets)
	if err := json.Unmarshal(stored, got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	migrateBTSets(stored, got)

	if got.PausePreloadMB != PausePreloadDefaultMB {
		t.Errorf("config without the key loaded PausePreloadMB=%d, want the default %d",
			got.PausePreloadMB, PausePreloadDefaultMB)
	}
	if got.PausePreloadAfterSec != PausePreloadAfterDefaultSec {
		t.Errorf("config without the key loaded PausePreloadAfterSec=%d, want the default %d",
			got.PausePreloadAfterSec, PausePreloadAfterDefaultSec)
	}
	// The migration must not disturb what the user had already chosen.
	if got.ReaderReadAheadMB != 90 {
		t.Errorf("migration changed ReaderReadAheadMB to %d, want 90", got.ReaderReadAheadMB)
	}
}

// A user who explicitly turned the feature off must stay off. An explicit 0 is
// a decision; a missing key is an absence, and only an absence may be filled
// in with a default.
func TestPausePreloadKeepsAnExplicitValue(t *testing.T) {
	stored, err := json.Marshal(map[string]any{
		"ReaderReadAHead":      90,
		"PausePreloadMB":       0,
		"PausePreloadAfterSec": 300,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	got := new(BTSets)
	if err := json.Unmarshal(stored, got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	migrateBTSets(stored, got)

	if got.PausePreloadMB != 0 {
		t.Errorf("explicit PausePreloadMB=0 was overwritten with %d; an explicit 0 means disabled", got.PausePreloadMB)
	}
	if got.PausePreloadAfterSec != 300 {
		t.Errorf("explicit PausePreloadAfterSec=300 became %d", got.PausePreloadAfterSec)
	}
}
