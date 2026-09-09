package audio

import (
	"math"
	"sync"
)

// EdgeSeconds is the age at the ribbon's right edge, and the newest age the
// log axis can address. Fixed at the client's poll interval: a smaller value
// would draw detail a 1 Hz poll cannot deliver.
const EdgeSeconds = 1.0

// signalByte is the level at or above which a bin counts as signal: the
// -46 dBFS gate, which quantises to byte 60 (-45.9). It is arbitrary, and it
// only holds while the room's noise floor sits below it.
//
// The threshold is inclusive (>= signalByte); scripts/take-envelope.py counts
// b > 60, an exclusive threshold one quantisation step (0.235 dB) tighter.
// Deliberately not reconciled -- the gate is arbitrary either way, and the
// script is a diagnostic, not a consumer of this code.
const signalByte = 60

// Envelope retains a display envelope of the whole capture ring: one byte per
// level bin, holding the peak magnitude across the save channels, dB-coded
// over FloorDB..0 as round((db - FloorDB) * 255 / -FloorDB).
//
// That is exactly meter.js's (db - FLOOR_DB) / -FLOOR_DB scaled to 0..255, and
// exactly what scripts/take-envelope.py writes, so a consumer divides by 255.
// Storing linear amplitude instead would make everything below about -20 dBFS
// look like silence.
//
// Peak, never mean: a peak survives downsampling where a mean washes out.
//
// This exists because the client cannot hold it. A browser-side ring only
// fills while the page is open, and a dashcam is something you open *after*
// the moment.
//
// 900s of 10ms bins is 90,000 bytes. PushBin is a single array store with no
// allocation, which matters because it runs on the PortAudio callback thread.
type Envelope struct {
	binSeconds   float64
	saveChannels []int

	mu       sync.Mutex
	buf      []byte
	writePos int
	total    uint64
}

// NewEnvelope sizes the ring in bins. Callers derive capBins from the config's
// ring length so it cannot drift from the audio ring.
func NewEnvelope(capBins int, saveChannels []int, binMillis int) *Envelope {
	if capBins < 1 {
		capBins = 1
	}
	if binMillis < 1 {
		binMillis = 1
	}
	ch := make([]int, len(saveChannels))
	copy(ch, saveChannels)
	return &Envelope{
		binSeconds:   float64(binMillis) / 1000,
		saveChannels: ch,
		buf:          make([]byte, capBins),
	}
}

// PushBin folds one level bin into the envelope. Safe to call from the audio
// callback: it allocates nothing, computes the dB code before taking the
// lock, and holds the lock only for the single store.
func (e *Envelope) PushBin(b Bin) {
	var peak float32
	for _, c := range e.saveChannels {
		if c < 0 || c >= len(b.Max) || c >= len(b.Min) {
			continue
		}
		if v := b.Max[c]; v > peak {
			peak = v
		}
		if v := -b.Min[c]; v > peak {
			peak = v
		}
	}
	code := codeDB(peak)

	e.mu.Lock()
	e.buf[e.writePos] = code
	e.writePos = (e.writePos + 1) % len(e.buf)
	e.total++
	e.mu.Unlock()
}

// codeDB maps a 0..1 linear magnitude onto 0..255 through the dB scale.
func codeDB(amp float32) byte {
	if amp <= 0 {
		return 0
	}
	db := 20 * math.Log10(float64(amp))
	if db <= FloorDB {
		return 0
	}
	if db > 0 {
		db = 0
	}
	return byte(math.Round((db - FloorDB) * 255 / -FloorDB))
}

// RingSeconds is the envelope's full span, filled or not. len(buf) is fixed at
// construction, so this needs no lock.
func (e *Envelope) RingSeconds() float64 {
	return float64(len(e.buf)) * e.binSeconds
}

// BufferedSeconds is how much of that span actually holds data. It matters
// because the service restarts on every deploy and on hot-plug recovery, so a
// part-filled ring is routine rather than an edge case.
func (e *Envelope) BufferedSeconds() float64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return float64(e.bufferedLocked()) * e.binSeconds
}

func (e *Envelope) bufferedLocked() int {
	if e.total >= uint64(len(e.buf)) {
		return len(e.buf)
	}
	return int(e.total)
}

// Buckets aggregates the envelope into n log-spaced buckets, oldest first, so
// bucket i draws at x = i/n across the ribbon. The mapping is
//
//	age(x) = A * (T/A)^(1-x)      A = EdgeSeconds, T = RingSeconds()
//
// which the client re-implements to place its markers, reading A and T off the
// same response so the two cannot drift apart.
//
// Linear was never viable here: at a 390px viewport it puts the 30s marker
// 13px from the edge. The log axis gives the last 30s about half the width.
//
// The newest bucket deliberately reaches age 0 rather than stopping at A, so
// audio arriving right now still shows at the right edge.
//
// The lock is held only long enough to memcpy the ring into a chronological
// scratch copy, matching Ring's Snapshot: the aggregation below runs on that
// copy, so a slow HTTP poll can never stall PushBin on the PortAudio callback
// thread.
func (e *Envelope) Buckets(n int) []byte {
	if n < 1 {
		n = 1
	}
	out := make([]byte, n)

	t := e.RingSeconds()
	a := EdgeSeconds
	// A log axis needs its newest addressable age to sit strictly inside the
	// ring -- age(x) at x=0 must be older than age(x=1), or the exponent
	// below has nothing to span. NewEnvelope(100, ..., 10) makes t equal
	// EdgeSeconds exactly, which is why this is <=, not <.
	if t <= a {
		a = t / 2
	}
	span := math.Log(t / a)

	// Sized to the ring's fixed capacity, not to what is buffered, so the
	// allocation happens before the lock is taken; only chrono[:buffered]
	// ends up meaningful.
	n2 := len(e.buf)
	chrono := make([]byte, n2)

	e.mu.Lock()
	buffered := e.bufferedLocked()
	if buffered > 0 {
		start := ((e.writePos-buffered)%n2 + n2) % n2
		c := copy(chrono, e.buf[start:])
		if c < buffered {
			copy(chrono[c:], e.buf[:buffered-c])
		}
	}
	e.mu.Unlock()

	// ageAt returns the coded byte k bins back from the newest (k=0 is
	// newest, on the local copy above). Anything not yet buffered reads as
	// 0 rather than wrapping onto stale bytes -- the property that makes a
	// part-filled ring report its unwritten region correctly.
	ageAt := func(k int) byte {
		if k < 0 || k >= buffered {
			return 0
		}
		return chrono[buffered-1-k]
	}

	// boundary(j) is the age-index cut point at fractional position x=j/n
	// along the log axis, oldest to newest (j=0..n). Bucket i then spans
	// [boundary(i+1), boundary(i)): the same integer serves as one bucket's
	// lower bound and its neighbour's upper bound, so adjacent buckets can
	// neither overlap nor skip a bin the way independently-rounded
	// oldAge/newAge (one ceil'd, one truncated) used to.
	//
	// The two ends are exact integers by construction -- n2 and 0 -- rather
	// than round-tripped through Exp(Log(...)), which can land a ULP off
	// and silently drop the outermost bin on either edge.
	//
	// Each boundary is computed fresh from the formula rather than carried
	// forward and decremented: a carried "always shrink by at least one"
	// budget can deplete before the loop reaches the newest bucket once n
	// is large relative to the ring's bin resolution, silently zeroing the
	// most important bucket -- the one showing audio arriving right now.
	// Computed independently, boundary(n-1) is bounded below by roughly
	// EdgeSeconds/binSeconds bins (~100 for this app's 1s edge and 10ms
	// bins) no matter how large n is, so the newest bucket never collapses.
	boundary := func(j int) int {
		if j == 0 {
			return n2
		}
		if j == n {
			return 0
		}
		x := float64(j) / float64(n)
		age := a * math.Exp(span*(1-x))
		return int(age / e.binSeconds)
	}

	hi := boundary(0)
	for i := 0; i < n; i++ {
		lo := boundary(i + 1)
		if lo > hi {
			lo = hi // defensive: age(x) is monotonic, so this should not trigger
		}

		var peak byte
		for k := lo; k < hi; k++ {
			if v := ageAt(k); v > peak {
				peak = v
			}
		}
		out[i] = peak
		hi = lo
	}
	return out
}

// atLocked reads the bin n places back from the write head; n=0 is the newest.
// Anything past what is buffered reads as 0.
func (e *Envelope) atLocked(n int) byte {
	if n < 0 || n >= e.bufferedLocked() {
		return 0
	}
	n2 := len(e.buf)
	return e.buf[((e.writePos-1-n)%n2+n2)%n2]
}

// SignalSeconds reports how much of the newest span carries signal, counting
// bins at or above signalByte, clamped to what is actually buffered.
//
// The client cannot compute this from Buckets: a bucket at the old end spans
// tens of seconds and its peak says only that something in there was loud.
func (e *Envelope) SignalSeconds(span float64) float64 {
	if span <= 0 {
		return 0
	}

	e.mu.Lock()
	defer e.mu.Unlock()

	bins := int(math.Round(span / e.binSeconds))
	if avail := e.bufferedLocked(); bins > avail {
		bins = avail
	}
	n := 0
	for k := 0; k < bins; k++ {
		if e.atLocked(k) >= signalByte {
			n++
		}
	}
	return float64(n) * e.binSeconds
}
