package audio

import "testing"

func TestMarkRecordsTheCurrentFrame(t *testing.T) {
	s := NewFlagStore(MaxLiveFlags)
	if got := s.Mark(1234); got != 1234 {
		t.Errorf("Mark returned %d, want 1234", got)
	}
	got := s.Active(1234, 900)
	if len(got) != 1 || got[0] != 1234 {
		t.Errorf("Active = %v, want [1234]", got)
	}
}

// totalFrames freezes while capture is dead, so repeated marks resolve to the
// same frame. They must collapse rather than stack up.
func TestRepeatedMarksAtTheSameFrameCollapse(t *testing.T) {
	s := NewFlagStore(MaxLiveFlags)
	s.Mark(500)
	s.Mark(500)
	s.Mark(500)
	if got := s.Len(); got != 1 {
		t.Errorf("Len = %d, want 1", got)
	}
}

func TestActiveDropsFlagsOlderThanTheRing(t *testing.T) {
	s := NewFlagStore(MaxLiveFlags)
	s.Mark(10)  // will be 990 frames old
	s.Mark(600) // will be 400 frames old
	got := s.Active(1000, 500)
	if len(got) != 1 || got[0] != 600 {
		t.Errorf("Active = %v, want [600]", got)
	}
	// Pruning is permanent, not just filtered from the view.
	if s.Len() != 1 {
		t.Errorf("Len after prune = %d, want 1", s.Len())
	}
}

func TestActiveKeepsAFlagExactlyAtTheRingBoundary(t *testing.T) {
	s := NewFlagStore(MaxLiveFlags)
	s.Mark(500) // exactly ringFrames old at now=1000, ring=500
	got := s.Active(1000, 500)
	if len(got) != 1 {
		t.Errorf("Active = %v, want the boundary flag kept", got)
	}
}

func TestActiveReturnsAscendingOrder(t *testing.T) {
	s := NewFlagStore(MaxLiveFlags)
	s.Mark(300)
	s.Mark(100)
	s.Mark(200)
	got := s.Active(400, 900)
	want := []uint64{100, 200, 300}
	if len(got) != 3 {
		t.Fatalf("Active = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Active[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestCapDropsTheOldestFlag(t *testing.T) {
	s := NewFlagStore(3)
	s.Mark(1)
	s.Mark(2)
	s.Mark(3)
	s.Mark(4)
	got := s.Active(100, 900)
	want := []uint64{2, 3, 4}
	if len(got) != 3 {
		t.Fatalf("Active = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Active[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestRemoveReportsWhetherItRemovedAnything(t *testing.T) {
	s := NewFlagStore(MaxLiveFlags)
	s.Mark(42)
	if !s.Remove(42) {
		t.Error("Remove(42) = false, want true")
	}
	if s.Remove(42) {
		t.Error("Remove(42) twice = true, want false")
	}
	if s.Len() != 0 {
		t.Errorf("Len = %d, want 0", s.Len())
	}
}

func TestClearEmptiesTheStore(t *testing.T) {
	s := NewFlagStore(MaxLiveFlags)
	s.Mark(1)
	s.Mark(2)
	s.Clear()
	if s.Len() != 0 {
		t.Errorf("Len = %d, want 0", s.Len())
	}
}

func TestActiveOnAnEmptyStoreReturnsNothing(t *testing.T) {
	s := NewFlagStore(MaxLiveFlags)
	if got := s.Active(100, 900); len(got) != 0 {
		t.Errorf("Active = %v, want empty", got)
	}
}
