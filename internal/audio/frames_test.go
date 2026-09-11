package audio

import (
	"errors"
	"path/filepath"
	"testing"
)

// rampWAV writes a 2-channel take where left = frame index, right = -frame
// index, so any block's contents identify its position.
func rampWAV(t *testing.T, frames int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "jam_ramp.wav")
	data := make([]int32, frames*2)
	for i := 0; i < frames; i++ {
		data[i*2] = int32(i)
		data[i*2+1] = -int32(i)
	}
	if _, err := WriteWAV(p, data, 2, []int{0, 1}, 48000); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadWAVInfoReportsDataOffsetAndFrames(t *testing.T) {
	info, err := ReadWAVInfo(rampWAV(t, 100))
	if err != nil {
		t.Fatal(err)
	}
	if info.DataOffset != 44 {
		t.Errorf("DataOffset = %d, want 44 for a canonical header", info.DataOffset)
	}
	if info.Frames() != 100 {
		t.Errorf("Frames() = %d, want 100", info.Frames())
	}
}

func TestReadFramesStreamsExactlyTheWindowInOrder(t *testing.T) {
	p := rampWAV(t, 1000)
	var got []int32
	var firsts []int64
	info, err := ReadFrames(p, 250, 610, 128, func(b []int32, first int64) error {
		got = append(got, b...)
		firsts = append(firsts, first)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.Channels != 2 {
		t.Fatalf("channels = %d", info.Channels)
	}
	if len(got) != 360*2 {
		t.Fatalf("got %d samples, want %d", len(got), 360*2)
	}
	for i := 0; i < 360; i++ {
		if got[i*2] != int32(250+i) || got[i*2+1] != -int32(250+i) {
			t.Fatalf("frame %d = (%d,%d), want (%d,%d)", i, got[i*2], got[i*2+1], 250+i, -(250 + i))
		}
	}
	// 360 frames in 128-frame blocks: 250, 378, 506.
	if len(firsts) != 3 || firsts[0] != 250 || firsts[1] != 378 || firsts[2] != 506 {
		t.Errorf("block starts = %v", firsts)
	}
}

func TestReadFramesRejectsABadWindow(t *testing.T) {
	p := rampWAV(t, 100)
	for _, w := range [][2]int64{{-1, 10}, {10, 10}, {20, 10}, {0, 101}} {
		_, err := ReadFrames(p, w[0], w[1], 64, func([]int32, int64) error { return nil })
		if !errors.Is(err, ErrRange) {
			t.Errorf("window %v: err = %v, want ErrRange", w, err)
		}
	}
}

func TestReadFramesStopsOnCallbackError(t *testing.T) {
	p := rampWAV(t, 1000)
	boom := errors.New("boom")
	calls := 0
	_, err := ReadFrames(p, 0, 1000, 100, func([]int32, int64) error { calls++; return boom })
	if !errors.Is(err, boom) || calls != 1 {
		t.Errorf("err = %v, calls = %d; want boom after one call", err, calls)
	}
}
