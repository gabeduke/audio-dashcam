package audio

import (
	"sort"
	"sync"
)

// MaxLiveFlags bounds the live store. A stuck or spammed button must not be
// able to grow it without limit; 256 marks over a 15-minute ring is far more
// than anyone places by hand.
const MaxLiveFlags = 256

// FlagStore holds live marks as absolute ring frames -- the same clock
// Ring.TotalFrames reports. Absolute frames are used rather than ages because
// the ring's clock stalls during a dropout, which keeps a mark pinned to the
// audio instead of to wall time.
//
// Marks are not consumed by a save. They leave only by ageing out of the ring,
// so two overlapping captures both inherit the marks that fall inside them and
// a mistimed capture never silently destroys one.
type FlagStore struct {
	mu    sync.Mutex
	max   int
	marks []uint64 // ascending
}

func NewFlagStore(max int) *FlagStore {
	if max <= 0 {
		max = MaxLiveFlags
	}
	return &FlagStore{max: max}
}

// Mark records a mark at the given absolute frame and returns it. Marking the
// same frame twice collapses to one entry, which is what happens when capture
// is dead and TotalFrames has stopped advancing.
func (s *FlagStore) Mark(now uint64) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	i := sort.Search(len(s.marks), func(i int) bool { return s.marks[i] >= now })
	if i < len(s.marks) && s.marks[i] == now {
		return now
	}
	s.marks = append(s.marks, 0)
	copy(s.marks[i+1:], s.marks[i:])
	s.marks[i] = now

	if len(s.marks) > s.max {
		s.marks = s.marks[len(s.marks)-s.max:]
	}
	return now
}

// Active prunes marks that have aged out of the ring and returns what is left,
// ascending. A mark exactly ringFrames old is still inside the ring and is
// kept, matching Ring.Snapshot's own inclusive window.
func (s *FlagStore) Active(now, ringFrames uint64) []uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	if ringFrames < now {
		oldest := now - ringFrames
		i := sort.Search(len(s.marks), func(i int) bool { return s.marks[i] >= oldest })
		s.marks = s.marks[i:]
	}
	if len(s.marks) == 0 {
		return nil
	}
	out := make([]uint64, len(s.marks))
	copy(out, s.marks)
	return out
}

// Remove drops one mark, reporting whether it was there.
func (s *FlagStore) Remove(frame uint64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	i := sort.Search(len(s.marks), func(i int) bool { return s.marks[i] >= frame })
	if i >= len(s.marks) || s.marks[i] != frame {
		return false
	}
	s.marks = append(s.marks[:i], s.marks[i+1:]...)
	return true
}

func (s *FlagStore) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.marks = nil
}

func (s *FlagStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.marks)
}
