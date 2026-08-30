package pglogwatch

import "container/list"

// defaultMaxTrackedFiles bounds the in-memory offset store.
//
// IFC-007 fixes it at 2500 to match pgwatch's maxTrackedFiles, so migrating
// pgwatch onto this module cannot change how many rotated files it remembers.
const defaultMaxTrackedFiles = 2500

// OffsetStore persists how far each log file has been read.
//
// The values are BYTE offsets, not line counts (IFC-006). That is what makes
// resumption a single seek instead of a re-read, and it is what lets the same
// numbers drive the remote reader, whose pg_read_file(path, offset, len) is
// already offset-based (§7.6).
//
// Implementations must tolerate being called from the goroutine that drives
// the reader; they are not called concurrently by this package.
type OffsetStore interface {
	// Get returns the stored offset for a path, and whether there was one.
	Get(path string) (offset int64, ok bool)

	// Set records how far a path has been read.
	Set(path string, offset int64)
}

// memoryOffsetStore is the default OffsetStore: bounded, with least-recently-
// used eviction.
//
// The bound is the point. A log directory accumulates rotated files forever,
// and an unbounded map would grow without limit in a process designed to run
// for months -- a slow leak in the one component that is supposed to be cheap
// (IFC-007).
//
// Evicting the least recently used entry is right for this access pattern: log
// files are read newest-first and old ones are never revisited, so the entry
// evicted is one whose file has almost certainly been deleted already.
type memoryOffsetStore struct {
	max   int
	order *list.List // front is most recently used
	items map[string]*list.Element
}

type offsetEntry struct {
	path   string
	offset int64
}

func newMemoryOffsetStore(maxEntries int) *memoryOffsetStore {
	if maxEntries <= 0 {
		maxEntries = defaultMaxTrackedFiles
	}
	return &memoryOffsetStore{
		max:   maxEntries,
		order: list.New(),
		items: make(map[string]*list.Element, maxEntries),
	}
}

func (s *memoryOffsetStore) Get(path string) (int64, bool) {
	el, ok := s.items[path]
	if !ok {
		return 0, false
	}
	s.order.MoveToFront(el)
	return el.Value.(*offsetEntry).offset, true
}

func (s *memoryOffsetStore) Set(path string, offset int64) {
	if el, ok := s.items[path]; ok {
		el.Value.(*offsetEntry).offset = offset
		s.order.MoveToFront(el)
		return
	}
	s.items[path] = s.order.PushFront(&offsetEntry{path: path, offset: offset})
	for s.order.Len() > s.max {
		oldest := s.order.Back()
		if oldest == nil {
			return
		}
		s.order.Remove(oldest)
		delete(s.items, oldest.Value.(*offsetEntry).path)
	}
}

// len reports how many paths are tracked, for tests.
func (s *memoryOffsetStore) len() int { return s.order.Len() }
