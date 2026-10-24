package settings

import "testing"

// TestCloseDBAllowsReopen is the closed-handle regression: NewTDB returned the
// stale global after CloseDB, whose db field was nil.
func TestCloseDBAllowsReopen(t *testing.T) {
	oldPath := Path
	oldGlobal := globalBboltDB
	Path = t.TempDir()
	globalBboltDB = nil
	t.Cleanup(func() {
		if globalBboltDB != nil {
			globalBboltDB.CloseDB()
		}
		Path = oldPath
		globalBboltDB = oldGlobal
	})

	db1 := NewTDB()
	if db1 == nil {
		t.Fatal("NewTDB: nil")
	}
	db1.Set("x", "a", []byte("1"))
	db1.CloseDB()

	db2 := NewTDB()
	if db2 == nil {
		t.Fatal("NewTDB after CloseDB returned nil")
	}
	if db2 == db1 {
		t.Fatal("NewTDB returned the closed handle")
	}
	if got := string(db2.Get("x", "a")); got != "1" {
		t.Fatalf("reopened DB lost data: %q", got)
	}
}
