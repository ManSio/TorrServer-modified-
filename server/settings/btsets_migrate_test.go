package settings

import (
	"encoding/json"
	"testing"
)

// A stored config from before AutoTune has no key for it, and the zero value
// of a bool is false. Upgrading without the migration would therefore switch
// every existing install to manual mode with whatever numbers happen to be
// stored - the opposite of the shipped behaviour. The migration keeps new
// installs and old installs on the same default.
func TestAutoTuneMigratesConfigsThatPredateIt(t *testing.T) {
	stored, err := json.Marshal(map[string]any{
		"ReaderReadAHead": 90,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	got := new(BTSets)
	if err := json.Unmarshal(stored, got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	migrateBTSets(stored, got)

	if !got.AutoTune {
		t.Error("config without the key loaded AutoTune=false, want the default true")
	}
}

// An explicit false is a decision to tune by hand. Only a missing key may be
// filled in.
func TestAutoTuneKeepsAnExplicitFalse(t *testing.T) {
	stored, err := json.Marshal(map[string]any{
		"ReaderReadAHead": 90,
		"AutoTune":        false,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	got := new(BTSets)
	if err := json.Unmarshal(stored, got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	migrateBTSets(stored, got)

	if got.AutoTune {
		t.Error("explicit AutoTune=false was overwritten with true")
	}
}

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

// A stored config from before BackgroundFetch has no key for it. The fetch
// must be on for old installs too, or upgrading changes behaviour silently.
func TestBackgroundFetchMigratesConfigsThatPredateIt(t *testing.T) {
	stored, err := json.Marshal(map[string]any{
		"ReaderReadAHead": 90,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := new(BTSets)
	if err := json.Unmarshal(stored, got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	migrateBTSets(stored, got)
	if !got.BackgroundFetch {
		t.Error("config without the key loaded BackgroundFetch=false, want true")
	}
}

// An explicit false stays false: someone who turned the fetch off did it on
// purpose, and an upgrade must not switch it back on behind their back.
func TestBackgroundFetchKeepsAnExplicitFalse(t *testing.T) {
	stored, err := json.Marshal(map[string]any{
		"ReaderReadAHead": 90,
		"BackgroundFetch": false,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := new(BTSets)
	if err := json.Unmarshal(stored, got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	migrateBTSets(stored, got)
	if got.BackgroundFetch {
		t.Error("explicit BackgroundFetch=false was overwritten with true")
	}
}
