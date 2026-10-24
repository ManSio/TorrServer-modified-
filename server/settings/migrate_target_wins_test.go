package settings

import (
	"path"
	"testing"
)

// memDB is the smallest TorrServerDB needed by MigrateAll.
type memDB struct {
	data map[string][]byte
}

func newMemDB() *memDB {
	return &memDB{data: make(map[string][]byte)}
}

func dbKey(xpath, name string) string {
	return path.Join(xpath, name)
}

func (m *memDB) CloseDB() {}
func (m *memDB) Get(xpath, name string) []byte {
	v := m.data[dbKey(xpath, name)]
	if v == nil {
		return nil
	}
	out := make([]byte, len(v))
	copy(out, v)
	return out
}
func (m *memDB) Set(xpath, name string, value []byte) {
	v := make([]byte, len(value))
	copy(v, value)
	m.data[dbKey(xpath, name)] = v
}
func (m *memDB) List(xpath string) []string {
	var names []string
	for k := range m.data {
		if path.Dir(k) == xpath {
			names = append(names, path.Base(k))
		}
	}
	return names
}
func (m *memDB) Rem(xpath, name string) { delete(m.data, dbKey(xpath, name)) }
func (m *memDB) Clear(xpath string) {
	for k := range m.data {
		if path.Dir(k) == xpath {
			delete(m.data, k)
		}
	}
}

// TestMigrateAllKeepsTarget is the regression for issue #678: an old source
// file must not overwrite a value that already exists in the live database.
func TestMigrateAllKeepsTarget(t *testing.T) {
	source := newMemDB()
	target := newMemDB()
	source.Set("/Viewed", "abc", []byte(`{"Pos":1,"Viewed":100}`))
	target.Set("/Viewed", "abc", []byte(`{"Pos":9,"Viewed":900}`))

	migrated, skipped, err := MigrateAll(source, target, "/Viewed")
	if err != nil {
		t.Fatalf("MigrateAll: %v", err)
	}
	if migrated != 0 || skipped != 1 {
		t.Fatalf("migrated=%d skipped=%d, want 0/1", migrated, skipped)
	}
	if got := string(target.Get("/Viewed", "abc")); got != `{"Pos":9,"Viewed":900}` {
		t.Fatalf("target was overwritten: %s", got)
	}
}

// TestMigrateAllFillsMissing is the positive control: an absent key must still
// be migrated, otherwise the test above could pass by doing nothing at all.
func TestMigrateAllFillsMissing(t *testing.T) {
	source := newMemDB()
	target := newMemDB()
	source.Set("/Viewed", "new", []byte(`{"Pos":3}`))

	migrated, skipped, err := MigrateAll(source, target, "/Viewed")
	if err != nil {
		t.Fatalf("MigrateAll: %v", err)
	}
	if migrated != 1 || skipped != 0 {
		t.Fatalf("migrated=%d skipped=%d, want 1/0", migrated, skipped)
	}
	if got := string(target.Get("/Viewed", "new")); got != `{"Pos":3}` {
		t.Fatalf("missing key was not migrated: %q", got)
	}
}
