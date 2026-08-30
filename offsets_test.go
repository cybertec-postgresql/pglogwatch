package pglogwatch

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The bounded in-memory OffsetStore (IFC-007).

func TestOffsetStoreIsBoundedWithLRUEviction(t *testing.T) {
	// IFC-007. The bound matters because a log directory accumulates
	// rotated files forever, and an unbounded map is a slow leak in a
	// process meant to run for months.
	const maxEntries = 8
	s := newMemoryOffsetStore(maxEntries)

	for i := range 20 {
		s.Set(pathN(i), int64(i))
	}
	assert.Equal(t, maxEntries, s.len(), "the store must stay bounded")

	// The most recent survive; the oldest are gone.
	for i := 12; i < 20; i++ {
		off, ok := s.Get(pathN(i))
		assert.True(t, ok, "recent path %d must be retained", i)
		assert.Equal(t, int64(i), off)
	}
	for i := range 12 {
		_, ok := s.Get(pathN(i))
		assert.False(t, ok, "old path %d must have been evicted", i)
	}
}

func TestOffsetStoreEvictsLeastRecentlyUsedNotOldest(t *testing.T) {
	// LRU, not FIFO. A file that is still being appended to is read on
	// every pass, so it must survive even though it was added first --
	// under FIFO the active file would be evicted while dead ones stayed.
	s := newMemoryOffsetStore(3)
	s.Set("active", 1)
	s.Set("b", 2)
	s.Set("c", 3)

	_, ok := s.Get("active") // touching it makes it most recently used
	require.True(t, ok)

	s.Set("d", 4) // evicts something

	_, ok = s.Get("active")
	assert.True(t, ok, "a recently used entry must not be evicted")
	_, ok = s.Get("b")
	assert.False(t, ok, "the least recently used entry must be the one evicted")
}

func TestOffsetStoreUpdatesInPlace(t *testing.T) {
	s := newMemoryOffsetStore(4)
	s.Set("p", 10)
	s.Set("p", 20)
	off, ok := s.Get("p")
	require.True(t, ok)
	assert.Equal(t, int64(20), off)
	assert.Equal(t, 1, s.len(), "updating must not add an entry")
}

func pathN(i int) string {
	return "/var/log/postgresql/log-" + string(rune('a'+i/26)) + string(rune('a'+i%26)) + ".json"
}
