package audio

import "sync"

// Ring is a fixed-capacity circular buffer of interleaved int32 frames.
//
// Exactly one goroutine writes to it (the ring writer started by Capture);
// readers take the mutex only long enough to memcpy a snapshot out, then do
// any deinterleaving on their own copy. The PortAudio callback never touches
// this type directly and so can never block on it.
type Ring struct {
	mu        sync.Mutex
	buf       []int32
	channels  int
	capFrames int

	writePos    int    // sample index of the next write
	totalFrames uint64 // frames ever written; saturates the ring once >= capFrames
}

func NewRing(capFrames, channels int) *Ring {
	return &Ring{
		buf:       make([]int32, capFrames*channels),
		channels:  channels,
		capFrames: capFrames,
	}
}

// WriteFrames appends a block and accounts for it in frames rather than samples.
func (r *Ring) WriteFrames(block []int32) {
	frames := len(block) / r.channels
	r.mu.Lock()
	n := len(r.buf)
	for len(block) > 0 {
		c := copy(r.buf[r.writePos:], block)
		block = block[c:]
		r.writePos = (r.writePos + c) % n
	}
	r.totalFrames += uint64(frames)
	r.mu.Unlock()
}

// BufferedFrames reports how many frames are currently retrievable.
func (r *Ring) BufferedFrames() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.bufferedLocked()
}

func (r *Ring) bufferedLocked() int {
	if r.totalFrames >= uint64(r.capFrames) {
		return r.capFrames
	}
	return int(r.totalFrames)
}

// TotalFrames reports how many frames have ever been written. It is monotonic
// for the life of the Ring, and the Ring is built once in NewCapture and never
// rebuilt -- supervise() reopens the stream, not the ring -- so this is a valid
// absolute clock across USB unplugs. Flags are stored against it.
//
// It advances only when audio arrives, so it stalls during a dropout. That is
// deliberate: a mark stays pinned to the audio rather than to wall clock.
func (r *Ring) TotalFrames() uint64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.totalFrames
}

// Snapshot copies the most recent `frames` frames out in chronological order.
// It clamps to what is actually buffered and returns the interleaved copy plus
// the frame count.
func (r *Ring) Snapshot(frames int) ([]int32, int) {
	data, got, _ := r.SnapshotAt(frames)
	return data, got
}

// SnapshotAt is Snapshot plus the absolute frame the window ends at, read under
// the same lock acquisition as the copy itself. Callers mapping absolute
// positions into the returned window must use this rather than pairing
// Snapshot with a separate TotalFrames call: a write landing between the two
// would shift the window out from under the position.
//
// The window covers absolute frames [endFrame-got, endFrame).
func (r *Ring) SnapshotAt(frames int) ([]int32, int, uint64) {
	r.mu.Lock()

	avail := r.bufferedLocked()
	if frames <= 0 || frames > avail {
		frames = avail
	}
	if frames == 0 {
		r.mu.Unlock()
		return nil, 0, 0
	}

	n := len(r.buf)
	want := frames * r.channels
	start := ((r.writePos-want)%n + n) % n

	out := make([]int32, want)
	c := copy(out, r.buf[start:])
	if c < want {
		copy(out[c:], r.buf[:want-c])
	}
	end := r.totalFrames

	r.mu.Unlock()
	return out, frames, end
}

// Capacity returns the ring size in frames.
func (r *Ring) Capacity() int { return r.capFrames }

// Channels returns the interleave width.
func (r *Ring) Channels() int { return r.channels }
