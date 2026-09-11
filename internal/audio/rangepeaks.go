package audio

import (
	"fmt"
	"math"
)

// MaxRangeBuckets caps a range-peaks request. 4096 is two buckets per device
// pixel on the widest phone at 3x, which is more than a waveform can show.
const MaxRangeBuckets = 4096

// RangePeaks computes min/max peaks per channel over frames [from, to) in
// exactly `buckets` buckets, reading only that range of the file. The last
// bucket absorbs any remainder frames so the count is always what was asked
// for. Buckets past the end of a very short range are empty (0,0).
func RangePeaks(path string, from, to int64, buckets int) (*PeakData, error) {
	if buckets < 1 || buckets > MaxRangeBuckets {
		return nil, fmt.Errorf("buckets must be 1..%d", MaxRangeBuckets)
	}

	info, err := ReadWAVInfo(path)
	if err != nil {
		return nil, err
	}

	frames := to - from
	per := frames / int64(buckets)
	if per < 1 {
		per = 1
	}
	bucketOf := func(frame int64) int {
		b := int((frame - from) / per)
		if b >= buckets {
			b = buckets - 1
		}
		return b
	}

	mins := make([][]float32, info.Channels)
	maxs := make([][]float32, info.Channels)
	for c := range mins {
		mins[c] = make([]float32, buckets)
		maxs[c] = make([]float32, buckets)
		for i := range mins[c] {
			mins[c][i] = float32(math.Inf(1))
			maxs[c][i] = float32(math.Inf(-1))
		}
	}

	_, err = ReadFrames(path, from, to, 1<<14, func(block []int32, first int64) error {
		ch := info.Channels
		n := len(block) / ch
		for i := 0; i < n; i++ {
			b := bucketOf(first + int64(i))
			for c := 0; c < ch; c++ {
				v := float32(float64(block[i*ch+c]) / 2147483648.0)
				if v < mins[c][b] {
					mins[c][b] = v
				}
				if v > maxs[c][b] {
					maxs[c][b] = v
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	data := make([][]float32, info.Channels)
	for c := range data {
		flat := make([]float32, 0, buckets*2)
		for i := 0; i < buckets; i++ {
			mn, mx := mins[c][i], maxs[c][i]
			if math.IsInf(float64(mn), 1) {
				mn, mx = 0, 0
			}
			flat = append(flat, mn, mx)
		}
		data[c] = flat
	}

	return &PeakData{
		Version:    1,
		Channels:   info.Channels,
		SampleRate: info.SampleRate,
		Duration:   float64(frames) / float64(info.SampleRate),
		Buckets:    buckets,
		From:       from,
		Data:       data,
	}, nil
}
