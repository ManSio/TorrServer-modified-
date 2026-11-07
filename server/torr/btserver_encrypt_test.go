package torr

import (
	"testing"

	"github.com/anacrolix/torrent"
)

// The two encryption settings contradict each other, so the combination rule is
// worth pinning: whichever way it is resolved, a user reading the settings page
// must not be told the opposite of what the client does.
func TestEncryptionPolicy(t *testing.T) {
	cases := []struct {
		name         string
		force        bool
		disable      bool
		wantDisabled bool
		wantForced   bool
	}{
		{"neither set: anacrolix's own default", false, false, false, false},
		{"disable only", false, true, true, false},
		{"force only", true, false, false, true},
		{"both set: force wins", true, true, true, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := encryptionPolicy(c.force, c.disable)
			if got.DisableEncryption != c.wantDisabled {
				t.Errorf("DisableEncryption = %v, want %v", got.DisableEncryption, c.wantDisabled)
			}
			if got.ForceEncryption != c.wantForced {
				t.Errorf("ForceEncryption = %v, want %v", got.ForceEncryption, c.wantForced)
			}
			// A policy that both disables and forces is nonsense.
			if got.DisableEncryption && got.ForceEncryption {
				t.Error("policy both disables and forces encryption")
			}
		})
	}
}

// Refusing encryption must not accidentally force it, and vice versa: the zero
// value of the settings (what an existing config deserialises to) has to keep
// behaving the way it did before the field existed.
func TestEncryptionPolicyZeroValueIsDefault(t *testing.T) {
	got := encryptionPolicy(false, false)
	if got.DisableEncryption {
		t.Error("zero value disables encryption, changing behaviour for existing configs")
	}
	if got.ForceEncryption {
		t.Error("zero value forces encryption")
	}
	if got != (torrent.EncryptionPolicy{}) {
		t.Errorf("zero value produced %+v, want the empty policy", got)
	}
}
