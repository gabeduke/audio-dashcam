package audio

import (
	"sync"
	"testing"
)

func TestTotalFramesCountsEverythingEverWritten(t *testing.T) {
	r := NewRing(10, 2)
	if got := r.TotalFrames(); got != 0 {
		t.Errorf("TotalFrames on a fresh ring = %d, want 0", got)
	}
	r.WriteFrames(make([]int32, 4*2)) // 4 frames
	r.WriteFrames(make([]int32, 3*2)) // 3 frames
	if got := r.TotalFrames(); got != 7 {
		t.Errorf("TotalFrames = %d, want 7", got)
	}
}

func TestTotalFramesKeepsCountingPastCapacity(t *testing.T) {
	r := NewRing(4, 1)
	r.WriteFrames(make([]int32, 10))
	if got := r.TotalFrames(); got != 10 {
		t.Errorf("TotalFrames = %d, want 10 (not clamped to capacity)", got)
	}
	if got := r.BufferedFrames(); got != 4 {
		t.Errorf("BufferedFrames = %d, want 4", got)
	}
}

func TestSnapshotAtReportsTheWindowEnd(t *testing.T) {
	r := NewRing(100, 1)
	in := make([]int32, 10)
	for i := range in {
		in[i] = int32(i + 1)
	}
	r.WriteFrames(in)

	data, got, end := r.SnapshotAt(4)
	if got != 4 {
		t.Fatalf("frames = %d, want 4", got)
	}
	if end != 10 {
		t.Errorf("endFrame = %d, want 10", end)
	}
	// The newest 4 frames are 7,8,9,10.
	want := []int32{7, 8, 9, 10}
	for i, v := range want {
		if data[i] != v {
			t.Errorf("data[%d] = %d, want %d", i, data[i], v)
		}
	}
}

func TestSnapshotAtOnEmptyRingReportsZeroEnd(t *testing.T) {
	r := NewRing(10, 1)
	data, got, end := r.SnapshotAt(4)
	if data != nil || got != 0 || end != 0 {
		t.Errorf("SnapshotAt on empty ring = (%v, %d, %d), want (nil, 0, 0)", data, got, end)
	}
}

func TestSnapshotAtClampsToWhatIsBuffered(t *testing.T) {
	r := NewRing(100, 1)
	r.WriteFrames(make([]int32, 5))
	_, got, end := r.SnapshotAt(50)
	if got != 5 || end != 5 {
		t.Errorf("got (%d, %d), want (5, 5)", got, end)
	}
}

// The whole point of SnapshotAt: the copy and its position must describe the
// same instant. If they were read separately a concurrent write could slip
// between them and every flag in the window would be off by that much.
func TestSnapshotAtIsConsistentUnderConcurrentWrites(t *testing.T) {
	r := NewRing(1000, 1)
	var wg sync.WaitGroup
	stop := make(chan struct{})

	wg.Add(1)
	go func() {
		defer wg.Done()
		block := make([]int32, 8)
		for {
			select {
			case <-stop:
				return
			default:
				r.WriteFrames(block)
			}
		}
	}()

	for i := 0; i < 500; i++ {
		_, got, end := r.SnapshotAt(16)
		if uint64(got) > end {
			t.Fatalf("window of %d frames ends at absolute frame %d: the copy outruns the counter", got, end)
		}
	}
	close(stop)
	wg.Wait()
}

func TestSnapshotStillReturnsTwoValues(t *testing.T) {
	r := NewRing(10, 1)
	r.WriteFrames(make([]int32, 5))
	data, got := r.Snapshot(3)
	if got != 3 || len(data) != 3 {
		t.Errorf("Snapshot = (%d values, %d frames), want (3, 3)", len(data), got)
	}
}
