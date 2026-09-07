package audio

import (
	"math"
	"sync"
)

// Bin is one time slice of level data, roughly 10ms wide. Min/Max carry the
// true sample extremes so the client can draw an actual waveform envelope
// rather than mirroring a single RMS scalar.
type Bin struct {
	Min []float32 `json:"min"` // per channel, -1..1
	Max []float32 `json:"max"` // per channel, -1..1
	RMS []float32 `json:"rms"` // per channel, dBFS (-inf clamped to floorDB)
}

// FloorDB is the bottom of the meter scale. Anything quieter reads as silence.
const FloorDB = -60.0

// Frame is what gets pushed to a websocket subscriber: the bins accumulated
// since the previous frame, plus held peaks.
type Frame struct {
	Bins []Bin     `json:"bins"`
	Peak []float32 `json:"peak"` // per channel, dBFS, decaying peak hold
	Clip []bool    `json:"clip"` // per channel, sample hit full scale in this frame
}

// Levels accumulates per-channel extremes into fixed-width bins and fans the
// result out to subscribers. All methods are safe for concurrent use; the
// accumulate path is allocation-free so it can run on the audio callback.
type Levels struct {
	channels   int
	binFrames  int // frames per bin
	sampleRate int

	mu       sync.Mutex
	curMin   []float32
	curMax   []float32
	curSumSq []float64
	curCount int
	pending  []Bin
	peakHold []float32
	clip     []bool

	subsMu sync.Mutex
	subs   map[chan Frame]struct{}
}

func NewLevels(channels, sampleRate, binMillis int) *Levels {
	binFrames := sampleRate * binMillis / 1000
	if binFrames < 1 {
		binFrames = 1
	}
	l := &Levels{
		channels:   channels,
		binFrames:  binFrames,
		sampleRate: sampleRate,
		curMin:     make([]float32, channels),
		curMax:     make([]float32, channels),
		curSumSq:   make([]float64, channels),
		peakHold:   make([]float32, channels),
		clip:       make([]bool, channels),
		subs:       map[chan Frame]struct{}{},
	}
	l.resetBin()
	for i := range l.peakHold {
		l.peakHold[i] = FloorDB
	}
	return l
}

func (l *Levels) resetBin() {
	for i := 0; i < l.channels; i++ {
		l.curMin[i] = 0
		l.curMax[i] = 0
		l.curSumSq[i] = 0
	}
	l.curCount = 0
}

// Accumulate folds one interleaved block into the current bins.
func (l *Levels) Accumulate(block []int32) {
	const scale = 1.0 / 2147483648.0
	ch := l.channels

	l.mu.Lock()
	for off := 0; off+ch <= len(block); off += ch {
		for c := 0; c < ch; c++ {
			v := float32(float64(block[off+c]) * scale)
			if v < l.curMin[c] {
				l.curMin[c] = v
			}
			if v > l.curMax[c] {
				l.curMax[c] = v
			}
			l.curSumSq[c] += float64(v) * float64(v)
			if v >= 0.999 || v <= -0.999 {
				l.clip[c] = true
			}
		}
		l.curCount++
		if l.curCount >= l.binFrames {
			l.flushBinLocked()
		}
	}
	l.mu.Unlock()
}

func (l *Levels) flushBinLocked() {
	b := Bin{
		Min: make([]float32, l.channels),
		Max: make([]float32, l.channels),
		RMS: make([]float32, l.channels),
	}
	n := float64(l.curCount)
	for c := 0; c < l.channels; c++ {
		b.Min[c] = l.curMin[c]
		b.Max[c] = l.curMax[c]
		db := FloorDB
		if n > 0 {
			if r := math.Sqrt(l.curSumSq[c] / n); r > 0 {
				db = 20 * math.Log10(r)
			}
		}
		if db < FloorDB || math.IsNaN(db) {
			db = FloorDB
		}
		b.RMS[c] = float32(db)
		if b.RMS[c] > l.peakHold[c] {
			l.peakHold[c] = b.RMS[c]
		}
	}
	l.pending = append(l.pending, b)
	// Bound the backlog if nobody is draining (no clients connected).
	if len(l.pending) > 512 {
		l.pending = l.pending[len(l.pending)-512:]
	}
	l.resetBin()
}

// Drain removes and returns the bins accumulated since the last call, plus the
// current peak-hold and clip state. Peak hold decays each drain so it falls
// back toward the signal instead of latching forever.
func (l *Levels) Drain() Frame {
	l.mu.Lock()
	defer l.mu.Unlock()

	f := Frame{
		Bins: l.pending,
		Peak: make([]float32, l.channels),
		Clip: make([]bool, l.channels),
	}
	l.pending = nil
	copy(f.Peak, l.peakHold)
	copy(f.Clip, l.clip)

	for i := range l.peakHold {
		l.peakHold[i] -= 1.5 // dB per drain tick
		if l.peakHold[i] < FloorDB {
			l.peakHold[i] = FloorDB
		}
		l.clip[i] = false
	}
	return f
}

// Snapshot reports the current per-channel RMS in dBFS without consuming bins.
// Used by /api/status and for the channel-routing diagnostic.
func (l *Levels) Snapshot() []float32 {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]float32, l.channels)
	for c := range out {
		out[c] = FloorDB
	}
	if len(l.pending) > 0 {
		copy(out, l.pending[len(l.pending)-1].RMS)
	}
	return out
}

// Subscribe returns a channel of frames and a cancel func.
func (l *Levels) Subscribe() (<-chan Frame, func()) {
	ch := make(chan Frame, 8)
	l.subsMu.Lock()
	l.subs[ch] = struct{}{}
	l.subsMu.Unlock()

	return ch, func() {
		l.subsMu.Lock()
		if _, ok := l.subs[ch]; ok {
			delete(l.subs, ch)
			close(ch)
		}
		l.subsMu.Unlock()
	}
}

// Broadcast drains pending bins and pushes them to every subscriber. Slow
// subscribers are skipped rather than allowed to stall the publisher.
func (l *Levels) Broadcast() {
	l.subsMu.Lock()
	if len(l.subs) == 0 {
		l.subsMu.Unlock()
		return
	}
	l.subsMu.Unlock()

	f := l.Drain()

	l.subsMu.Lock()
	for ch := range l.subs {
		select {
		case ch <- f:
		default:
		}
	}
	l.subsMu.Unlock()
}

// HasSubscribers reports whether anyone is listening.
func (l *Levels) HasSubscribers() bool {
	l.subsMu.Lock()
	defer l.subsMu.Unlock()
	return len(l.subs) > 0
}
