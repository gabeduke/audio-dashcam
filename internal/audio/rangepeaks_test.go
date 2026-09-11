package audio

import (
	"errors"
	"math"
	"path/filepath"
	"testing"
)

// noiseWAV writes a take with a deterministic pseudo-random signal so peaks
// are non-trivial.
func noiseWAV(t *testing.T, frames int) (string, *PeakData) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "jam_noise.wav")
	data := make([]int32, frames*2)
	x := uint32(12345)
	for i := range data {
		x = x*1664525 + 1013904223
		data[i] = int32(x)
	}
	pd, err := WriteWAV(p, data, 2, []int{0, 1}, 48000)
	if err != nil {
		t.Fatal(err)
	}
	return p, pd
}

func TestRangePeaksOverTheWholeFileMatchesTheSavedPeaks(t *testing.T) {
	p, saved := noiseWAV(t, 1024*7) // 7 frames per bucket, exact
	got, err := RangePeaks(p, 0, 1024*7, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if got.Buckets != saved.Buckets || got.From != 0 || got.Channels != 2 {
		t.Fatalf("shape = %d buckets from %d, want %d from 0", got.Buckets, got.From, saved.Buckets)
	}
	for c := range saved.Data {
		for i := range saved.Data[c] {
			if got.Data[c][i] != saved.Data[c][i] {
				t.Fatalf("ch %d [%d] = %v, want %v", c, i, got.Data[c][i], saved.Data[c][i])
			}
		}
	}
}

func TestRangePeaksOfASubRangeEqualsPeaksOfThatRangeAlone(t *testing.T) {
	p, _ := noiseWAV(t, 5000)
	got, err := RangePeaks(p, 1000, 1640, 64) // 10 frames per bucket
	if err != nil {
		t.Fatal(err)
	}
	if got.From != 1000 || got.Buckets != 64 || math.Abs(got.Duration-640.0/48000) > 1e-9 {
		t.Fatalf("from=%d buckets=%d dur=%v", got.From, got.Buckets, got.Duration)
	}
	// Recompute by hand from the samples.
	var want [2][]float32
	_, err = ReadFrames(p, 1000, 1640, 640, func(b []int32, _ int64) error {
		for c := 0; c < 2; c++ {
			for k := 0; k < 64; k++ {
				mn, mx := float32(math.Inf(1)), float32(math.Inf(-1))
				for j := 0; j < 10; j++ {
					v := float32(float64(b[(k*10+j)*2+c]) / 2147483648.0)
					if v < mn {
						mn = v
					}
					if v > mx {
						mx = v
					}
				}
				want[c] = append(want[c], mn, mx)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for c := 0; c < 2; c++ {
		for i := range want[c] {
			if got.Data[c][i] != want[c][i] {
				t.Fatalf("ch %d [%d] = %v, want %v", c, i, got.Data[c][i], want[c][i])
			}
		}
	}
}

func TestRangePeaksLastBucketAbsorbsTheRemainder(t *testing.T) {
	p, _ := noiseWAV(t, 1000)
	got, err := RangePeaks(p, 0, 1000, 3) // 333 per bucket, one left over
	if err != nil {
		t.Fatal(err)
	}
	if got.Buckets != 3 {
		t.Errorf("buckets = %d, want exactly 3", got.Buckets)
	}
}

func TestRangePeaksValidates(t *testing.T) {
	p, _ := noiseWAV(t, 100)
	if _, err := RangePeaks(p, 0, 100, 0); err == nil {
		t.Error("buckets=0 accepted")
	}
	if _, err := RangePeaks(p, 0, 100, MaxRangeBuckets+1); err == nil {
		t.Error("buckets over the cap accepted")
	}
	if _, err := RangePeaks(p, 50, 200, 4); !errors.Is(err, ErrRange) {
		t.Errorf("out of range: %v", err)
	}
	// More buckets than frames is fine: buckets past the audio are empty.
	if _, err := RangePeaks(p, 0, 10, 20); err != nil {
		t.Errorf("more buckets than frames: %v", err)
	}
}
