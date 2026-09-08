package audio

import "testing"

// oneBinOfConstant builds an interleaved block of exactly one bin's worth of
// frames, every sample at the same amplitude. 1<<29 against the 1/2^31 scale
// is 0.25, i.e. about -12.04 dBFS, comfortably above FloorDB.
func oneBinOfConstant(channels, binFrames int, v int32) []int32 {
	block := make([]int32, binFrames*channels)
	for i := range block {
		block[i] = v
	}
	return block
}

func TestSnapshotSurvivesDrain(t *testing.T) {
	l := NewLevels(2, 48000, 10) // 480 frames per bin
	l.Accumulate(oneBinOfConstant(2, 480, 1<<29))

	before := l.Snapshot()
	if before[0] <= FloorDB {
		t.Fatalf("expected a real level before Drain, got %v", before)
	}

	// Broadcast drains every 40ms whenever the UI is open. A status poll that
	// lands just after a drain must still report the last known levels.
	l.Drain()

	after := l.Snapshot()
	if after[0] <= FloorDB {
		t.Fatalf("Snapshot fell to the floor after Drain: %v (it must not depend on pending)", after)
	}
	if after[0] != before[0] || after[1] != before[1] {
		t.Fatalf("levels changed across Drain: %v -> %v", before, after)
	}
}

func TestSnapshotReadsFloorBeforeAnyAudio(t *testing.T) {
	// A freshly made []float32 is all zeros, and 0 dBFS is full scale. If the
	// new field is not seeded with FloorDB the meters peg on startup.
	l := NewLevels(4, 48000, 10)
	got := l.Snapshot()
	if len(got) != 4 {
		t.Fatalf("want 4 channels, got %d", len(got))
	}
	for i, v := range got {
		if v != FloorDB {
			t.Fatalf("channel %d reads %v before any audio, want FloorDB (%v)", i, v, FloorDB)
		}
	}
}

func TestSnapshotTracksTheMostRecentBin(t *testing.T) {
	l := NewLevels(1, 48000, 10)
	l.Accumulate(oneBinOfConstant(1, 480, 1<<29)) // ~ -12 dBFS
	loud := l.Snapshot()[0]

	l.Accumulate(oneBinOfConstant(1, 480, 1<<20)) // ~ -66 dBFS, below the floor
	quiet := l.Snapshot()[0]

	if quiet >= loud {
		t.Fatalf("Snapshot did not follow the newest bin: %v then %v", loud, quiet)
	}
	if quiet != FloorDB {
		t.Fatalf("a sub-floor bin should clamp to FloorDB, got %v", quiet)
	}
}
