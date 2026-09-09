# Buffer Ribbon Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Draw the capture ring as a waveform on a logarithmic age axis with a marker at each capture tier, so you can see which button catches your take before you press it.

**Architecture:** The server retains a display envelope of the whole ring — one dB-coded byte per 10 ms level bin, peak across the save channels, 88 KiB for 900 s — written from `Levels.flushBinLocked`, which already computes those bins and throws them away. `GET /api/envelope` aggregates that into log-spaced buckets plus seconds-of-signal per capture tier. A new `static/lib/ribbon.js` polls it at 1 Hz and renders DOM + SVG into the existing `.viz-wrap`, replacing the `Visualizer` class, which is deleted.

**Tech Stack:** Go 1.x (stdlib + gorilla/mux), vanilla ES modules, no build step. Go tests run on the Mac — `audio/envelope.go` is pure Go, so unlike `capture.go` it needs no cgo or PortAudio.

**Spec:** `docs/superpowers/specs/2026-09-08-buffer-ribbon-design.md` (approved, `c49d9b2`).

---

## Before you start

- **Work from `v2-go/`** for all Go commands. `git` paths below are from the repo root.
- **`go test ./...` already passes** (34 tests). If it does not, stop and fix that first — you cannot tell your own breakage from pre-existing breakage otherwise.
- **Verify every regression test actually fails before you fix it.** This project has shipped a test that could not fail and caught it only by reverting the fix. Step "run it to see it fail" is not ceremony here.
- **Do not touch `dashcam.env` on the Pi.** Ring length lives in both `config.go` and that untracked file; nothing in this plan changes either.

## Three deviations from the spec, already amended into it

1. **`buckets` ships as a base64 string, not a JSON array of ints.** 400 buckets is 536 base64 chars against ~1600 as JSON ints, it is what `scripts/take-envelope.py` already writes, it is what the mockup's client already decodes, and Go marshals `[]byte` to base64 with no conversion code. Smaller on a slow link, less code, established encoding.
2. **The newest bucket extends to age 0**, not down to `A`. Otherwise the freshest second is in no bucket at all and never reaches the right edge. This makes the spec's "1–2 s lag" claim conservative rather than wrong.
3. **`.stale::after` does not actually need `z-index` to paint on top** — generated `::after` content is already the last box among the element's children, so DOM order alone puts it above the ribbon. It is still set explicitly, as insurance against a future ribbon child taking a `z-index`.

---

## Corrections found during execution

Recorded rather than quietly patched, because all three are defects in *this
plan* and each is easy to repeat.

**Tasks 1 and 2 could not produce two working commits.** Task 1's tests read
values back through `Buckets(1)[0]`, but `Buckets` is Task 2's deliverable — so
four of Task 1's five tests depend on code Task 1 does not contain, and a
commit at the end of Task 1 does not compile. Task 1's commit step is removed
and the two now share one commit. The general lesson: **a task boundary is not
real if the earlier task's tests can only observe behaviour through the later
task's API.** Split by observable behaviour, not by file.

**The `Buckets` code specified below held a lock the audio callback needs.** It
held `Envelope.mu` across the whole aggregation — ~150 µs measured on an M1, an
estimated 1–2 ms on the Pi — while `PushBin` wants that same mutex every 10 ms
from the PortAudio callback thread. `audio/ring.go` had already established and
documented the right pattern one file over: *take the mutex only long enough to
memcpy a snapshot out, then do the work on your own copy.* The shipped code
follows `Ring.Snapshot`. **The `Buckets` listing in Task 2 is left as
originally written; read the committed source for its final shape.**

**Tasks 6 and 7 left the app broken between commits.** Task 6 deleted the
visualiser and nothing replaced it until Task 7, so the intermediate commit
renders a dead panel — and Task 8 deploys to a live device. They are re-cut
into three tasks (6, 7, 7b) so every commit leaves a working UI.

---

## File structure

| File | Responsibility |
|---|---|
| `v2-go/audio/envelope.go` | **Create.** Retained display envelope: fixed byte ring, dB coding, log bucketing, signal counting. Pure Go, no cgo. |
| `v2-go/audio/envelope_test.go` | **Create.** Unit tests for all of the above. |
| `v2-go/audio/levels.go` | **Modify.** One field, one setter, one call in `flushBinLocked`. |
| `v2-go/audio/levels_test.go` | **Modify.** One test that bins reach the envelope. |
| `v2-go/audio/capture.go` | **Modify.** Build the envelope, attach it to `Levels`, expose it. |
| `v2-go/api/api.go` | **Modify.** `New` takes the envelope; `GET /api/envelope`. |
| `v2-go/api/api_test.go` | **Modify.** Endpoint tests against a real `*audio.Envelope`. |
| `v2-go/main.go` | **Modify.** Pass `cap.Envelope()` into `api.New`. |
| `v2-go/static/lib/ribbon.js` | **Create.** The ribbon component: fetch, axis, render, readout. |
| `v2-go/static/lib/meter.js` | **Modify.** Delete `Visualizer`; gain `fmtDur`. |
| `v2-go/static/app.js` | **Modify.** Swap `Visualizer` for `Ribbon`; drop the local `fmtDur`. |
| `v2-go/static/index.html` | **Modify.** Move `.viz-wrap` out of the monitor panel; drop the canvas. |
| `v2-go/static/styles.css` | **Modify.** Ribbon internals, the grid span, the stale z-index. |
| `v2-go/static/sw.js` | **Modify.** Add `/lib/ribbon.js` to `SHELL`, bump `CACHE`. |

---

### Task 1: The envelope ring — storage, dB coding, buffered accounting

**Files:**
- Create: `v2-go/audio/envelope.go`
- Test: `v2-go/audio/envelope_test.go`

- [ ] **Step 1: Write the failing tests**

Create `v2-go/audio/envelope_test.go`:

```go
package audio

import "testing"

// binAt builds a one-channel Bin whose peak magnitude is amp.
func binAt(amp float32) Bin {
	return Bin{Min: []float32{-amp}, Max: []float32{amp}, RMS: []float32{0}}
}

func TestEnvelopeCodesDBOnTheMeterScale(t *testing.T) {
	e := NewEnvelope(10, []int{0}, 10)
	// 0.25 is -12.04 dBFS. meter.js maps that to (db+60)/60; scaled to 0..255
	// that is round(47.96 * 4.25) = 204.
	e.PushBin(binAt(0.25))
	if got := e.Buckets(1)[0]; got != 204 {
		t.Fatalf("byte = %d, want 204 (-12.04 dBFS on the 0..255 dB scale)", got)
	}
}

func TestEnvelopeCodesSilenceAndSubFloorAsZero(t *testing.T) {
	e := NewEnvelope(10, []int{0}, 10)
	e.PushBin(binAt(0))
	if got := e.Buckets(1)[0]; got != 0 {
		t.Fatalf("digital silence coded as %d, want 0", got)
	}

	e2 := NewEnvelope(10, []int{0}, 10)
	e2.PushBin(binAt(0.0005)) // about -66 dBFS, below FloorDB
	if got := e2.Buckets(1)[0]; got != 0 {
		t.Fatalf("sub-floor coded as %d, want 0", got)
	}
}

func TestEnvelopeTakesThePeakAcrossSaveChannels(t *testing.T) {
	// Channel 1 is louder, and only channels 0 and 1 are saved, so channel 2
	// being louder still must not show up.
	e := NewEnvelope(10, []int{0, 1}, 10)
	e.PushBin(Bin{
		Min: []float32{-0.01, -0.25, -0.9},
		Max: []float32{0.01, 0.25, 0.9},
		RMS: []float32{0, 0, 0},
	})
	if got := e.Buckets(1)[0]; got != 204 {
		t.Fatalf("byte = %d, want 204 (channel 1 at 0.25, channel 2 not saved)", got)
	}
}

func TestEnvelopeUsesNegativePeaksToo(t *testing.T) {
	// A waveform can be asymmetric; the magnitude is what matters.
	e := NewEnvelope(10, []int{0}, 10)
	e.PushBin(Bin{Min: []float32{-0.25}, Max: []float32{0.001}, RMS: []float32{0}})
	if got := e.Buckets(1)[0]; got != 204 {
		t.Fatalf("byte = %d, want 204 from the negative peak", got)
	}
}

func TestEnvelopeBufferedGrowsThenSaturates(t *testing.T) {
	e := NewEnvelope(10, []int{0}, 10) // 10 bins x 10ms = 0.1s capacity
	if got := e.BufferedSeconds(); got != 0 {
		t.Fatalf("fresh envelope buffered %v, want 0", got)
	}
	for i := 0; i < 4; i++ {
		e.PushBin(binAt(0.25))
	}
	if got := e.BufferedSeconds(); got < 0.039 || got > 0.041 {
		t.Fatalf("buffered = %v, want 0.04 after 4 bins", got)
	}
	for i := 0; i < 50; i++ {
		e.PushBin(binAt(0.25))
	}
	if got := e.BufferedSeconds(); got < 0.099 || got > 0.101 {
		t.Fatalf("buffered = %v, want 0.1 once wrapped", got)
	}
	if got := e.RingSeconds(); got < 0.099 || got > 0.101 {
		t.Fatalf("ring = %v, want 0.1", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
cd v2-go && go test ./audio/ -run TestEnvelope -v
```

Expected: FAIL — `undefined: NewEnvelope`.

- [ ] **Step 3: Write the implementation**

Create `v2-go/audio/envelope.go`:

```go
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
// callback: it allocates nothing and holds the lock for a single store.
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

	e.mu.Lock()
	e.buf[e.writePos] = codeDB(peak)
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

// atLocked reads the bin n places back from the write head; n=0 is the newest.
// Anything past what is buffered reads as 0.
func (e *Envelope) atLocked(n int) byte {
	if n < 0 || n >= e.bufferedLocked() {
		return 0
	}
	n2 := len(e.buf)
	return e.buf[((e.writePos-1-n)%n2+n2)%n2]
}
```

- [ ] **Step 4: Do not run or commit yet — continue straight into Task 2**

These tests cannot pass, or even compile, on their own: four of the five read
values back through `Buckets(1)[0]`, and `Buckets` is Task 2's deliverable.
That is a flaw in how this plan was cut, not something to work around.

**Task 1 has no commit step.** Write Task 2's tests and implementation, then
run and commit both tasks together as a single commit.

---

### Task 2: Log-spaced bucketing

**Files:**
- Modify: `v2-go/audio/envelope.go`
- Test: `v2-go/audio/envelope_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `v2-go/audio/envelope_test.go`:

```go
// pushRamp fills the envelope with n bins whose amplitude climbs steadily, so
// the newest audio is the loudest and any ordering mistake is obvious.
func pushRamp(e *Envelope, n int) {
	for i := 0; i < n; i++ {
		e.PushBin(binAt(float32(0.002 + 0.9*float64(i)/float64(n))))
	}
}

func TestBucketsRunOldestFirst(t *testing.T) {
	e := NewEnvelope(1000, []int{0}, 10)
	pushRamp(e, 1000)

	b := e.Buckets(20)
	if len(b) != 20 {
		t.Fatalf("got %d buckets, want 20", len(b))
	}
	if b[0] >= b[len(b)-1] {
		t.Fatalf("buckets are not oldest-first: first=%d last=%d", b[0], b[len(b)-1])
	}
}

func TestBucketsTakeThePeakNotTheMean(t *testing.T) {
	// One loud bin in an otherwise quiet stretch must survive into its bucket.
	// A mean over ~50 bins would bury it.
	e := NewEnvelope(1000, []int{0}, 10)
	for i := 0; i < 1000; i++ {
		if i == 100 { // 900 bins back from the head, i.e. 9s old
			e.PushBin(binAt(0.5))
			continue
		}
		e.PushBin(binAt(0.002))
	}

	b := e.Buckets(20)
	var max byte
	for _, v := range b {
		if v > max {
			max = v
		}
	}
	// 0.5 is -6.02 dBFS -> round(53.98 * 4.25) = 229.
	if max != 229 {
		t.Fatalf("loudest bucket = %d, want 229; a peak was averaged away", max)
	}
}

func TestNewestBucketReachesAgeZero(t *testing.T) {
	// Ages below EdgeSeconds must still land at the right edge, or audio
	// arriving right now is in no bucket at all.
	e := NewEnvelope(90000, []int{0}, 10) // 900s
	for i := 0; i < 90000; i++ {
		e.PushBin(binAt(0.002))
	}
	e.PushBin(binAt(0.5)) // newest bin, ~0s old

	b := e.Buckets(400)
	if b[len(b)-1] != 229 {
		t.Fatalf("newest bucket = %d, want 229; the freshest audio never reached the edge", b[len(b)-1])
	}
}

func TestBucketsAreZeroBeyondWhatIsBuffered(t *testing.T) {
	// A part-filled ring must not report its unwritten region as silence --
	// the client draws that as a hatch, and it can only tell them apart from
	// buffered_seconds.
	e := NewEnvelope(90000, []int{0}, 10) // 900s capacity
	for i := 0; i < 3000; i++ {           // only 30s written
		e.PushBin(binAt(0.5))
	}

	b := e.Buckets(400)
	if b[0] != 0 {
		t.Fatalf("oldest bucket = %d, want 0 for never-written time", b[0])
	}
	if b[len(b)-1] == 0 {
		t.Fatalf("newest bucket is 0, but 30s of loud audio was written")
	}
}

func TestBucketsClampToAtLeastOne(t *testing.T) {
	e := NewEnvelope(100, []int{0}, 10)
	pushRamp(e, 100)
	if got := len(e.Buckets(0)); got != 1 {
		t.Fatalf("Buckets(0) returned %d buckets, want 1", got)
	}
	if got := len(e.Buckets(-5)); got != 1 {
		t.Fatalf("Buckets(-5) returned %d buckets, want 1", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
cd v2-go && go test ./audio/ -run 'TestBuckets|TestNewestBucket|TestEnvelope' -v
```

Expected: FAIL — `e.Buckets undefined`.

- [ ] **Step 3: Write the implementation**

Append to `v2-go/audio/envelope.go`:

```go
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
func (e *Envelope) Buckets(n int) []byte {
	if n < 1 {
		n = 1
	}
	out := make([]byte, n)

	t := e.RingSeconds()
	a := EdgeSeconds
	// Guards a ring shorter than the edge age, which only happens in tests.
	if t <= a {
		a = t / 2
	}
	span := math.Log(t / a)

	e.mu.Lock()
	defer e.mu.Unlock()

	for i := 0; i < n; i++ {
		oldAge := a * math.Exp(span*(1-float64(i)/float64(n)))
		newAge := a * math.Exp(span*(1-float64(i+1)/float64(n)))
		if i == n-1 {
			newAge = 0
		}

		from := int(newAge / e.binSeconds)
		to := int(math.Ceil(oldAge / e.binSeconds))
		if to <= from {
			to = from + 1
		}

		var peak byte
		for k := from; k < to; k++ {
			if v := e.atLocked(k); v > peak {
				peak = v
			}
		}
		out[i] = peak
	}
	return out
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
cd v2-go && go test ./audio/ -run 'TestBuckets|TestNewestBucket|TestEnvelope' -v
```

Expected: PASS, all of Task 1's and Task 2's tests.

- [ ] **Step 5: Commit both tasks together**

One commit, not two — Task 1's tests depend on Task 2's code, so a commit
between them would not compile:

```bash
git add v2-go/audio/envelope.go v2-go/audio/envelope_test.go
git commit -m "Add a retained level envelope for the buffer ribbon"
```

---

### Task 3: Seconds of signal per span

**Files:**
- Modify: `v2-go/audio/envelope.go`
- Test: `v2-go/audio/envelope_test.go`

This is what lets the readout say a span would save nothing. It has to be server-side: a far-left bucket spans ~60 s, and its peak only says *something* in there was loud, not how much.

- [ ] **Step 1: Write the failing tests**

Append to `v2-go/audio/envelope_test.go`:

```go
func TestSignalSecondsCountsOnlyTheNewestSpan(t *testing.T) {
	e := NewEnvelope(1000, []int{0}, 10) // 10s capacity
	for i := 0; i < 500; i++ {           // older half: loud
		e.PushBin(binAt(0.5))
	}
	for i := 0; i < 500; i++ { // newer half: silent
		e.PushBin(binAt(0))
	}

	if got := e.SignalSeconds(2); got != 0 {
		t.Fatalf("newest 2s reports %vs of signal, want 0 -- it is silent", got)
	}
	if got := e.SignalSeconds(10); got < 4.9 || got > 5.1 {
		t.Fatalf("whole ring reports %vs of signal, want ~5", got)
	}
}

func TestSignalSecondsThresholdIsByte60(t *testing.T) {
	// byte 60 is -45.9 dBFS: amplitude 10^(-45.88/20) = 0.005082.
	// Just under it must not count; just over it must.
	quiet := NewEnvelope(100, []int{0}, 10)
	for i := 0; i < 100; i++ {
		quiet.PushBin(binAt(0.0049))
	}
	if got := quiet.SignalSeconds(1); got != 0 {
		t.Fatalf("below the gate counted %vs, want 0", got)
	}

	loud := NewEnvelope(100, []int{0}, 10)
	for i := 0; i < 100; i++ {
		loud.PushBin(binAt(0.0053))
	}
	if got := loud.SignalSeconds(1); got < 0.99 || got > 1.01 {
		t.Fatalf("above the gate counted %vs, want ~1", got)
	}
}

func TestSignalSecondsClampsToWhatIsBuffered(t *testing.T) {
	e := NewEnvelope(1000, []int{0}, 10) // 10s capacity
	for i := 0; i < 200; i++ {           // only 2s written, all loud
		e.PushBin(binAt(0.5))
	}
	// Asking for 10s must not invent 8s of signal from unwritten bins.
	if got := e.SignalSeconds(10); got < 1.9 || got > 2.1 {
		t.Fatalf("got %vs of signal from a 2s buffer, want ~2", got)
	}
}

func TestSignalSecondsOfANonPositiveSpanIsZero(t *testing.T) {
	e := NewEnvelope(100, []int{0}, 10)
	for i := 0; i < 100; i++ {
		e.PushBin(binAt(0.5))
	}
	if got := e.SignalSeconds(0); got != 0 {
		t.Fatalf("SignalSeconds(0) = %v, want 0", got)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
cd v2-go && go test ./audio/ -run TestSignalSeconds -v
```

Expected: FAIL — `e.SignalSeconds undefined`.

- [ ] **Step 3: Write the implementation**

Append to `v2-go/audio/envelope.go`:

```go
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
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
cd v2-go && go test ./audio/ -v && go vet ./... && gofmt -l .
```

Expected: PASS for every audio test; `go vet` silent; `gofmt -l` prints nothing.

- [ ] **Step 5: Commit**

```bash
git add v2-go/audio/envelope.go v2-go/audio/envelope_test.go
git commit -m "Count seconds of signal in a span"
```

---

### Task 4: Wire the envelope into Levels and Capture

**Files:**
- Modify: `v2-go/audio/levels.go:44` (struct), `v2-go/audio/levels.go:145` (`flushBinLocked`)
- Modify: `v2-go/audio/capture.go:48-68`
- Test: `v2-go/audio/levels_test.go`

- [ ] **Step 1: Write the failing test**

Append to `v2-go/audio/levels_test.go`:

```go
func TestLevelsFeedTheEnvelope(t *testing.T) {
	// The envelope exists so the ribbon can show audio from before the page
	// was opened, which means bins must reach it whether or not anything is
	// subscribed to the websocket.
	l := NewLevels(2, 48000, 10) // 480 frames per bin
	e := NewEnvelope(100, []int{0, 1}, 10)
	l.SetEnvelope(e)

	l.Accumulate(oneBinOfConstant(2, 480, 1<<29)) // 0.25, about -12 dBFS

	if got := e.BufferedSeconds(); got < 0.009 || got > 0.011 {
		t.Fatalf("envelope buffered %vs after one bin, want 0.01", got)
	}
	if got := e.Buckets(1)[0]; got != 204 {
		t.Fatalf("envelope byte = %d, want 204", got)
	}
}

func TestLevelsWithoutAnEnvelopeStillWork(t *testing.T) {
	// Every existing test builds Levels with no envelope; that must stay valid.
	l := NewLevels(2, 48000, 10)
	l.Accumulate(oneBinOfConstant(2, 480, 1<<29))
	if l.Snapshot()[0] <= FloorDB {
		t.Fatal("Levels broke when no envelope was attached")
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
cd v2-go && go test ./audio/ -run TestLevels -v
```

Expected: FAIL — `l.SetEnvelope undefined`.

- [ ] **Step 3: Write the implementation**

In `v2-go/audio/levels.go`, add the field to the `Levels` struct, immediately after `lastRMS`:

```go
	// env retains bins past the 40ms Drain window so the ribbon can show audio
	// from before the page was opened. Nil in tests and whenever the feature
	// is not wired up.
	env *Envelope
```

Add the setter after `NewLevels`:

```go
// SetEnvelope attaches a retained envelope. Bins are pushed to it as they
// flush, which is the only way the ribbon can show audio from before the page
// was opened.
func (l *Levels) SetEnvelope(e *Envelope) {
	l.mu.Lock()
	l.env = e
	l.mu.Unlock()
}
```

In `flushBinLocked`, immediately after `copy(l.lastRMS, b.RMS)`:

```go
	if l.env != nil {
		l.env.PushBin(b)
	}
```

In `v2-go/audio/capture.go`, add a named constant above `NewCapture`:

```go
// levelBinMillis is the width of one level bin. The envelope's capacity is
// derived from it, so the two cannot drift.
const levelBinMillis = 10
```

Add the field to the `Capture` struct next to `levels`:

```go
	env *Envelope
```

Replace the `levels:` line in `NewCapture` and wire the envelope after the struct literal:

```go
		levels: NewLevels(cfg.Channels, cfg.SampleRate, levelBinMillis),
```

```go
	// Capacity comes from the same RingSeconds as the audio ring, so the
	// ribbon's timeline cannot drift from what Capture would actually write.
	// SaveChannels is what the meters and the takes use, so the ribbon shows
	// the same pair even under SAVE_ALL_CHANNELS.
	c.env = NewEnvelope(cfg.RingSeconds*1000/levelBinMillis, cfg.SaveChannels, levelBinMillis)
	c.levels.SetEnvelope(c.env)
```

Put those two lines just before `c.deviceName.Store("")`. Add the accessor next to `Levels()`:

```go
func (c *Capture) Envelope() *Envelope { return c.env }
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
cd v2-go && go build ./... && go test ./audio/ -v
```

Expected: build clean, all audio tests PASS.

- [ ] **Step 5: Commit**

```bash
git add v2-go/audio/levels.go v2-go/audio/levels_test.go v2-go/audio/capture.go
git commit -m "Feed level bins into the retained envelope"
```

---

### Task 5: `GET /api/envelope`

**Files:**
- Modify: `v2-go/api/api.go:27-45` (struct, `New`, routes), plus a new handler
- Modify: `v2-go/main.go:40`
- Test: `v2-go/api/api_test.go`

`/api/peaks` is already the per-take sidecar, hence a new name. The endpoint takes an injected `*audio.Envelope` rather than reaching through `a.cap`, because `Capture` needs cgo and PortAudio and these tests must run on the Mac.

- [ ] **Step 1: Write the failing tests**

Append to `v2-go/api/api_test.go`:

```go
// newEnvelopeAPI builds an API over a real envelope holding `bins` loud bins.
// Capture and Saver stay nil: the envelope handler never touches them.
func newEnvelopeAPI(t *testing.T, capBins, bins int) *mux.Router {
	t.Helper()
	e := audio.NewEnvelope(capBins, []int{0}, 10)
	for i := 0; i < bins; i++ {
		e.PushBin(audio.Bin{Min: []float32{-0.5}, Max: []float32{0.5}, RMS: []float32{0}})
	}
	a := New(&config.Config{OutputDir: t.TempDir()}, nil, nil, e)
	r := mux.NewRouter()
	a.SetupRoutes(r)
	return r
}

func getEnvelope(t *testing.T, r *mux.Router, query string) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/envelope"+query, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("bad JSON: %v (%s)", err, w.Body.String())
	}
	return body
}

func TestEnvelopeReportsRingAndBuffered(t *testing.T) {
	r := newEnvelopeAPI(t, 90000, 3000) // 900s ring, 30s written
	body := getEnvelope(t, r, "?buckets=64")

	if got := body["ring_seconds"].(float64); got != 900 {
		t.Errorf("ring_seconds = %v, want 900", got)
	}
	if got := body["buffered_seconds"].(float64); got < 29.9 || got > 30.1 {
		t.Errorf("buffered_seconds = %v, want ~30", got)
	}
	if got := body["edge_seconds"].(float64); got != 1 {
		t.Errorf("edge_seconds = %v, want 1", got)
	}
}

func TestEnvelopeBucketsAreBase64OfTheRequestedLength(t *testing.T) {
	r := newEnvelopeAPI(t, 90000, 90000)
	body := getEnvelope(t, r, "?buckets=64")

	raw, err := base64.StdEncoding.DecodeString(body["buckets"].(string))
	if err != nil {
		t.Fatalf("buckets is not base64: %v", err)
	}
	if len(raw) != 64 {
		t.Fatalf("got %d buckets, want 64", len(raw))
	}
	if raw[len(raw)-1] == 0 {
		t.Fatal("newest bucket is silent, but the whole ring is loud")
	}
}

func TestEnvelopeSignalSecondsIsParallelToSpans(t *testing.T) {
	r := newEnvelopeAPI(t, 90000, 3000) // 30s of loud audio
	body := getEnvelope(t, r, "?buckets=16&spans=10,30")

	sig := body["signal_seconds"].([]any)
	if len(sig) != 2 {
		t.Fatalf("got %d signal_seconds for 2 spans", len(sig))
	}
	if v := sig[0].(float64); v < 9.9 || v > 10.1 {
		t.Errorf("10s span reports %v, want ~10", v)
	}
	if v := sig[1].(float64); v < 29.9 || v > 30.1 {
		t.Errorf("30s span reports %v, want ~30", v)
	}
}

func TestEnvelopeSpanZeroMeansTheWholeRing(t *testing.T) {
	// The UI's Full button sends 0, matching /api/trigger.
	r := newEnvelopeAPI(t, 1000, 1000) // 10s ring, fully loud
	body := getEnvelope(t, r, "?buckets=8&spans=0")

	sig := body["signal_seconds"].([]any)
	if v := sig[0].(float64); v < 9.9 || v > 10.1 {
		t.Fatalf("span 0 reports %v, want the whole 10s ring", v)
	}
}

func TestEnvelopeClampsBucketCount(t *testing.T) {
	r := newEnvelopeAPI(t, 90000, 90000)

	body := getEnvelope(t, r, "?buckets=99999")
	raw, _ := base64.StdEncoding.DecodeString(body["buckets"].(string))
	if len(raw) != 600 {
		t.Errorf("buckets=99999 returned %d, want the 600 cap", len(raw))
	}

	body = getEnvelope(t, r, "?buckets=0")
	raw, _ = base64.StdEncoding.DecodeString(body["buckets"].(string))
	if len(raw) != 1 {
		t.Errorf("buckets=0 returned %d, want 1", len(raw))
	}
}

func TestEnvelopeRejectsNonNumericParams(t *testing.T) {
	r := newEnvelopeAPI(t, 1000, 1000)
	for _, q := range []string{"?buckets=lots", "?spans=30,soon"} {
		req := httptest.NewRequest(http.MethodGet, "/api/envelope"+q, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", q, w.Code)
		}
	}
}

func TestEnvelopeWithoutAnEnvelopeIs503(t *testing.T) {
	a := New(&config.Config{OutputDir: t.TempDir()}, nil, nil, nil)
	r := mux.NewRouter()
	a.SetupRoutes(r)

	req := httptest.NewRequest(http.MethodGet, "/api/envelope", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}
```

Add `"encoding/base64"` to the imports in `v2-go/api/api_test.go`.

- [ ] **Step 2: Run the tests to verify they fail**

```bash
cd v2-go && go test ./api/ -run TestEnvelope -v
```

Expected: FAIL to compile — `too many arguments in call to New`.

- [ ] **Step 3: Write the implementation**

In `v2-go/api/api.go`, add the field and extend `New`:

```go
type API struct {
	cfg   *config.Config
	cap   *audio.Capture
	saver *audio.Saver
	env   *audio.Envelope
}

func New(cfg *config.Config, cap *audio.Capture, saver *audio.Saver, env *audio.Envelope) *API {
	return &API{cfg: cfg, cap: cap, saver: saver, env: env}
}
```

Add the route in `SetupRoutes`, next to `/api/peaks`:

```go
	r.HandleFunc("/api/envelope", a.handleEnvelope).Methods(http.MethodGet, http.MethodHead)
```

Add the cap, the response type and the handler after `handlePeaks`:

```go
// maxBuckets caps what a client can ask for. The ribbon wants about one bucket
// per CSS pixel and the widest target is ~1246px, so this is generous.
//
// It is also the only upper bound on the work one request makes the envelope
// do — Buckets clamps the low end but not the high end — so this is what stops
// an unbounded ?buckets= from turning into an unbounded aggregation.
const maxBuckets = 600

type envelopeResponse struct {
	RingSeconds     float64 `json:"ring_seconds"`
	BufferedSeconds float64 `json:"buffered_seconds"`
	EdgeSeconds     float64 `json:"edge_seconds"`
	// Buckets is base64 rather than a JSON array: 400 buckets is 536 chars
	// against ~1600, it is the encoding scripts/take-envelope.py already
	// writes, and Go marshals []byte this way with no conversion.
	Buckets       []byte    `json:"buckets"`
	SignalSeconds []float64 `json:"signal_seconds"`
}

// handleEnvelope serves the buffer ribbon: log-spaced buckets over the whole
// ring, plus seconds-of-signal for each capture tier the client names.
//
// The tiers come from the client so buildDurations() stays the only place that
// decides what they are.
func (a *API) handleEnvelope(w http.ResponseWriter, r *http.Request) {
	if a.env == nil {
		writeErr(w, http.StatusServiceUnavailable, "envelope not available")
		return
	}

	buckets := 400
	if v := r.URL.Query().Get("buckets"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "buckets must be an integer")
			return
		}
		buckets = n
	}
	if buckets < 1 {
		buckets = 1
	}
	if buckets > maxBuckets {
		buckets = maxBuckets
	}

	ring := a.env.RingSeconds()
	var spans []float64
	if v := r.URL.Query().Get("spans"); v != "" {
		for _, part := range strings.Split(v, ",") {
			f, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
			if err != nil || f < 0 {
				writeErr(w, http.StatusBadRequest, "spans must be non-negative numbers")
				return
			}
			spans = append(spans, f)
		}
	}

	// Non-nil so an empty spans list marshals as [] rather than null.
	sig := make([]float64, len(spans))
	for i, s := range spans {
		if s == 0 { // 0 means the whole ring, matching /api/trigger
			s = ring
		}
		sig[i] = a.env.SignalSeconds(s)
	}

	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, envelopeResponse{
		RingSeconds:     ring,
		BufferedSeconds: a.env.BufferedSeconds(),
		EdgeSeconds:     audio.EdgeSeconds,
		Buckets:         a.env.Buckets(buckets),
		SignalSeconds:   sig,
	})
}
```

In `v2-go/api/api_test.go`, update the existing helper at line 24 so the package still compiles:

```go
	a := New(&config.Config{OutputDir: dir}, nil, nil, nil)
```

In `v2-go/main.go`, update the wiring:

```go
	api.New(cfg, cap, saver, cap.Envelope()).SetupRoutes(r)
```

- [ ] **Step 4: Run the full suite**

```bash
cd v2-go && go build ./... && go test ./... && go vet ./... && gofmt -l .
```

Expected: all tests PASS (the original 34 plus the new ones), vet silent, `gofmt -l` prints nothing.

- [ ] **Step 5: Commit**

```bash
git add v2-go/api/api.go v2-go/api/api_test.go v2-go/main.go
git commit -m "Serve the buffer ribbon envelope over HTTP"
```

---

### Task 6: Share `fmtDur` from `meter.js`

**Files:**
- Modify: `v2-go/static/lib/meter.js` (end of file)
- Modify: `v2-go/static/app.js:4`, and the local `fmtDur` after `buildDurations`

Pure refactor, no behaviour change. **`Visualizer` is deliberately left alone
here** — it is deleted in Task 7b, once the ribbon exists to replace it, so that
no commit on this branch leaves the app with a dead panel. `ribbon.js` imports
`fmtDur` from `meter.js`, which is why this move comes first.

- [ ] **Step 1: Add `fmtDur` to `meter.js`**

At the end of `v2-go/static/lib/meter.js`, replace the export line with:

```js
/** Format a duration in seconds the way the capture tiers and stats read. */
export function fmtDur(s) {
  if (s >= 60) {
    const m = s / 60;
    return Number.isInteger(m) ? `${m}m` : `${m.toFixed(1)}m`;
  }
  return `${s}s`;
}

export { FLOOR_DB, dbToFrac };
```

- [ ] **Step 2: Use it from `app.js`**

Change the import at line 4 — `Visualizer` stays in this list for now:

```js
import { Visualizer, Meters, FLOOR_DB, fmtDur } from '/lib/meter.js';
```

Then delete the local `fmtDur` function, the one directly after `buildDurations`. Change nothing else.

- [ ] **Step 3: Verify the app still works exactly as before**

```bash
cd v2-go && grep -n "function fmtDur" static/app.js || echo "local copy gone"
cd v2-go && grep -n "fmtDur" static/lib/meter.js
```

Expected: the local copy is gone and `meter.js` exports it. Deploy with `./deploy.sh --static` and confirm the app looks and behaves identically — the visualiser still scrolls, the stats still read `15m`. This task is a no-op from the user's side; if anything changed, something is wrong.

- [ ] **Step 4: Commit**

```bash
git add v2-go/static/lib/meter.js v2-go/static/app.js
git commit -m "Share fmtDur from meter.js"
```

---

### Task 7: The ribbon component

**Files:**
- Create: `v2-go/static/lib/ribbon.js`

- [ ] **Step 1: Write the component**

Create `v2-go/static/lib/ribbon.js`:

```js
// The buffer ribbon: the capture ring drawn on a logarithmic age axis, with a
// marker at each capture tier and the selected tier's span shaded, so you can
// see which button catches your take before pressing it.
//
// Position comes from
//
//   left% = (1 - ln(age/A) / ln(T/A)) * 100
//
// with A = edge_seconds and T = ring_seconds, both read off the response so the
// server's bucketing and these markers cannot drift apart. Bucket i of N draws
// at x = i, so the waveform inverts nothing.
//
// It replaces the live visualiser rather than sitting beside it. Resolution at
// the right edge improves (~50 px/s against the old flat 37.6), latency gets
// worse by 1-2s. Live level lives on the meters, still on the websocket.

import { fmtDur } from '/lib/meter.js';

const SVG_NS = 'http://www.w3.org/2000/svg';
const MAX_BUCKETS = 600;
const POLL_MS = 1000;

function add(parent, tag, cls) {
  const el = document.createElement(tag);
  el.className = cls;
  parent.appendChild(el);
  return el;
}

/** Decode the base64 envelope the server sends into bytes. */
function decode(b64) {
  const s = atob(b64 || '');
  const out = new Uint8Array(s.length);
  for (let i = 0; i < s.length; i++) out[i] = s.charCodeAt(i);
  return out;
}

export class Ribbon {
  /**
   * @param {HTMLElement} wrap the existing .viz-wrap
   */
  constructor(wrap) {
    this.wrap = wrap;
    this.spans = [];      // capture tiers in seconds; 0 means the whole ring
    this.selected = null;
    this.data = null;
    this.timer = null;

    wrap.textContent = '';
    this.hatch = add(wrap, 'div', 'rb-hatch');
    this.bandLayer = add(wrap, 'div', 'rb-layer');

    this.svg = document.createElementNS(SVG_NS, 'svg');
    this.svg.setAttribute('class', 'rb-wave');
    this.svg.setAttribute('preserveAspectRatio', 'none');
    this.path = document.createElementNS(SVG_NS, 'path');
    this.svg.appendChild(this.path);
    wrap.appendChild(this.svg);

    this.markLayer = add(wrap, 'div', 'rb-layer');
    add(wrap, 'div', 'rb-scrim');
    this.labelLayer = add(wrap, 'div', 'rb-layer');
    add(wrap, 'div', 'rb-now');

    const now = add(this.labelLayer, 'span', 'rb-label rb-now-label');
    now.textContent = 'now';

    this.readout = add(wrap, 'p', 'rb-readout');
    this.readout.textContent = 'connecting';

    this._onVis = () => (document.hidden ? this.stop() : this.start());
    document.addEventListener('visibilitychange', this._onVis);
    this.start();
  }

  /** @param {number[]} spans tier lengths in seconds; 0 means the whole ring */
  setSpans(spans) {
    this.spans = spans.slice();
    if (this.selected === null || !this.spans.includes(this.selected)) {
      this.selected = this.spans[0] ?? null;
    }
    this.render();
    this.poll();
  }

  setSelected(seconds) {
    this.selected = seconds;
    this.render();
  }

  start() {
    if (this.timer) return;
    this.timer = setInterval(() => this.poll(), POLL_MS);
    this.poll();
  }

  stop() {
    clearInterval(this.timer);
    this.timer = null;
  }

  destroy() {
    this.stop();
    document.removeEventListener('visibilitychange', this._onVis);
  }

  async poll() {
    const w = Math.round(this.wrap.clientWidth) || 340;
    const n = Math.min(MAX_BUCKETS, Math.max(60, w));
    const q = `buckets=${n}&spans=${this.spans.join(',')}`;
    try {
      const res = await fetch(`/api/envelope?${q}`, { cache: 'no-store' });
      if (!res.ok) return; // keep the last ribbon drawn
      this.data = await res.json();
      this.render();
    } catch {
      // Keep the last ribbon drawn. The health dot already reports that the
      // server is unreachable, and a second indicator would add nothing.
    }
  }

  render() {
    const d = this.data;
    if (!d) return;

    const T = d.ring_seconds;
    const A = d.edge_seconds;
    const denom = Math.log(T / A);
    const leftPct = (age) => (1 - Math.log(Math.max(age, A) / A) / denom) * 100;
    const abs = (s) => (s === 0 ? T : s);

    this.drawWave(decode(d.buckets));

    // Everything older than what is buffered is time that was never recorded,
    // not silence. Drawing it flat would read as "twelve minutes of quiet".
    this.hatch.style.width = `${leftPct(d.buffered_seconds).toFixed(2)}%`;

    const tiers = this.spans.map((s) => ({ s, age: abs(s) }));
    const sel = this.selected;

    this.bandLayer.textContent = '';
    for (const t of tiers) {
      if (t.s !== sel) continue;
      const band = add(this.bandLayer, 'div', 'rb-band');
      const l = leftPct(t.age);
      band.style.left = `${l.toFixed(2)}%`;
      band.style.width = `${(100 - l).toFixed(2)}%`;
    }

    this.markLayer.textContent = '';
    for (const t of tiers) {
      const mark = add(this.markLayer, 'div', 'rb-mark' + (t.s === sel ? ' on' : ''));
      mark.style.left = `${leftPct(t.age).toFixed(2)}%`;
    }

    // Rebuild labels but keep the fixed "now" at the right edge.
    for (const old of this.labelLayer.querySelectorAll('.rb-label:not(.rb-now-label)')) {
      old.remove();
    }
    tiers.forEach((t, i) => {
      const label = document.createElement('span');
      label.className = 'rb-label' + (t.s === sel ? ' on' : '');
      label.textContent = fmtDur(t.age);
      label.style.left = `${leftPct(t.age).toFixed(2)}%`;
      // The oldest label sits at 0% and would hang off the left edge if it
      // were centred like the rest.
      label.style.transform = i === tiers.length - 1 ? 'translateX(0)' : 'translateX(-50%)';
      this.labelLayer.appendChild(label);
    });

    this.readout.textContent = this.readoutText(d, abs(sel));
  }

  drawWave(bytes) {
    const n = bytes.length;
    if (!n) return;
    this.svg.setAttribute('viewBox', `0 0 ${Math.max(n - 1, 1)} 100`);

    const top = [];
    const bot = [];
    for (let i = 0; i < n; i++) {
      const a = (bytes[i] / 255) * 47; // 47 leaves room for the label scrim
      top.push(`${i},${(50 - a).toFixed(2)}`);
      bot.push(`${i},${(50 + a).toFixed(2)}`);
    }
    this.path.setAttribute('d', `M${top.join(' L')} L${bot.reverse().join(' L')} Z`);
  }

  readoutText(d, span) {
    if (d.buffered_seconds <= 0) return 'buffer empty';

    const i = this.spans.indexOf(this.selected);
    const signal = i >= 0 ? (d.signal_seconds?.[i] ?? 0) : 0;

    // Empty wins over under-buffered when both hold: if the buffer holds 90s,
    // the span is 7m and that 90s is silent, SILENT is the more actionable of
    // the two true statements.
    if (signal < 0.5) return `last ${fmtDur(span)} — SILENT, this would save nothing`;
    if (span > d.buffered_seconds + 0.5) {
      return `last ${fmtDur(span)} — only ${fmtDur(Math.round(d.buffered_seconds))} buffered`;
    }
    return `last ${fmtDur(span)}`;
  }
}
```

- [ ] **Step 2: Verify it parses**

**Do not wire it into `app.js` yet** — that is Task 7b. This task adds one new
file and imports it from nowhere, so the app is untouched and still running the
old visualiser.

```bash
cd v2-go && node --input-type=module -e "$(cat static/lib/ribbon.js | sed "s#from '/lib/meter.js'#from './static/lib/meter.js'#")" 2>&1 | head -5 || true
```

Expected: no `SyntaxError`. A module-resolution complaint is fine; a syntax error is not. If `node` is unavailable, skip — Task 10 catches it in a real browser.

- [ ] **Step 3: Commit**

```bash
git add v2-go/static/lib/ribbon.js
git commit -m "Add the buffer ribbon component"
```

---

### Task 7b: Swap the visualiser for the ribbon

**Files:**
- Modify: `v2-go/static/app.js`
- Modify: `v2-go/static/lib/meter.js`

This is the commit where the UI actually changes. It is one commit on purpose:
the swap and the deletion have to land together, or an intermediate commit
leaves the app with a dead panel — and Task 8 deploys to a live device.

- [ ] **Step 1: Point `app.js` at the ribbon**

Change the import at line 4 to drop `Visualizer`, and add the ribbon import after it:

```js
import { Meters, FLOOR_DB, fmtDur } from '/lib/meter.js';
import { Ribbon } from '/lib/ribbon.js';
```

In the state block, replace `let viz = null;` with:

```js
let ribbon = null;
```

At the bottom of `buildDurations`, after the loop that appends buttons:

```js
  ribbon?.setSpans(opts.map((o) => o.s));
  ribbon?.setSelected(selSeconds);
```

Inside the button click handler in `buildDurations`, after the `aria-pressed` loop:

```js
      ribbon?.setSelected(selSeconds);
```

Under the `--- live ---` heading, replace the `viz = new Visualizer(el.viz, { channels: [2, 3] });` line with:

```js
ribbon = new Ribbon(el.vizWrap);
```

Delete the `viz?.setChannels(sel);` line in `applyStatus`, the `viz: $('viz'),` entry in the `el` object, and `viz.push(f);` in the `connectLive` callback.

**Order matters:** `ribbon` must be constructed before the first `pollStatus()` resolves, because `applyStatus` calls `buildDurations`. The existing `pollStatus()` call is at the bottom of the file, well after this line, so leaving it where `viz` was is correct — and the `?.` guards make a race harmless either way.

- [ ] **Step 2: Delete the `Visualizer` class**

Remove the whole `export class Visualizer { ... }` block from `v2-go/static/lib/meter.js` — it starts at the `export class Visualizer` line and ends at the closing brace before `/** Renders a row of DOM level meters and keeps them updated. */`. Also delete the now-unused `warp` helper above it and the file-header comment's three numbered points, which describe canvas bugs that no longer exist. Replace that header with:

```js
// Level meters, plus the shared dB and duration formatting helpers.
//
// The scrolling canvas visualiser that used to live here was replaced by the
// buffer ribbon (lib/ribbon.js): it could only ever show the ~9s that arrived
// while the page was open, and a dashcam is something you open after the
// moment.
```

Keep `FLOOR_DB`, `dbToFrac`, `Meters` and `fmtDur`.

- [ ] **Step 3: Verify nothing references the removed names**

```bash
cd v2-go && grep -rn "Visualizer\|viz\.\|viz =\|\$('viz')" static/ || echo "clean"
```

Expected: `clean`. `el.vizWrap` and `$('viz-wrap')` must survive — the `.stale` toggle still uses them.

- [ ] **Step 4: Commit**

```bash
git add v2-go/static/lib/meter.js v2-go/static/app.js
git commit -m "Swap the canvas visualiser for the buffer ribbon"
```

At this point the ribbon renders into the old `.viz-wrap` slot, still inside the monitor panel. Task 8 moves it out and gives it the full width.

---

### Task 8: Markup, layout and styles

**Files:**
- Modify: `v2-go/static/index.html:57-63`
- Modify: `v2-go/static/styles.css:125-146`, `:459-478`, `:509-530`

- [ ] **Step 1: Move `.viz-wrap` out of the monitor panel**

CSS cannot reparent an element, so there is one DOM for every viewport. In `v2-go/static/index.html`, change:

```html
<main>

  <div class="col col-monitor">

    <section class="panel">
      <div class="viz-wrap" id="viz-wrap">
        <canvas id="viz"></canvas>
      </div>

      <div class="meters" id="meters"></div>
```

to:

```html
<main>

  <!-- The ribbon is a direct child of main, not of a column: at the
       two-column breakpoints it spans both (grid-column: 1 / -1), which is the
       only way the compressed old end gets enough pixels to be readable.
       Its contents are built by lib/ribbon.js. -->
  <div class="viz-wrap" id="viz-wrap"></div>

  <div class="col col-monitor">

    <section class="panel">

      <div class="meters" id="meters"></div>
```

- [ ] **Step 2: Add the ribbon styles**

In `v2-go/static/styles.css`, replace the `.viz-wrap canvas` rule and extend the visualiser block. Find:

```css
.viz-wrap canvas { display: block; width: 100%; height: 100%; }
```

Replace it with:

```css
/* Ribbon internals. Every layer is inset:0 and unpositioned in the stacking
   sense, so paint order is DOM order and .stale::after -- generated content,
   and therefore the last box -- still lands on top. */
.rb-layer { position: absolute; inset: 0; pointer-events: none; }

.rb-wave { position: absolute; inset: 0; width: 100%; height: 100%; display: block; }
.rb-wave path { fill: rgba(52, 211, 153, .72); }

/* Time the buffer has not reached yet. Never drawn as a flat line: that reads
   as silence, and the two are completely different facts. */
.rb-hatch {
  position: absolute; top: 0; bottom: 0; left: 0; width: 0;
  background: repeating-linear-gradient(
    45deg, rgba(38, 50, 74, .55) 0 5px, rgba(6, 11, 22, 0) 5px 10px);
}

.rb-band { position: absolute; top: 0; bottom: 0; background: rgba(52, 211, 153, .13); }

.rb-mark { position: absolute; top: 0; bottom: 0; width: 1px; background: rgba(38, 50, 74, .9); }
.rb-mark.on { background: rgba(52, 211, 153, .55); }

.rb-scrim {
  position: absolute; top: 0; left: 0; right: 0; height: 22px;
  background: linear-gradient(to bottom, rgba(6, 11, 22, .92), rgba(6, 11, 22, 0));
}

.rb-label {
  position: absolute; top: 5px; padding: 0 4px;
  font-family: var(--mono); font-size: 10px; letter-spacing: .06em;
  color: var(--ink-faint); white-space: nowrap;
}
.rb-label.on { color: var(--accent); }
.rb-now-label { right: 7px; left: auto; color: var(--accent); }

.rb-now { position: absolute; top: 0; bottom: 0; right: 0; width: 2px; background: var(--accent); }

.rb-readout {
  position: absolute; left: 9px; bottom: 6px; margin: 0;
  font-family: var(--mono); font-size: 10px; letter-spacing: .04em;
  color: var(--ink-faint); white-space: nowrap;
}
```

- [ ] **Step 3: Keep the stale scrim on top**

Change the `.viz-wrap.stale::after` rule to add a z-index. Generated `::after` content is already the last box among the element's children, so DOM order alone puts it above the ribbon — this is explicit insurance so a future ribbon layer taking a `z-index` cannot silently regress the "no signal" scrim.

```css
.viz-wrap.stale::after {
  content: "no signal";
  position: absolute; inset: 0;
  z-index: 2;
  display: grid; place-items: center;
  font-family: var(--mono); font-size: 12px; letter-spacing: .12em;
  text-transform: uppercase; color: var(--danger);
  background: rgba(6, 11, 22, .72);
}
```

- [ ] **Step 4: Span both columns at the two-column breakpoints**

In the `@media (min-width: 860px), (orientation: landscape) and (min-width: 700px) and (max-height: 560px)` block, add the span rule after the `.col-monitor` rule:

```css
  /* Width is linear but the compression is logarithmic, so widening inside a
     column (338px -> 522px) fights a curve that needs about 10x. Spanning both
     gets 1246px, where an 8-bar phrase at the old end is 4.3px instead of
     1.8px -- the difference between visible and not. */
  .viz-wrap { grid-column: 1 / -1; }
```

- [ ] **Step 5: Hide the readout where there is no room**

In the final `@media (orientation: landscape) and (max-height: 440px)` block, add:

```css
  /* 64px of ribbon has no room for a readout under the waveform. The same
     tier already drops .last and the channel diagnostic. */
  .rb-readout { display: none; }
```

- [ ] **Step 6: Deploy the static files and look at it**

```bash
./deploy.sh --static
```

Expected: about a second, no rebuild. Open the app on the tailnet HTTPS name and confirm the ribbon draws, the markers sit at 30s/2m/7m/15m, and tapping a tier shades its span.

- [ ] **Step 7: Commit**

```bash
git add v2-go/static/index.html v2-go/static/styles.css
git commit -m "Lay out the buffer ribbon across both columns"
```

---

### Task 9: Service worker shell

**Files:**
- Modify: `v2-go/static/sw.js:9-21`

`addAll` is atomic. Miss this and precaching fails entirely and offline dies silently — this has bitten the project before, which is why it is its own task rather than a line in Task 7.

- [ ] **Step 1: Add the file and bump the cache**

```js
const CACHE = 'dashcam-shell-v3';
const SHELL = [
  '/',
  '/index.html',
  '/styles.css',
  '/app.js',
  '/lib/live.js',
  '/lib/meter.js',
  '/lib/ribbon.js',
  '/lib/takes.js',
  '/lib/wakelock.js',
  '/vendor/wavesurfer.esm.js',
  '/manifest.json',
  '/icons/icon-192.png',
];
```

- [ ] **Step 2: Verify every listed file exists**

```bash
cd v2-go/static && for f in styles.css app.js lib/live.js lib/meter.js lib/ribbon.js lib/takes.js lib/wakelock.js vendor/wavesurfer.esm.js manifest.json icons/icon-192.png; do
  [ -f "$f" ] && echo "ok   $f" || echo "MISSING $f"
done```

Expected: `ok` on every line. Any `MISSING` means `addAll` will reject the whole shell.

- [ ] **Step 3: Deploy and confirm the new worker takes over**

```bash
./deploy.sh --static
```

Then on the tailnet HTTPS name, in DevTools → Application → Service Workers, reload twice and confirm the active worker is serving `dashcam-shell-v3` and that Cache Storage lists `/lib/ribbon.js`.

- [ ] **Step 4: Commit**

```bash
git add v2-go/static/sw.js
git commit -m "Precache the ribbon module"
```

---

### Task 10: Verify the layout across viewports

**Files:**
- Create: `<scratchpad>/ribbon-check.mjs` — **throwaway, not committed.** This project has no JS harness and does not want one; a scratchpad Playwright script is the established pattern, and it is how the tablet layout was verified and how a rename race was caught.

- [ ] **Step 1: Write the script**

Create `ribbon-check.mjs` in the session scratchpad. Set `URL` to the tailnet HTTPS address — **never commit it, and never put it in a tracked file.**

```js
import { chromium } from 'playwright';

const URL = process.env.DASHCAM_URL; // https://<pi-host>.<tailnet>.ts.net/
if (!URL) throw new Error('set DASHCAM_URL');

const VIEWPORTS = [
  { name: 'phone portrait',      width: 390, height: 844 },
  { name: 'phone landscape',     width: 844, height: 390 },
  { name: 'tablet landscape',    width: 1280, height: 800 },
  { name: 'iPhone SE landscape', width: 667, height: 375 },
];

const browser = await chromium.launch();
for (const v of VIEWPORTS) {
  const page = await browser.newPage({ viewport: { width: v.width, height: v.height } });
  await page.goto(URL, { waitUntil: 'networkidle' });
  await page.waitForTimeout(1500); // let one poll land

  const m = await page.evaluate(() => {
    const wrap = document.getElementById('viz-wrap');
    const r = wrap.getBoundingClientRect();
    const main = document.querySelector('main').getBoundingClientRect();
    const readout = wrap.querySelector('.rb-readout');
    return {
      ribbonW: Math.round(r.width),
      mainW: Math.round(main.width),
      spans: Math.round(r.width) >= Math.round(main.width) - 40,
      marks: wrap.querySelectorAll('.rb-mark').length,
      labels: [...wrap.querySelectorAll('.rb-label')].map((e) => e.textContent),
      bands: wrap.querySelectorAll('.rb-band').length,
      pathLen: (wrap.querySelector('.rb-wave path')?.getAttribute('d') || '').length,
      hatchW: wrap.querySelector('.rb-hatch').style.width,
      readout: readout.textContent,
      readoutShown: getComputedStyle(readout).display !== 'none',
      parentIsMain: wrap.parentElement.tagName === 'MAIN',
      bodyScrollsX: document.body.scrollWidth > document.body.clientWidth,
    };
  });
  console.log(v.name, JSON.stringify(m, null, 2));
  await page.screenshot({ path: `ribbon-${v.name.replace(/ /g, '-')}.png` });
  await page.close();
}
await browser.close();
```

- [ ] **Step 2: Run it**

```bash
cd <scratchpad> && npm i playwright && DASHCAM_URL='https://<pi-host>.<tailnet>.ts.net/' node ribbon-check.mjs
```

- [ ] **Step 3: Check each assertion by hand**

| viewport | expect |
|---|---|
| phone portrait | `spans: true`, `parentIsMain: true`, 4 marks, labels `["now","30s","2m","7m","15m"]`, 1 band, `bodyScrollsX: false` |
| phone landscape (390 tall) | `readoutShown: false` — the ≤440px tier hides it |
| tablet landscape | `ribbonW` ≈ 1246, `spans: true` — this is the whole reason for the grid span |
| iPhone SE landscape | one column (667px is below the 700px trigger), `spans: true`, no horizontal scroll |

`pathLen` must be non-zero everywhere; a zero-length path means the fetch or the decode failed.

- [ ] **Step 4: Check the stale scrim still paints on top**

With the app open, unplug the EP-136 and wait for `/api/status` to report unhealthy. The red `no signal` scrim must cover the ribbon completely, including the readout. Plug it back in and confirm recovery within ~30s.

If it does not cover, a ribbon layer has picked up a stacking context — check for a `z-index` or a `transform` on `.rb-*`.

- [ ] **Step 5: Look at the screenshots**

Open the four PNGs. The waveform must be visibly denser toward the right, the markers must sit where the labels say, and the tablet shot must show the ribbon above both columns rather than inside the left one.

**Nothing to commit** — the script and screenshots stay in the scratchpad.

---

### Task 11: Deploy and verify on hardware

- [ ] **Step 1: Full deploy**

Go code changed, so this is the full path, not `--static`:

```bash
./deploy.sh
```

If it fails with `communication with agent failed`, the macOS ssh agent has gone stale — see the blocker in `STATE.md`. Bypass:

```bash
ssh -o "IdentityAgent none" -o IdentitiesOnly=yes -i ~/.ssh/id_rsa "$DASHCAM_HOST" true
```

- [ ] **Step 2: Confirm the tests pass on the Pi too**

```bash
ssh "$DASHCAM_HOST" 'cd ~/audio-dashcam/v2-go && go test ./...'
```

Expected: `ok` for every package.

- [ ] **Step 3: Confirm the endpoint answers**

```bash
curl -s "http://${DASHCAM_HOST#*@}/api/envelope?buckets=16&spans=30,120,420,0" | python3 -m json.tool
```

Expected: `ring_seconds: 900`, `edge_seconds: 1`, a base64 `buckets` string, and four `signal_seconds`.

- [ ] **Step 4: Watch the hatch shrink**

The service just restarted, so the ring is empty. Immediately after the deploy the ribbon should be almost entirely hatched and the readout should read `buffer empty`, then `SILENT`. Check again after a few minutes: the hatch must have visibly shrunk from the left.

**This is the one state no mockup covered**, so look at it properly rather than glancing.

- [ ] **Step 5: Confirm the readout tells the truth about a real take**

Wait until the ring holds a few minutes, play something for ~30s, then stop and wait two minutes.

- Select `30s` → the readout must read `SILENT, this would save nothing`.
- Select `7m` → it must not.
- Press Capture with `7m` selected, then check the take's waveform in the takes list: the passage must sit where the ribbon said it would.

**That last check is the whole feature.** If the ribbon and the saved take disagree about where the audio is, stop and debug before going further — the most likely cause is the envelope and the audio ring having drifted apart.

- [ ] **Step 6: Confirm audio is genuinely flowing before trusting any silence**

An all-floor reading means *either* broken meters *or* a quiet room, and this has sent a session the wrong way before:

```bash
DASHCAM_ADDR="${DASHCAM_HOST#*@}" python3 scripts/channel-probe.py --seconds 20
```

- [ ] **Step 7: Check the cost on the Pi**

```bash
ssh "$DASHCAM_HOST" 'systemctl --user status audio-dashcam | head -20'
curl -s "http://${DASHCAM_HOST#*@}/api/status" | python3 -m json.tool | grep -E "xruns|capture_healthy|ring_seconds"
```

Expected: `xruns: 0` and `capture_healthy: true`. The envelope adds 88 KiB and one array store per 10 ms bin on the callback thread; if xruns have appeared, that is the first suspect.

- [ ] **Step 8: Commit anything left and update `STATE.md`**

`STATE.md` is gitignored. Move the ribbon out of "READY TO PLAN" into "Recently shipped", record what hardware verification actually showed, and note whether the far-left region (7m–15m) has now been seen with real data — the spec flags that it never had been.

---

## Self-review

**Spec coverage.** Every section of the spec maps to a task: retention → 1, bucketing → 2 (committed together with 1), signal counting → 3, wiring → 4, the endpoint → 5, `fmtDur` sharing → 6, the component and readout → 7, the swap and `Visualizer` deletion → 7b, layout/hatch/stale → 8, the service-worker rule → 9, viewport verification → 10, hardware states → 11. The three "honest gaps" in the spec are not tasks because they are accepted limits, but Task 11 Step 8 asks for the far-left one to be re-checked once real data reaches it.

**Deviations,** all three listed at the top and amended into the spec: base64 buckets, the newest bucket reaching age 0, and the `z-index` rationale.

**Names used consistently across tasks:** `NewEnvelope(capBins, saveChannels, binMillis)`, `PushBin`, `Buckets(n)`, `SignalSeconds(span)`, `BufferedSeconds()`, `RingSeconds()`, `EdgeSeconds`, `signalByte`, `codeDB`, `Levels.SetEnvelope`, `Capture.Envelope()`, `api.New(cfg, cap, saver, env)`, `Ribbon.setSpans/setSelected/poll/render`, `fmtDur`. CSS classes are all `rb-*` except the existing `.viz-wrap`.

**Known sharp edge:** Task 1's tests call `Buckets`, which does not exist until Task 2, so the two share a single commit. This was originally written as two commits and produced one that did not compile — see "Corrections found during execution" at the top.

**Every commit leaves working software.** Go tasks (1+2, 3, 4, 5) each keep `go test ./...` green. UI tasks are ordered so the app is never mid-swap: 6 shares `fmtDur` with the visualiser still running, 7 adds an unreferenced file, 7b performs the swap and the deletion together, 8 moves it and 9 precaches it. Check this property holds for any task you re-cut.
