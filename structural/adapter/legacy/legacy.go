// Package legacy simulates a third-party library we cannot modify.
// It is deliberately awkward: different method names, different value type,
// different error convention. The adapter package wraps it.
package legacy

// Entry is the legacy value type.
type Entry struct {
	Payload []byte
	Version int
}

// EntryStore is the legacy storage API: Load/Save with a bool ok return.
type EntryStore struct {
	m map[string]Entry
}

// New returns an empty legacy store.
func New() *EntryStore { return &EntryStore{m: make(map[string]Entry)} }

// Load mirrors map lookup semantics.
func (s *EntryStore) Load(key string) (Entry, bool) {
	e, ok := s.m[key]
	return e, ok
}

// Save overwrites and bumps the version counter.
func (s *EntryStore) Save(key string, e Entry) {
	if prev, ok := s.m[key]; ok {
		e.Version = prev.Version + 1
	} else {
		e.Version = 1
	}
	s.m[key] = e
}

// Len reports the number of stored entries.
func (s *EntryStore) Len() int { return len(s.m) }
