# MIDI Clock and Tempo Metadata Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stamp each saved take with the tempo it was played at, read from the
EP-136's free-running MIDI clock, and let the owner edit that value.

**Architecture:** A new `midi` package owns a rawmidi reader goroutine and a
ring of clock-pulse timestamps. `audio` never imports it — `Saver` takes a
one-method `TempoSource` interface, so the cgo-tainted audio package keeps no
knowledge of MIDI and both sides test with fakes. `main.go` is the only place
the two meet. A MIDI failure of any kind produces a take with no BPM; it can
never fail a save.

**Tech Stack:** Go 1.x, no new dependencies. ALSA rawmidi character devices
read directly (`/dev/snd/midiC<N>D0`) — no cgo, no subprocess, no library.
Vanilla ES modules for the UI.

---

## Read before starting

- **The spec:** `docs/superpowers/specs/2026-09-08-midi-clock-tempo-metadata-design.md`.
  It carries the hardware findings and the rejected features. This plan does
  not repeat them.
- **`scripts/midi-probe.py`**, particularly `Tally.feed` and `Tally.bpm_stats`.
  The estimator here is a deliberate port of `bpm_stats`'s rolling median, and
  the two are cross-checked against each other during hardware verification.
  The comments in that file explain three defects it was fixed for.

### Two corrections to the spec, found while reading the code

1. **"`Saver.Save` already knows the captured window" is false.** `Ring` stores
   frames and a `totalFrames` counter and carries no wall-clock anchor at all
   (`v2-go/audio/ring.go`). The window is therefore *derived*: take
   `time.Now()` immediately after `Snapshot` returns as the end, and subtract
   `gotFrames / SampleRate` for the start. The error is the capture pipeline's
   latency — `INPUT_LATENCY_MS` is 100, plus one `FRAMES_PER_BUFFER` block at
   2048 frames (43ms), plus channel hand-off — so on the order of 150–250ms.
   Against a 30-second window that is under 1%, and the tempo is a median over
   the whole window rather than a value placed at an instant, so it is
   negligible. Frame-exact alignment stays the scrubber's problem, exactly as
   the spec says.

2. **No full MIDI parser is needed, and building one would be a mistake.**
   System Realtime bytes are `>= 0xF8`; every other byte in every other message
   — including SysEx data bytes, which are `0x00`–`0x7F` — is below that. Since
   nothing in this feature consumes notes, CC, or SysEx, the reader can discard
   every byte under `0xF8` unconditionally. The spec's requirement that
   "realtime bytes arriving inside another message are counted without
   corrupting the enclosing message" is then satisfied by construction, because
   there is no enclosing message to corrupt. Task 1 still pins it with a test.

### Scope added beyond the spec, with the owner's approval (2026-09-09)

The spec stops at the sidecar, which leaves its own verification list with no
surface: there is no way to see that the clock is alive, or that the reader
recovered after an unplug, short of saving a take or reading the journal.
Tasks 7 and 9 add `midi_connected` and `midi_bpm` to `/api/status` and a fourth
`tempo` tile to the existing stats grid. This also catches the failure mode the
state file records as indistinguishable from broken firmware: **clock-send is
off by default on the EP**, and a dash in that tile is what says so.

### Standing rules this work touches

- **No new file under `static/`.** Everything lands in existing files, so
  `SHELL` in `sw.js` and its cache version are deliberately untouched. If you
  find yourself adding a file there, `addAll` is atomic and precaching fails
  entirely unless `SHELL` and `CACHE` are both updated.
- **Commit style is this repo's, not the skill's default.** Imperative
  sentence-case subject, no `feat:`/`fix:` prefix, and a body that explains why.
  Every commit ends with the trailers configured for this session.
- **Verify a new regression test actually fails against the unfixed code.**
  This project has shipped a test that could not fail. Each task below names
  the expected failure text.
- `go build`/`go test` work on the Mac (`brew install portaudio` is already
  done). Nothing in the `midi` package needs cgo or hardware — its tests use a
  fixture directory and a FIFO.

---

## File structure

**Create:**

| File | Responsibility |
|---|---|
| `v2-go/midi/clock.go` | The pulse-timestamp ring and the BPM estimator. No I/O. |
| `v2-go/midi/clock_test.go` | Estimator against synthetic input; chunked-arrival refusal. |
| `v2-go/midi/device.go` | Discovery: `/proc/asound/cards` → rawmidi device node path. No I/O beyond reading those paths. |
| `v2-go/midi/device_test.go` | Parsing against fixtures taken from a real Pi. |
| `v2-go/midi/reader.go` | The goroutine: open, read, timestamp, feed, recover. |
| `v2-go/midi/reader_test.go` | End-to-end over a FIFO — no hardware. |

**Modify:**

| File | Change |
|---|---|
| `v2-go/audio/meta.go` | `Meta.BPM *float64` |
| `v2-go/audio/save.go` | `TempoSource` interface, `Saver.SetTempoSource`, BPM written at save, `Take.BPM` |
| `v2-go/audio/save_test.go` | Save-path tests with a fake tempo source |
| `v2-go/audio/meta_test.go` | Round-trip and backward-compatibility |
| `v2-go/api/api.go` | `New` gains a `MIDISource`; status fields; `bpm` on PATCH |
| `v2-go/api/api_test.go` | `newTestAPI` signature; BPM validation tests |
| `v2-go/main.go` | Construct the clock and reader, wire both sides |
| `v2-go/static/index.html` | Fourth stat tile |
| `v2-go/static/styles.css` | 4-column stats grid; `.take-bpm` |
| `v2-go/static/app.js` | Render the live tempo |
| `v2-go/static/lib/takes.js` | BPM on each take row, inline edit |

---

## Task 1: The clock ring and the BPM estimator

**Files:**
- Create: `v2-go/midi/clock.go`
- Test: `v2-go/midi/clock_test.go`

- [ ] **Step 1: Write the failing tests**

Create `v2-go/midi/clock_test.go`:

```go
package midi

import (
	"math"
	"testing"
	"time"
)

// feedAt pushes n clock pulses spaced exactly `interval` apart, starting at
// `start`, and returns the timestamp one past the last pulse.
//
// Intervals are chosen so the pulse spacing is a whole number of nanoseconds:
// interval = 2.5e9 / bpm nanoseconds, so 125 BPM is exactly 20ms and 100 BPM
// is exactly 25ms. A tempo like 120 would be 20833333.33ns and the rounding
// would show up in the second decimal place the spec asks us to pin.
func feedAt(c *Clock, start time.Time, n int, interval time.Duration) time.Time {
	t := start
	for i := 0; i < n; i++ {
		c.Feed(t, ClockByte)
		t = t.Add(interval)
	}
	return t
}

func bpmOf(bpm float64) time.Duration {
	return time.Duration(2.5e9 / bpm)
}

func TestBPMExactAtSteadyTempo(t *testing.T) {
	c := NewClock(10000)
	start := time.Now()
	end := feedAt(c, start, 200, bpmOf(125))

	got, ok := c.BPM(start.Add(-time.Second), end)
	if !ok {
		t.Fatal("BPM reported no reading, want a reading")
	}
	if math.Abs(got-125.00) > 0.005 {
		t.Errorf("BPM = %.4f, want 125.00", got)
	}
}

func TestBPMExactAtADifferentTempo(t *testing.T) {
	c := NewClock(10000)
	start := time.Now()
	end := feedAt(c, start, 200, bpmOf(100))

	got, ok := c.BPM(start.Add(-time.Second), end)
	if !ok {
		t.Fatal("BPM reported no reading, want a reading")
	}
	if math.Abs(got-100.00) > 0.005 {
		t.Errorf("BPM = %.4f, want 100.00", got)
	}
}

// The median is the whole reason this is not a mean. A short excursion to a
// much faster tempo drags the overall count/span figure to about 131 BPM while
// the median stays on the tempo actually played.
func TestBPMMedianResistsAShortExcursion(t *testing.T) {
	c := NewClock(10000)
	start := time.Now()
	mid := feedAt(c, start, 200, bpmOf(125))
	end := feedAt(c, mid, 30, bpmOf(200))

	got, ok := c.BPM(start.Add(-time.Second), end)
	if !ok {
		t.Fatal("BPM reported no reading, want a reading")
	}
	if math.Abs(got-125.00) > 0.005 {
		t.Errorf("BPM = %.4f, want 125.00 (a mean would read about 131)", got)
	}
}

// Fewer than two quarter notes is not a tempo. 47 pulses is one short.
func TestBPMRefusesFewerThanTwoQuarterNotes(t *testing.T) {
	c := NewClock(10000)
	start := time.Now()
	end := feedAt(c, start, 47, bpmOf(125))

	if got, ok := c.BPM(start.Add(-time.Second), end); ok {
		t.Errorf("BPM = %.2f, ok = true; want no reading from 47 pulses", got)
	}
}

// Chunked arrival, and the reason the guard is a distinct-timestamp count
// rather than a count of collapsed windows.
//
// Eight 500ms batches of 31 pulses is 248 pulses carrying 8 distinct
// timestamps. Only 56 of the 224 rolling windows collapse to zero elapsed
// time; the other 168 straddle exactly one batch boundary, all read
// 60 / 0.5 = 120 BPM, and their median is a rock-steady, entirely fictional
// 120. So "collapsed windows outnumber usable ones" does not fire here, and
// the number that survives it is worse than no number at all.
//
// What actually distinguishes this from a real reading is that the pulses
// never got individual timestamps. Verified by hand before this test was
// written; see the plan's self-review.
func TestBPMRefusesWhenTimestampsAreTooCoarse(t *testing.T) {
	c := NewClock(10000)
	start := time.Now()
	batch := start
	for b := 0; b < 8; b++ {
		for i := 0; i < 31; i++ {
			c.Feed(batch, ClockByte) // whole batch, one timestamp
		}
		batch = batch.Add(500 * time.Millisecond)
	}

	if got, ok := c.BPM(start.Add(-time.Second), batch); ok {
		t.Errorf("BPM = %.2f, ok = true; want no reading from batched timestamps", got)
	}
}

// Realtime bytes are single bytes that may arrive inside another message.
// Nothing below 0xF8 is a pulse, and interleaving a note-on's bytes between
// two clocks must not change the count or the tempo.
func TestNonRealtimeBytesAreIgnored(t *testing.T) {
	c := NewClock(10000)
	start := time.Now()
	t0 := start
	for i := 0; i < 200; i++ {
		c.Feed(t0, ClockByte)
		// A note-on straddling the gap, plus a SysEx fragment.
		c.Feed(t0, 0x90)
		c.Feed(t0, 0x3C)
		c.Feed(t0, 0x7F)
		c.Feed(t0, 0xF0)
		c.Feed(t0, 0x7E)
		c.Feed(t0, 0xF7)
		t0 = t0.Add(bpmOf(125))
	}

	if n := c.Pulses(); n != 200 {
		t.Errorf("Pulses = %d, want 200", n)
	}
	got, ok := c.BPM(start.Add(-time.Second), t0)
	if !ok || math.Abs(got-125.00) > 0.005 {
		t.Errorf("BPM = %.4f, ok = %v; want 125.00, true", got, ok)
	}
}

func TestTransportBytesAreCountedNotPulses(t *testing.T) {
	c := NewClock(10000)
	now := time.Now()
	c.Feed(now, StartByte)
	c.Feed(now, StopByte)
	c.Feed(now, StopByte)
	c.Feed(now, ClockByte)

	if n := c.Pulses(); n != 1 {
		t.Errorf("Pulses = %d, want 1", n)
	}
	starts, conts, stops := c.Transport()
	if starts != 1 || conts != 0 || stops != 2 {
		t.Errorf("Transport = (%d, %d, %d), want (1, 0, 2)", starts, conts, stops)
	}
}

// The ring must drop the oldest pulses rather than grow, and a query that
// reaches past what it still holds must still answer from what is there.
func TestClockRingWrapsAndKeepsTheNewest(t *testing.T) {
	c := NewClock(100)
	start := time.Now()
	end := feedAt(c, start, 250, bpmOf(125))

	if n := c.Pulses(); n != 250 {
		t.Errorf("Pulses = %d, want 250 (a lifetime count, not a ring occupancy)", n)
	}
	got, ok := c.BPM(start.Add(-time.Second), end)
	if !ok {
		t.Fatal("BPM reported no reading after wrapping, want a reading")
	}
	if math.Abs(got-125.00) > 0.005 {
		t.Errorf("BPM = %.4f, want 125.00", got)
	}
}

// A window with no pulses in it is the normal state when the EP is unplugged.
func TestBPMOutsideTheWindowIsNoReading(t *testing.T) {
	c := NewClock(10000)
	start := time.Now()
	end := feedAt(c, start, 200, bpmOf(125))

	if _, ok := c.BPM(end.Add(time.Hour), end.Add(2*time.Hour)); ok {
		t.Error("ok = true for a window with no pulses, want false")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd v2-go && go test ./midi/ -v`
Expected: FAIL — `no required module provides package .../v2-go/midi` or, once
the directory exists, `undefined: NewClock`.

- [ ] **Step 3: Write the implementation**

Create `v2-go/midi/clock.go`:

```go
// Package midi reads the MIDI clock the EP-136 free-runs over USB and answers
// one question: what tempo was playing over a given wall-clock interval.
//
// It deliberately knows nothing about audio. The audio package takes a
// one-method interface instead of importing this one, so a MIDI failure has no
// path into the capture thread and neither package needs the other to test.
package midi

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// MIDI System Realtime status bytes. Every byte at or above ClockByte is a
// single-byte realtime message that may arrive *inside* another message, which
// is why they are tested before anything else and why every byte below them is
// discarded unread — see the plan's note on why no parser is needed.
const (
	ClockByte    byte = 0xF8
	StartByte    byte = 0xFA
	ContinueByte byte = 0xFB
	StopByte     byte = 0xFC
)

// PulsesPerQuarter is fixed by the MIDI specification. It is not configurable
// and no device varies it.
const PulsesPerQuarter = 24

// minPulses is two quarter notes. Below this there is no tempo to report:
// the spec's rule is that a short window yields no BPM, absent rather than
// zero. At exactly this count the estimator gets 24 rolling windows.
const minPulses = 2 * PulsesPerQuarter

// MaxPulsesPerSecond sizes the ring: 400 BPM (the top of the editable range)
// at 24 pulses per quarter is 160 pulses a second. Sizing by count rather than
// by age means a slow tempo simply retains more history than asked for, which
// is harmless because every query is bounded by its interval.
const MaxPulsesPerSecond = 160

// Clock is a ring of clock-pulse arrival times.
//
// Timestamps are wall-clock nanoseconds, because the only consumer correlates
// them against a time.Now() taken at save time. An NTP step would corrupt
// readings that straddle it; on a Pi that has been up for minutes this is not
// worth defending against, and a wrong BPM is editable.
type Clock struct {
	mu       sync.Mutex
	buf      []int64
	writePos int

	pulses    atomic.Uint64
	starts    atomic.Uint64
	continues atomic.Uint64
	stops     atomic.Uint64
}

// NewClock sizes the ring in pulses. A capacity below 1 is raised to 1 so the
// zero case cannot panic in the modulo below.
func NewClock(capPulses int) *Clock {
	if capPulses < 1 {
		capPulses = 1
	}
	return &Clock{buf: make([]int64, capPulses)}
}

// CapacityFor returns a ring size covering ringSeconds at the fastest tempo
// the field accepts. Callers derive it from the audio ring's length so the two
// windows cannot drift apart.
func CapacityFor(ringSeconds int) int {
	if ringSeconds < 1 {
		ringSeconds = 1
	}
	return ringSeconds * MaxPulsesPerSecond
}

// Feed offers one received byte, stamped with the time it was read.
//
// Anything below ClockByte is discarded without inspection: no consumer here
// wants notes, CC or SysEx, and discarding them unconditionally is what makes
// a realtime byte arriving mid-message harmless rather than corrupting.
func (c *Clock) Feed(ts time.Time, b byte) {
	switch {
	case b == ClockByte:
	case b == StartByte:
		c.starts.Add(1)
		return
	case b == ContinueByte:
		c.continues.Add(1)
		return
	case b == StopByte:
		c.stops.Add(1)
		return
	default:
		return
	}

	c.pulses.Add(1)
	n := ts.UnixNano()

	c.mu.Lock()
	c.buf[c.writePos] = n
	c.writePos = (c.writePos + 1) % len(c.buf)
	c.mu.Unlock()
}

// Pulses is the lifetime clock count, not the ring occupancy. It is the
// cheapest "is the clock arriving at all" signal there is.
func (c *Clock) Pulses() uint64 { return c.pulses.Load() }

// Transport reports Start, Continue and Stop counts. Nothing consumes these
// yet. They exist because whether the EP sends transport at all was never
// validly tested — the probe drained its pipe between phases and the device
// was operated before each phase began — and a counter that ticks during
// ordinary use settles the question for free.
func (c *Clock) Transport() (starts, continues, stops uint64) {
	return c.starts.Load(), c.continues.Load(), c.stops.Load()
}

// BPM reports the tempo over [start, end] as the median of rolling
// quarter-note windows, or false when there is no defensible reading.
//
// Median, not mean: during free play the overall count-over-span figure sat
// above the median, the signature of a tempo that climbed mid-window, and a
// mean inherits that skew.
func (c *Clock) BPM(start, end time.Time) (float64, bool) {
	ts := c.between(start.UnixNano(), end.UnixNano())
	if len(ts) < minPulses {
		return 0, false
	}

	// Refuse when the pulses did not get individual arrival times. Several
	// pulses sharing one timestamp means bytes were batched before being
	// stamped, and then the windows that survive measure the batch boundaries
	// rather than the device: eight 500ms batches of 31 pulses produces a
	// rock-steady, entirely fictional 120 BPM from its 168 non-collapsed
	// windows. Counting collapsed windows does not catch that — 56 against
	// 168 — so the guard measures the property that actually matters.
	distinct := 1
	for i := 1; i < len(ts); i++ {
		if ts[i] != ts[i-1] {
			distinct++
		}
	}
	if distinct*2 < len(ts) {
		return 0, false
	}

	bpms := make([]float64, 0, len(ts))
	for i := PulsesPerQuarter; i < len(ts); i++ {
		dt := ts[i] - ts[i-PulsesPerQuarter]
		if dt <= 0 {
			// Two pulses in one read. Skipped rather than counted: 60/0 is
			// not a tempo, and the guard above has already decided whether
			// this is happening often enough to matter.
			continue
		}
		bpms = append(bpms, 60*float64(time.Second)/float64(dt))
	}
	if len(bpms) == 0 {
		return 0, false
	}

	sort.Float64s(bpms)
	// Upper-middle element for an even count, matching
	// scripts/midi-probe.py's `bpms[len(bpms) // 2]`, so a hardware
	// cross-check compares like with like.
	return bpms[len(bpms)/2], true
}

// between copies out the timestamps in [startNs, endNs] in ascending order.
//
// It walks backwards from the newest and stops at the first pulse older than
// the window, so an 8-second status query touches a few hundred entries rather
// than the whole ring, while a full-ring save query still gets everything.
func (c *Clock) between(startNs, endNs int64) []int64 {
	c.mu.Lock()
	defer c.mu.Unlock()

	n := len(c.buf)
	out := make([]int64, 0, 512)
	for k := 0; k < n; k++ {
		ts := c.buf[((c.writePos-1-k)%n+n)%n]
		if ts == 0 { // never written
			break
		}
		if ts > endNs {
			continue
		}
		if ts < startNs {
			break
		}
		out = append(out, ts)
	}

	for l, r := 0, len(out)-1; l < r; l, r = l+1, r-1 {
		out[l], out[r] = out[r], out[l]
	}
	return out
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd v2-go && go test ./midi/ -v -race`
Expected: PASS, 9 tests.

- [ ] **Step 5: Commit**

```bash
cd /Users/gabeduke/projects/audio-dashcam
git add v2-go/midi/clock.go v2-go/midi/clock_test.go
git commit -m "$(cat <<'EOF'
Add the MIDI clock ring and its tempo estimator

A ring of clock-pulse arrival times answering one question: what tempo
was playing over a wall-clock interval. The estimator is the median of
rolling quarter-note windows, a port of bpm_stats in
scripts/midi-probe.py, so a hardware cross-check compares like with
like -- including taking the upper-middle element on an even count.

Median rather than mean because a free-play window measured on hardware
sat above its own median, the signature of a tempo climbing mid-window,
and a mean inherits that skew.

Every byte below 0xF8 is discarded without inspection. No consumer here
wants notes, CC or SysEx, and SysEx data bytes are 0x00-0x7F, so a
realtime byte arriving inside another message is harmless by
construction rather than by careful parsing.

The estimator refuses two ways rather than returning a number it cannot
defend: under two quarter notes there is no tempo, and pulses that did
not get individual arrival times are not a measurement of the device.

The second guard counts distinct timestamps rather than collapsed
windows, because collapsed windows do not catch it. Eight 500ms batches
of 31 pulses collapse only 56 of 224 windows; the other 168 each
straddle exactly one batch boundary, all read 60/0.5, and their median
is a rock-steady, entirely fictional 120 BPM. Checked by hand against
the real numbers before the guard was chosen.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01UHd1yoZ1gjE864aXud9Lqe
EOF
)"
```

---

## Task 2: Rawmidi device discovery

**Files:**
- Create: `v2-go/midi/device.go`
- Test: `v2-go/midi/device_test.go`

The fixture below is the **real** output of `cat /proc/asound/cards` on the Pi,
captured 2026-09-09 with the interface unplugged, plus a synthesised EP-136
entry in the same shape. Note the two-line-per-card layout: the header line
carries the card number, a 15-character padded id in brackets, the driver and
the short name; the indented continuation line carries the long name.

**The match must be tested against the whole entry, not the bracketed id.**
`DEVICE_MATCH` is `EP-136`, and the bracketed id is truncated and sanitised by
ALSA — the string that reliably contains `EP-136` is the short or long name.

- [ ] **Step 1: Write the failing tests**

Create `v2-go/midi/device_test.go`:

```go
package midi

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// realCards is `cat /proc/asound/cards` from the Pi on 2026-09-09 with the
// interface unplugged, with an EP-136 entry added in the same shape.
const realCards = ` 0 [vc4hdmi0       ]: vc4-hdmi - vc4-hdmi-0
                      vc4-hdmi-0
 1 [vc4hdmi1       ]: vc4-hdmi - vc4-hdmi-1
                      vc4-hdmi-1
 2 [Sidekick       ]: USB-Audio - EP-136 K.O. Sidekick
                      Teenage Engineering EP-136 K.O. Sidekick at usb-xhci-hcd.1-1, high speed
`

const cardsNoEP = ` 0 [vc4hdmi0       ]: vc4-hdmi - vc4-hdmi-0
                      vc4-hdmi-0
 1 [vc4hdmi1       ]: vc4-hdmi - vc4-hdmi-1
                      vc4-hdmi-1
`

// fixture writes a cards file and a snd directory containing the named nodes.
func fixture(t *testing.T, cards string, nodes ...string) (cardsPath, sndDir string) {
	t.Helper()
	root := t.TempDir()
	cardsPath = filepath.Join(root, "cards")
	if err := os.WriteFile(cardsPath, []byte(cards), 0o644); err != nil {
		t.Fatal(err)
	}
	sndDir = filepath.Join(root, "snd")
	if err := os.MkdirAll(sndDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		if err := os.WriteFile(filepath.Join(sndDir, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return cardsPath, sndDir
}

// The bracketed id reads "Sidekick" and contains no "EP-136" at all, so
// matching only the id would miss the device this project is built around.
func TestFindMatchesTheLongNameNotTheBracketedID(t *testing.T) {
	cards, snd := fixture(t, realCards, "midiC2D0", "controlC2")

	got, err := Find(cards, snd, "EP-136")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	want := filepath.Join(snd, "midiC2D0")
	if got != want {
		t.Errorf("Find = %q, want %q", got, want)
	}
}

func TestFindMatchesTheBracketedIDToo(t *testing.T) {
	cards, snd := fixture(t, realCards, "midiC2D0")

	got, err := Find(cards, snd, "Sidekick")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got != filepath.Join(snd, "midiC2D0") {
		t.Errorf("Find = %q, want the card 2 node", got)
	}
}

func TestFindIsCaseInsensitive(t *testing.T) {
	cards, snd := fixture(t, realCards, "midiC2D0")

	if _, err := Find(cards, snd, "ep-136"); err != nil {
		t.Errorf("Find with lowercase match: %v", err)
	}
}

// The EP is frequently unplugged. That is a normal state, and it must be
// distinguishable from a real failure so the reader can stay quiet about it.
func TestFindReportsNoDeviceWhenTheCardIsAbsent(t *testing.T) {
	cards, snd := fixture(t, cardsNoEP)

	_, err := Find(cards, snd, "EP-136")
	if !errors.Is(err, ErrNoDevice) {
		t.Errorf("err = %v, want ErrNoDevice", err)
	}
}

// A card can exist and expose no MIDI at all — an HDMI output, for one.
func TestFindReportsNoDeviceWhenTheCardHasNoMIDINode(t *testing.T) {
	cards, snd := fixture(t, realCards, "controlC2", "pcmC2D0c")

	_, err := Find(cards, snd, "EP-136")
	if !errors.Is(err, ErrNoDevice) {
		t.Errorf("err = %v, want ErrNoDevice", err)
	}
}

// The EP is hw:2,0,0, so D0 is the right sub-device. Take the lowest rather
// than hardcoding it, so a device that numbers differently still works.
func TestFindTakesTheLowestSubDevice(t *testing.T) {
	cards, snd := fixture(t, realCards, "midiC2D1", "midiC2D0", "midiC2D2")

	got, err := Find(cards, snd, "EP-136")
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	if got != filepath.Join(snd, "midiC2D0") {
		t.Errorf("Find = %q, want midiC2D0", got)
	}
}

// Another card's node must never be mistaken for this one's. midiC1D0 is a
// prefix collision hazard for a naive match on "midiC1".
func TestFindDoesNotPickAnotherCardsNode(t *testing.T) {
	cards, snd := fixture(t, realCards, "midiC1D0", "midiC12D0")

	_, err := Find(cards, snd, "EP-136")
	if !errors.Is(err, ErrNoDevice) {
		t.Errorf("err = %v, want ErrNoDevice (card 2 has no node)", err)
	}
}

// On a Mac there is no /proc/asound at all. That is the same normal state.
func TestFindReportsNoDeviceWhenThereIsNoALSA(t *testing.T) {
	_, err := Find("/nonexistent/proc/asound/cards", "/nonexistent/dev/snd", "EP-136")
	if !errors.Is(err, ErrNoDevice) {
		t.Errorf("err = %v, want ErrNoDevice", err)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd v2-go && go test ./midi/ -run TestFind -v`
Expected: FAIL with `undefined: Find` and `undefined: ErrNoDevice`.

- [ ] **Step 3: Write the implementation**

Create `v2-go/midi/device.go`:

```go
package midi

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// DefaultCardsPath and DefaultSndDir are the real ALSA locations. They are
// parameters rather than constants everywhere below so the tests can run
// against a fixture directory, on a Mac, with no hardware.
const (
	DefaultCardsPath = "/proc/asound/cards"
	DefaultSndDir    = "/dev/snd"
)

// ErrNoDevice reports that no card matched, or that the matching card exposes
// no rawmidi node. This is a normal state, not a failure: the interface is
// frequently unplugged, and a Mac has no /proc/asound at all. Callers must not
// treat it as an error worth shouting about.
var ErrNoDevice = errors.New("no matching MIDI device")

// cardHeader matches the first line of a card entry in /proc/asound/cards:
//
//	 2 [Sidekick       ]: USB-Audio - EP-136 K.O. Sidekick
//
// Continuation lines are indented and carry the long name, which is where the
// full manufacturer and model text lives.
var cardHeader = regexp.MustCompile(`^\s*(\d+)\s+\[`)

// Find returns the path of the rawmidi character device belonging to the first
// card whose /proc/asound/cards entry contains match, case-insensitively.
//
// The whole entry is searched — both lines — rather than the bracketed id
// alone. ALSA truncates that id to 15 characters and sanitises it, so the
// EP-136 appears there as "Sidekick" with no model number in it; the string
// DEVICE_MATCH is set to only appears in the short and long names.
func Find(cardsPath, sndDir, match string) (string, error) {
	b, err := os.ReadFile(cardsPath)
	if err != nil {
		return "", fmt.Errorf("%w: %v", ErrNoDevice, err)
	}

	needle := strings.ToLower(match)
	card := -1
	var entry strings.Builder
	found := -1

	flush := func() {
		if card >= 0 && found < 0 && strings.Contains(strings.ToLower(entry.String()), needle) {
			found = card
		}
	}

	for _, line := range strings.Split(string(b), "\n") {
		if m := cardHeader.FindStringSubmatch(line); m != nil {
			flush()
			card, _ = strconv.Atoi(m[1])
			entry.Reset()
		}
		entry.WriteString(line)
		entry.WriteString("\n")
	}
	flush()

	if found < 0 {
		return "", fmt.Errorf("%w: no card matching %q", ErrNoDevice, match)
	}
	return rawMIDINode(sndDir, found)
}

// rawMIDINode picks the lowest-numbered rawmidi sub-device on a card. The
// EP-136 is hw:2,0,0, so D0 is what this resolves to in practice; taking the
// lowest rather than hardcoding D0 costs nothing and survives a device that
// numbers differently.
func rawMIDINode(sndDir string, card int) (string, error) {
	// The trailing D in the pattern is what stops card 1 matching card 12:
	// "midiC1D*" cannot match "midiC12D0".
	glob := filepath.Join(sndDir, fmt.Sprintf("midiC%dD*", card))
	nodes, err := filepath.Glob(glob)
	if err != nil || len(nodes) == 0 {
		return "", fmt.Errorf("%w: card %d exposes no rawmidi node", ErrNoDevice, card)
	}
	sort.Strings(nodes)
	return nodes[0], nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd v2-go && go test ./midi/ -v`
Expected: PASS, 17 tests.

- [ ] **Step 5: Commit**

```bash
cd /Users/gabeduke/projects/audio-dashcam
git add v2-go/midi/device.go v2-go/midi/device_test.go
git commit -m "$(cat <<'EOF'
Find the EP's rawmidi node by scanning /proc/asound/cards

Discovery searches the whole card entry -- header line and the indented
long-name line -- case-insensitively, not the bracketed card id. ALSA
truncates that id to 15 characters and sanitises it, so the EP-136
appears there as "Sidekick" with no model number in it, and matching the
id alone would miss the one device this project is built around.
Confirmed against the real file from the Pi, in the test fixture.

The sub-device glob keeps its trailing D ("midiC2D*") so card 1 cannot
match card 12's nodes.

A missing card, a card with no rawmidi node, and a machine with no
/proc/asound at all all report ErrNoDevice. All three are normal states
rather than failures: the interface is frequently unplugged, and the
tests run on a Mac.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01UHd1yoZ1gjE864aXud9Lqe
EOF
)"
```

---

## Task 3: The reader goroutine

**Files:**
- Create: `v2-go/midi/reader.go`
- Test: `v2-go/midi/reader_test.go`

The test opens a **FIFO** named `midiC2D0` inside a fixture snd directory. A
FIFO behaves enough like a rawmidi character device for this purpose — a
blocking read that returns as bytes are written — so discovery, opening,
timestamping and recovery are all exercised with no hardware and on a Mac.

**Note on `Stop`:** it closes the stop channel and the open file, and
deliberately does **not** wait for the read goroutine. A blocking read on a
character device cannot be interrupted portably, and the only caller is
process shutdown, where a goroutine parked in a read costs nothing. Waiting
would risk hanging shutdown for the sake of tidiness.

- [ ] **Step 1: Write the failing tests**

Create `v2-go/midi/reader_test.go`:

```go
package midi

import (
	"math"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// fifoFixture builds a cards file plus an snd dir whose midiC2D0 is a FIFO.
func fifoFixture(t *testing.T) (cardsPath, sndDir, fifo string) {
	t.Helper()
	root := t.TempDir()
	cardsPath = filepath.Join(root, "cards")
	if err := os.WriteFile(cardsPath, []byte(realCards), 0o644); err != nil {
		t.Fatal(err)
	}
	sndDir = filepath.Join(root, "snd")
	if err := os.MkdirAll(sndDir, 0o755); err != nil {
		t.Fatal(err)
	}
	fifo = filepath.Join(sndDir, "midiC2D0")
	if err := syscall.Mkfifo(fifo, 0o666); err != nil {
		t.Fatalf("mkfifo: %v", err)
	}
	return cardsPath, sndDir, fifo
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestReaderFeedsPulsesFromTheDevice(t *testing.T) {
	cards, snd, fifo := fifoFixture(t)
	clock := NewClock(10000)

	r := NewReader("EP-136", clock)
	r.CardsPath, r.SndDir = cards, snd
	r.Start()
	defer r.Stop()

	// Opening the write end unblocks the reader's open.
	w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open write end: %v", err)
	}
	defer w.Close()

	waitFor(t, "the reader to connect", r.Connected)

	// 200 pulses at 125 BPM, written one at a time so each is timestamped as
	// it lands — the same shape a rawmidi device delivers.
	for i := 0; i < 200; i++ {
		if _, err := w.Write([]byte{ClockByte}); err != nil {
			t.Fatalf("write pulse %d: %v", i, err)
		}
		time.Sleep(2 * time.Millisecond)
	}

	waitFor(t, "200 pulses", func() bool { return clock.Pulses() >= 200 })
}

func TestReaderTimestampsFinelyEnoughToEstimateTempo(t *testing.T) {
	cards, snd, fifo := fifoFixture(t)
	clock := NewClock(10000)

	r := NewReader("EP-136", clock)
	r.CardsPath, r.SndDir = cards, snd
	r.Start()
	defer r.Stop()

	w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open write end: %v", err)
	}
	defer w.Close()
	waitFor(t, "the reader to connect", r.Connected)

	// 5ms apart is 500 BPM's worth of pulses, far faster than the EP, and it
	// keeps the test under a second. The point is only that the reader's
	// timestamps are per-arrival rather than per-batch: if they were batched,
	// BPM would refuse on its distinct-timestamp guard.
	start := time.Now()
	for i := 0; i < 120; i++ {
		w.Write([]byte{ClockByte})
		time.Sleep(5 * time.Millisecond)
	}
	waitFor(t, "120 pulses", func() bool { return clock.Pulses() >= 120 })

	got, ok := clock.BPM(start.Add(-time.Second), time.Now())
	if !ok {
		t.Fatal("BPM refused; the reader is batching its timestamps")
	}
	// 5ms per pulse is 60 / (24 * 0.005) = 500 BPM. Generous tolerance: this
	// asserts "not batched", not scheduler precision.
	if math.Abs(got-500) > 150 {
		t.Errorf("BPM = %.1f, want roughly 500", got)
	}
}

// Non-realtime traffic must pass through the reader without becoming pulses.
func TestReaderIgnoresNonRealtimeTraffic(t *testing.T) {
	cards, snd, fifo := fifoFixture(t)
	clock := NewClock(10000)

	r := NewReader("EP-136", clock)
	r.CardsPath, r.SndDir = cards, snd
	r.Start()
	defer r.Stop()

	w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open write end: %v", err)
	}
	defer w.Close()
	waitFor(t, "the reader to connect", r.Connected)

	// A note-on with a clock byte landing between its status and data bytes.
	w.Write([]byte{0x90, ClockByte, 0x3C, 0x7F, 0xF0, 0x7E, 0x00, 0xF7})
	waitFor(t, "one pulse", func() bool { return clock.Pulses() == 1 })

	time.Sleep(50 * time.Millisecond)
	if n := clock.Pulses(); n != 1 {
		t.Errorf("Pulses = %d, want exactly 1", n)
	}
}

// The device disappearing is the unplug case. The reader must notice, report
// itself disconnected, and keep retrying rather than exiting.
func TestReaderReportsDisconnectedWhenTheDeviceGoesAway(t *testing.T) {
	cards, snd, fifo := fifoFixture(t)
	clock := NewClock(10000)

	r := NewReader("EP-136", clock)
	r.CardsPath, r.SndDir = cards, snd
	r.RetryDelay = 20 * time.Millisecond
	r.Start()
	defer r.Stop()

	w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open write end: %v", err)
	}
	waitFor(t, "the reader to connect", r.Connected)

	// Closing the only writer gives the reader EOF, which is what an unplugged
	// interface looks like from the read side.
	w.Close()
	if err := os.Remove(fifo); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the reader to report disconnected", func() bool { return !r.Connected() })
}

// A reader that never finds its device is the normal state on a Mac and
// whenever the EP is unplugged. It must not spin, panic, or stop trying.
func TestReaderSurvivesNoDeviceAtAll(t *testing.T) {
	clock := NewClock(10000)
	r := NewReader("EP-136", clock)
	r.CardsPath, r.SndDir = "/nonexistent/cards", "/nonexistent/snd"
	r.RetryDelay = 10 * time.Millisecond
	r.Start()
	defer r.Stop()

	time.Sleep(120 * time.Millisecond)
	if r.Connected() {
		t.Error("Connected = true with no device, want false")
	}
	if n := clock.Pulses(); n != 0 {
		t.Errorf("Pulses = %d, want 0", n)
	}
}

func TestReaderStopIsIdempotent(t *testing.T) {
	clock := NewClock(10)
	r := NewReader("EP-136", clock)
	r.CardsPath, r.SndDir = "/nonexistent/cards", "/nonexistent/snd"
	r.RetryDelay = 10 * time.Millisecond
	r.Start()
	r.Stop()
	r.Stop() // must not panic on a second close
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd v2-go && go test ./midi/ -run TestReader -v`
Expected: FAIL with `undefined: NewReader`.

- [ ] **Step 3: Write the implementation**

Create `v2-go/midi/reader.go`:

```go
package midi

import (
	"errors"
	"io"
	"log"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// readBuf is one read's worth of bytes. The EP emits about 62 a second, so
// this is never close to full; it exists so a burst after a stall is taken in
// one syscall rather than 64.
const readBuf = 64

// Reader owns the rawmidi device: discovery, opening, reading, timestamping,
// and recovery when the interface disappears.
//
// It mirrors Capture.supervise's shape deliberately — detect, back off,
// rediscover, reopen — because the failure it handles is the same one: the
// interface being unplugged. What it does not share is any path back into the
// capture thread. Every error here is logged and dropped. The dashcam's job is
// audio; a MIDI failure produces a take with no BPM and nothing else.
type Reader struct {
	// CardsPath and SndDir default to the real ALSA locations and are fields
	// so tests can point them at a fixture directory.
	CardsPath string
	SndDir    string
	// RetryDelay is the pause between attempts to find and open the device.
	// The default is deliberately unhurried: a missing EP is the normal state,
	// not an outage to race back from.
	RetryDelay time.Duration

	match string
	clock *Clock

	connected atomic.Bool
	device    atomic.Value // string

	mu   sync.Mutex
	file *os.File

	stop     chan struct{}
	stopOnce sync.Once

	// quiet suppresses repeated "no device" logs. The EP is absent for days at
	// a time and a 4-second retry would otherwise write 20,000 identical lines
	// into the journal every day.
	quiet atomic.Bool
}

func NewReader(match string, clock *Clock) *Reader {
	r := &Reader{
		CardsPath:  DefaultCardsPath,
		SndDir:     DefaultSndDir,
		RetryDelay: 4 * time.Second,
		match:      match,
		clock:      clock,
		stop:       make(chan struct{}),
	}
	r.device.Store("")
	return r
}

// Connected reports whether the device is open right now.
func (r *Reader) Connected() bool { return r.connected.Load() }

// Device is the path currently open, or "" when there is none.
func (r *Reader) Device() string { s, _ := r.device.Load().(string); return s }

// BPM forwards to the clock so a caller can hold only the Reader.
func (r *Reader) BPM(start, end time.Time) (float64, bool) { return r.clock.BPM(start, end) }

// Start launches the read loop. It returns immediately; use Connected to
// observe state.
func (r *Reader) Start() { go r.run() }

// Stop ends the loop and closes the device.
//
// It does not wait for the read goroutine. A blocking read on a character
// device cannot be interrupted portably, and the only caller is process
// shutdown, where a goroutine parked in a read costs nothing — whereas waiting
// for it could hang shutdown indefinitely.
func (r *Reader) Stop() {
	r.stopOnce.Do(func() { close(r.stop) })
	r.closeDevice()
}

func (r *Reader) run() {
	for {
		select {
		case <-r.stop:
			return
		default:
		}

		path, err := Find(r.CardsPath, r.SndDir, r.match)
		if err != nil {
			if !r.quiet.Swap(true) {
				log.Printf("[*] midi: no device matching %q — clock unavailable, takes will have no BPM", r.match)
			}
			if !r.sleep(r.RetryDelay) {
				return
			}
			continue
		}

		f, err := os.Open(path)
		if err != nil {
			if !r.quiet.Swap(true) {
				log.Printf("[!] midi: open %s: %v", path, err)
			}
			if !r.sleep(r.RetryDelay) {
				return
			}
			continue
		}

		r.mu.Lock()
		r.file = f
		r.mu.Unlock()

		r.quiet.Store(false)
		r.device.Store(path)
		r.connected.Store(true)
		log.Printf("[*] midi: reading clock from %s", path)

		r.readLoop(f)

		r.connected.Store(false)
		r.device.Store("")
		r.closeDevice()

		select {
		case <-r.stop:
			return
		default:
			log.Printf("[*] midi: %s closed — rediscovering", path)
		}
		if !r.sleep(r.RetryDelay) {
			return
		}
	}
}

// readLoop timestamps at arrival, one time.Now() per read rather than per
// byte. Batch-then-timestamp cannot place a beat against an audio frame, and
// the estimator refuses outright once most pulses stop carrying distinct
// arrival times — so a read that returns as its bytes land is a correctness
// requirement, not an optimisation.
func (r *Reader) readLoop(f *os.File) {
	buf := make([]byte, readBuf)
	var sawTransport bool

	for {
		n, err := f.Read(buf)
		now := time.Now()
		for _, b := range buf[:n] {
			r.clock.Feed(now, b)
		}

		// Transport was never validly tested on this hardware. Logging the
		// first one seen settles the question during ordinary use, and costs
		// one bool.
		if !sawTransport {
			for _, b := range buf[:n] {
				if b == StartByte || b == ContinueByte || b == StopByte {
					log.Printf("[*] midi: transport byte %#x seen — the EP does send transport", b)
					sawTransport = true
					break
				}
			}
		}

		if err != nil {
			if !errors.Is(err, io.EOF) {
				log.Printf("[!] midi: read %s: %v", f.Name(), err)
			}
			return
		}

		select {
		case <-r.stop:
			return
		default:
		}
	}
}

// sleep waits d, or returns false if the reader was stopped first.
func (r *Reader) sleep(d time.Duration) bool {
	select {
	case <-r.stop:
		return false
	case <-time.After(d):
		return true
	}
}

func (r *Reader) closeDevice() {
	r.mu.Lock()
	f := r.file
	r.file = nil
	r.mu.Unlock()
	if f != nil {
		f.Close()
	}
	r.connected.Store(false)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd v2-go && go test ./midi/ -v -race`
Expected: PASS, 23 tests.

If `TestReaderTimestampsFinelyEnoughToEstimateTempo` is flaky under `-race` on
a loaded machine, widen its tolerance — it exists to prove timestamps are
per-arrival, not to measure the scheduler. Do not weaken it to the point where
a batching reader would pass: `ok` must still be true.

- [ ] **Step 5: Commit**

```bash
cd /Users/gabeduke/projects/audio-dashcam
git add v2-go/midi/reader.go v2-go/midi/reader_test.go
git commit -m "$(cat <<'EOF'
Read the EP's clock in its own goroutine, and recover from unplugs

The reader mirrors Capture.supervise's shape -- detect, back off,
rediscover, reopen -- because the failure is the same one: the interface
being unplugged. What it deliberately does not share is any path back
into the capture thread. Every error is logged and dropped.

Timestamps are taken per read, not per byte and not per batch. That is a
correctness requirement rather than an optimisation: the estimator
refuses outright once most pulses stop carrying distinct arrival times,
so a reader that accumulated bytes before stamping them would produce no
tempo at all.

The tests run over a FIFO named midiC2D0 inside a fixture snd directory,
so discovery, open, read, timestamping and recovery are all exercised
with no hardware and on a Mac.

"No device" logs once per transition rather than once per retry. The EP
is absent for days at a time and a 4-second retry would otherwise write
20,000 identical lines a day into the journal.

Stop closes the device without waiting for the read goroutine: a
blocking read on a character device cannot be interrupted portably, and
the only caller is process shutdown, where waiting could hang and a
parked goroutine costs nothing.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01UHd1yoZ1gjE864aXud9Lqe
EOF
)"
```

---

## Task 4: `BPM` in the sidecar

**Files:**
- Modify: `v2-go/audio/meta.go:33-38`
- Test: `v2-go/audio/meta_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `v2-go/audio/meta_test.go`:

```go
func TestMetaBPMRoundTrips(t *testing.T) {
	dir := t.TempDir()
	wav := filepath.Join(dir, "jam_a.wav")

	bpm := 129.87
	if err := WriteMeta(wav, Meta{Label: "one", BPM: &bpm}); err != nil {
		t.Fatal(err)
	}

	got := ReadMeta(wav)
	if got.BPM == nil {
		t.Fatal("BPM = nil, want 129.87")
	}
	if *got.BPM != 129.87 {
		t.Errorf("BPM = %v, want 129.87", *got.BPM)
	}
	if got.Label != "one" {
		t.Errorf("Label = %q, want %q", got.Label, "one")
	}
}

// Absent must be distinguishable from zero. A take saved with no MIDI device
// present has no tempo; it does not have a tempo of nothing.
func TestMetaAbsentBPMIsNilNotZero(t *testing.T) {
	dir := t.TempDir()
	wav := filepath.Join(dir, "jam_a.wav")

	if err := WriteMeta(wav, Meta{Label: "one"}); err != nil {
		t.Fatal(err)
	}
	if got := ReadMeta(wav); got.BPM != nil {
		t.Errorf("BPM = %v, want nil", *got.BPM)
	}

	b, err := os.ReadFile(metaPath(wav))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "bpm") {
		t.Errorf("sidecar carries a bpm key when none was set: %s", b)
	}
}

// A sidecar written before this field existed must still load. Version is
// deliberately not bumped: the rule in meta.go is to bump only for a change
// older readers cannot tolerate, and an optional additive field is tolerable.
func TestMetaSidecarWithoutBPMStillLoads(t *testing.T) {
	dir := t.TempDir()
	wav := filepath.Join(dir, "jam_a.wav")
	old := `{"version":1,"label":"before bpm existed","starred":true}`
	if err := os.WriteFile(metaPath(wav), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}

	got := ReadMeta(wav)
	if got.Label != "before bpm existed" || !got.Starred {
		t.Errorf("old sidecar did not load: %+v", got)
	}
	if got.BPM != nil {
		t.Errorf("BPM = %v, want nil", *got.BPM)
	}
	if got.Version != 1 {
		t.Errorf("Version = %d, want 1 (this field must not bump it)", got.Version)
	}
}

// The other direction: a sidecar carrying a BPM must load in a build that
// predates the field. Version 1 is what makes that true, so pin it.
func TestMetaVersionIsUnchangedByBPM(t *testing.T) {
	if MetaVersion != 1 {
		t.Errorf("MetaVersion = %d, want 1; adding an optional field must not bump it", MetaVersion)
	}
}
```

`meta_test.go` currently imports only `os`, `path/filepath` and `testing`.
Add `"strings"` to it.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd v2-go && go test ./audio/ -run TestMeta -v`
Expected: FAIL with `unknown field BPM in struct literal of type Meta`.

- [ ] **Step 3: Write the implementation**

In `v2-go/audio/meta.go`, replace the `Meta` struct:

```go
// Meta is the per-take sidecar. Every field but Version is optional: an absent
// sidecar means an unnamed, unstarred, untrimmed take, which is what keeps
// takes recorded before this feature valid without migration.
type Meta struct {
	Version int    `json:"version"`
	Label   string `json:"label,omitempty"`
	Starred bool   `json:"starred,omitempty"`
	Trim    *Trim  `json:"trim,omitempty"`

	// BPM is the tempo the take was played at, read from the EP's MIDI clock
	// at save time and editable afterwards.
	//
	// A pointer because absent and zero are different states: no MIDI device,
	// no clock, or too short a window all mean "no tempo", and a take with a
	// tempo of 0 does not exist. The free-running clock does not reliably
	// match the loaded project tempo — one idle window read 129.87 rock-steady
	// against a project set to 92 — so this is a starting point the owner
	// overrides, never a fact.
	BPM *float64 `json:"bpm,omitempty"`
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd v2-go && go test ./audio/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/gabeduke/projects/audio-dashcam
git add v2-go/audio/meta.go v2-go/audio/meta_test.go
git commit -m "$(cat <<'EOF'
Carry an optional BPM in the take sidecar

A pointer because absent and zero are different states: no MIDI device,
no clock, or too short a window all mean "no tempo", and a take with a
tempo of 0 does not exist.

MetaVersion stays at 1. The rule the file already states is to bump only
for a change older readers cannot tolerate, and an optional additive
field is tolerable in both directions -- a sidecar written before this
loads here, and one written here loads in a build that predates it. Both
directions are pinned by tests, along with the version itself, so a
later change cannot bump it by accident.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01UHd1yoZ1gjE864aXud9Lqe
EOF
)"
```

---

## Task 5: Stamp the tempo at save time

**Files:**
- Modify: `v2-go/audio/save.go` (imports, `Saver` struct, `Save`, `Take`, `ListTakes`)
- Test: `v2-go/audio/save_test.go`

`audio` does not import `midi`. The dependency is inverted through a
one-method interface so the cgo-tainted audio package keeps no knowledge of
MIDI and this path tests with a fake.

- [ ] **Step 1: Write the failing tests**

Append to `v2-go/audio/save_test.go`:

```go
// fakeTempo is a TempoSource that answers from a fixed value and records the
// window it was asked about.
type fakeTempo struct {
	bpm      float64
	ok       bool
	panics   bool
	gotStart time.Time
	gotEnd   time.Time
}

func (f *fakeTempo) BPM(start, end time.Time) (float64, bool) {
	if f.panics {
		panic("a tempo source must never be able to break a save")
	}
	f.gotStart, f.gotEnd = start, end
	return f.bpm, f.ok
}

func TestStampTempoWritesTheSidecar(t *testing.T) {
	dir := t.TempDir()
	wav := writeFakeTake(t, dir, "jam_a.wav", 0)

	src := &fakeTempo{bpm: 129.874321, ok: true}
	stampTempo(wav, src, time.Now(), 30*time.Second)

	got := ReadMeta(wav)
	if got.BPM == nil {
		t.Fatal("BPM = nil, want 129.87")
	}
	// Rounded to two decimals: the estimator's precision is not meaningful
	// past that and a sidecar full of 129.87432100000001 helps nobody.
	if *got.BPM != 129.87 {
		t.Errorf("BPM = %v, want 129.87", *got.BPM)
	}
}

func TestStampTempoAsksAboutTheCapturedWindow(t *testing.T) {
	dir := t.TempDir()
	wav := writeFakeTake(t, dir, "jam_a.wav", 0)

	end := time.Now()
	src := &fakeTempo{bpm: 120, ok: true}
	stampTempo(wav, src, end, 30*time.Second)

	if !src.gotEnd.Equal(end) {
		t.Errorf("end = %v, want %v", src.gotEnd, end)
	}
	if want := end.Add(-30 * time.Second); !src.gotStart.Equal(want) {
		t.Errorf("start = %v, want %v", src.gotStart, want)
	}
}

// No clock, no device, too short a window: all of these leave the take alone.
func TestStampTempoWritesNothingWhenThereIsNoReading(t *testing.T) {
	dir := t.TempDir()
	wav := writeFakeTake(t, dir, "jam_a.wav", 0)

	stampTempo(wav, &fakeTempo{ok: false}, time.Now(), 30*time.Second)

	if _, err := os.Stat(metaPath(wav)); !os.IsNotExist(err) {
		t.Errorf("a sidecar was written for a take with no reading (err = %v)", err)
	}
}

func TestStampTempoWithNoSourceIsANoOp(t *testing.T) {
	dir := t.TempDir()
	wav := writeFakeTake(t, dir, "jam_a.wav", 0)

	stampTempo(wav, nil, time.Now(), 30*time.Second)

	if _, err := os.Stat(metaPath(wav)); !os.IsNotExist(err) {
		t.Errorf("a sidecar was written with no tempo source (err = %v)", err)
	}
}

// The hard rule from the spec: a save must never fail because of MIDI. A
// tempo source that panics is the most hostile version of that.
func TestStampTempoSurvivesAPanickingSource(t *testing.T) {
	dir := t.TempDir()
	wav := writeFakeTake(t, dir, "jam_a.wav", 0)

	stampTempo(wav, &fakeTempo{panics: true}, time.Now(), 30*time.Second)
	// Reaching here without the process dying is the assertion.
}

// Stamping must not clobber a label a user set between the write and the
// stamp, and must not resurrect a take whose sidecar says it is newer.
func TestStampTempoPreservesExistingSidecarFields(t *testing.T) {
	dir := t.TempDir()
	wav := writeFakeTake(t, dir, "jam_a.wav", 0)
	if err := WriteMeta(wav, Meta{Label: "keep me", Starred: true}); err != nil {
		t.Fatal(err)
	}

	stampTempo(wav, &fakeTempo{bpm: 100, ok: true}, time.Now(), 30*time.Second)

	got := ReadMeta(wav)
	if got.Label != "keep me" || !got.Starred {
		t.Errorf("stamping lost sidecar fields: %+v", got)
	}
	if got.BPM == nil || *got.BPM != 100 {
		t.Errorf("BPM = %v, want 100", got.BPM)
	}
}

func TestListTakesReportsBPM(t *testing.T) {
	dir := t.TempDir()
	wav := writeFakeTake(t, dir, "jam_a.wav", time.Minute)
	bpm := 92.5
	if err := WriteMeta(wav, Meta{BPM: &bpm}); err != nil {
		t.Fatal(err)
	}

	takes, err := ListTakes(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(takes) != 1 || takes[0].BPM == nil {
		t.Fatalf("BPM missing from the listing: %+v", takes)
	}
	if *takes[0].BPM != 92.5 {
		t.Errorf("BPM = %v, want 92.5", *takes[0].BPM)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd v2-go && go test ./audio/ -run 'TestStampTempo|TestListTakesReportsBPM' -v`
Expected: FAIL with `undefined: stampTempo` and `t.BPM undefined`.

- [ ] **Step 3: Write the implementation**

In `v2-go/audio/save.go`, add `"math"` to the imports.

Add the interface and the `Saver` field. Replace the `Saver` struct and
`NewSaver`:

```go
// TempoSource reports the tempo over a wall-clock interval, or false when
// there is no defensible reading.
//
// It is an interface, and audio does not import the midi package, so that a
// MIDI failure has no path into the capture thread and this package — which is
// cgo and PortAudio — keeps no knowledge of MIDI at all. The implementation is
// midi.Reader; the tests use a fake.
type TempoSource interface {
	BPM(start, end time.Time) (float64, bool)
}

// Saver turns a slice of the ring into a take on disk, plus a preview and
// waveform peaks.
type Saver struct {
	cap *Capture

	mu        sync.Mutex
	lastSaved string
	saving    bool
	tempo     TempoSource
}

func NewSaver(c *Capture) *Saver { return &Saver{cap: c} }

// SetTempoSource attaches a clock. Nil, or never called, means takes carry no
// BPM — which is the correct behaviour on a machine with no MIDI at all.
func (s *Saver) SetTempoSource(t TempoSource) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tempo = t
}

func (s *Saver) tempoSource() TempoSource {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tempo
}
```

In `Save`, capture the window end right after `Snapshot` and stamp after the
peaks are written. Replace this block:

```go
	data, gotFrames := s.cap.Ring().Snapshot(frames)
	if gotFrames == 0 {
		return "", ErrNoAudio
	}
```

with:

```go
	data, gotFrames := s.cap.Ring().Snapshot(frames)
	// The end of the captured window, in wall-clock terms. The ring stores
	// frames and a counter and carries no clock of its own, so this is
	// derived rather than read: now, minus the snapshot's duration.
	//
	// It runs late by the capture pipeline's latency — INPUT_LATENCY_MS plus
	// one FRAMES_PER_BUFFER block plus the hand-off, so 150-250ms. Against a
	// 30-second window that is under 1%, and the tempo is a median over the
	// whole window rather than a value placed at an instant. Frame-exact
	// alignment is the scrubber's problem, not this one's.
	capturedAt := time.Now()
	if gotFrames == 0 {
		return "", ErrNoAudio
	}
```

Then, immediately after the `WritePeaks` block and before `s.mu.Lock()` sets
`lastSaved`, insert:

```go
	stampTempo(wavPath, s.tempoSource(),
		capturedAt, time.Duration(float64(gotFrames)/float64(cfg.SampleRate)*float64(time.Second)))
```

Add `stampTempo` next to `makePreview`:

```go
// stampTempo merges a BPM into a take's sidecar, if the clock has one to give.
//
// The spec's hard rule is that a save must never fail because of MIDI: no
// device, no clock, a parse error, an unplugged interface, or an
// implementation that panics all produce a take with no BPM and nothing else.
// The recover is not defensive habit — it is the only thing standing between a
// third-party bug and a lost recording, and by this point the WAV is already
// safely on disk.
func stampTempo(wavPath string, src TempoSource, end time.Time, window time.Duration) {
	if src == nil {
		return
	}
	defer func() {
		if p := recover(); p != nil {
			log.Printf("[!] midi: tempo source panicked for %s: %v", filepath.Base(wavPath), p)
		}
	}()

	bpm, ok := src.BPM(end.Add(-window), end)
	if !ok {
		return
	}
	// Two decimals: the estimator's precision is not meaningful past that, and
	// the field is a starting point the owner edits, not a measurement.
	bpm = math.Round(bpm*100) / 100

	m := ReadMeta(wavPath)
	m.BPM = &bpm
	if err := WriteMeta(wavPath, m); err != nil {
		log.Printf("[!] midi: bpm for %s: %v", filepath.Base(wavPath), err)
		return
	}
	log.Printf("[*] %s — %.2f BPM", filepath.Base(wavPath), bpm)
}
```

Add `BPM` to `Take`, after `Trim`:

```go
	Label   string   `json:"label"`
	Starred bool     `json:"starred"`
	Trim    *Trim    `json:"trim,omitempty"`
	BPM     *float64 `json:"bpm,omitempty"`
```

And in `ListTakes`, after `t.Trim = m.Trim`:

```go
		t.BPM = m.BPM
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd v2-go && go test ./audio/ -v -race`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
cd /Users/gabeduke/projects/audio-dashcam
git add v2-go/audio/save.go v2-go/audio/save_test.go
git commit -m "$(cat <<'EOF'
Stamp each take with the tempo its window was played at

Save takes a TempoSource interface rather than importing the midi
package. The dependency is inverted so this package -- which is cgo and
PortAudio -- keeps no knowledge of MIDI, so a MIDI failure has no path
into the capture thread, and so the save path tests with a fake.

The spec claims Save already knows the captured window. It does not:
Ring stores frames and a counter and carries no wall clock at all. The
window is derived instead -- time.Now() taken right after Snapshot,
minus the snapshot's duration. That runs late by the pipeline's latency,
100ms of INPUT_LATENCY_MS plus a 2048-frame block plus the hand-off, so
150-250ms. Against a 30-second window that is under 1%, and the tempo is
a median over the whole window rather than a value placed at an instant.

A save must never fail because of MIDI. No device, no clock, too short a
window, and a source that panics outright all produce a take with no BPM
and nothing else. The recover is not defensive habit: by that point the
WAV is already on disk, and it is the only thing between a bug in the
clock and a lost recording.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01UHd1yoZ1gjE864aXud9Lqe
EOF
)"
```

---

## Task 6: Wire the reader into the process

**Files:**
- Modify: `v2-go/main.go:32-40`

There is no new config: `DEVICE_MATCH` already names the interface and the
same string matches the ALSA card. Resist adding a `MIDI_ENABLED` flag — a
machine with no rawmidi node produces no BPM, which is the same outcome.

- [ ] **Step 1: Write the implementation**

In `v2-go/main.go`, add the import:

```go
	"github.com/gabeduke/audio-dashcam/v2-go/midi"
```

Replace the block from `saver := audio.NewSaver(cap)` through the `api.New`
line:

```go
	saver := audio.NewSaver(cap)

	// The clock ring covers the same window as the audio ring, so a full-ring
	// save can still ask about its oldest end. Sized in pulses at the fastest
	// tempo the BPM field accepts.
	clock := midi.NewClock(midi.CapacityFor(cfg.RingSeconds))
	reader := midi.NewReader(cfg.DeviceMatch, clock)
	reader.Start()
	defer reader.Stop()
	saver.SetTempoSource(reader)

	r := mux.NewRouter()
	api.New(cfg, cap, saver, cap.Envelope(), reader).SetupRoutes(r)
```

- [ ] **Step 2: Run the build to verify it fails at the API call**

Run: `cd v2-go && go build ./...`
Expected: FAIL with `too many arguments in call to api.New`. That is Task 7's
job; leave it failing and go straight to Task 7 rather than committing a
broken build.

- [ ] **Step 3: Commit — with Task 7**

This task's change does not compile on its own. It is committed together with
Task 7's API change; see that task's commit step.

---

## Task 7: MIDI state on `/api/status`

**Files:**
- Modify: `v2-go/api/api.go:26-35` (struct and `New`), `:59-105` (status)
- Test: `v2-go/api/api_test.go:20-28` (`newTestAPI`), plus new tests

- [ ] **Step 1: Write the failing tests**

In `v2-go/api/api_test.go`, update the helper (every existing test calls it, so
this one edit keeps them compiling):

```go
// newTestAPI builds an API over a temp takes directory. Capture, Saver and the
// MIDI source are nil because the metadata handler never touches them; a test
// that needed audio would have to run on hardware.
func newTestAPI(t *testing.T) (*mux.Router, string) {
	t.Helper()
	dir := t.TempDir()
	a := New(&config.Config{OutputDir: dir}, nil, nil, nil, nil)
	r := mux.NewRouter()
	a.SetupRoutes(r)
	return r, dir
}
```

Then append:

```go
// fakeMIDI stands in for midi.Reader.
type fakeMIDI struct {
	connected bool
	bpm       float64
	ok        bool
}

func (f *fakeMIDI) Connected() bool { return f.connected }
func (f *fakeMIDI) BPM(start, end time.Time) (float64, bool) {
	return f.bpm, f.ok
}

func newStatusAPI(t *testing.T, m MIDISource) *mux.Router {
	t.Helper()
	a := New(&config.Config{OutputDir: t.TempDir()}, nil, nil, nil, m)
	r := mux.NewRouter()
	// Only the MIDI half of the status response is exercised here; the rest
	// needs a live Capture.
	r.HandleFunc("/api/midi", func(w http.ResponseWriter, req *http.Request) {
		conn, bpm := a.midiState()
		writeJSON(w, http.StatusOK, map[string]any{"midi_connected": conn, "midi_bpm": bpm})
	})
	return r
}

func midiState(t *testing.T, m MIDISource) (bool, *float64) {
	t.Helper()
	r := newStatusAPI(t, m)
	req := httptest.NewRequest(http.MethodGet, "/api/midi", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	var got struct {
		Connected bool     `json:"midi_connected"`
		BPM       *float64 `json:"midi_bpm"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v (body %s)", err, w.Body.String())
	}
	return got.Connected, got.BPM
}

func TestStatusReportsLiveBPM(t *testing.T) {
	conn, bpm := midiState(t, &fakeMIDI{connected: true, bpm: 129.87, ok: true})
	if !conn {
		t.Error("midi_connected = false, want true")
	}
	if bpm == nil || *bpm != 129.87 {
		t.Errorf("midi_bpm = %v, want 129.87", bpm)
	}
}

// Connected but silent is the exact shape of the failure worth catching: the
// EP ships with clock-send off, and a run with it off is indistinguishable
// from firmware that cannot send clock. null, not 0.
func TestStatusReportsConnectedWithNoClockAsNull(t *testing.T) {
	conn, bpm := midiState(t, &fakeMIDI{connected: true, ok: false})
	if !conn {
		t.Error("midi_connected = false, want true")
	}
	if bpm != nil {
		t.Errorf("midi_bpm = %v, want null", *bpm)
	}
}

func TestStatusWithNoMIDISourceIsNotConnected(t *testing.T) {
	conn, bpm := midiState(t, nil)
	if conn {
		t.Error("midi_connected = true with no source, want false")
	}
	if bpm != nil {
		t.Errorf("midi_bpm = %v, want null", *bpm)
	}
}
```

Add `"time"` to that file's imports.

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd v2-go && go test ./api/ -v`
Expected: FAIL with `undefined: MIDISource` and `a.midiState undefined`.

- [ ] **Step 3: Write the implementation**

In `v2-go/api/api.go`, add `"time"` to the imports, then replace the `API`
struct and `New`:

```go
// MIDISource is the live clock, as the API needs it: is it there, and what is
// it saying right now. Satisfied by *midi.Reader; nil is a valid value and
// means this build has no clock.
type MIDISource interface {
	Connected() bool
	BPM(start, end time.Time) (float64, bool)
}

type API struct {
	cfg   *config.Config
	cap   *audio.Capture
	saver *audio.Saver
	env   *audio.Envelope
	midi  MIDISource
}

func New(cfg *config.Config, cap *audio.Capture, saver *audio.Saver, env *audio.Envelope, m MIDISource) *API {
	return &API{cfg: cfg, cap: cap, saver: saver, env: env, midi: m}
}

// liveTempoWindow is how far back the status poll asks about. Eight seconds is
// two quarter notes' worth down to about 15 BPM, so the reading survives any
// tempo the field accepts, while staying short enough that the number moves
// with the room rather than lagging it.
const liveTempoWindow = 8 * time.Second

// midiState reports the clock's presence and its current tempo. A nil BPM
// means no defensible reading — which is the shape that catches the EP's
// clock-send being switched off, since that looks exactly like connected and
// silent.
func (a *API) midiState() (bool, *float64) {
	if a.midi == nil {
		return false, nil
	}
	connected := a.midi.Connected()
	now := time.Now()
	if bpm, ok := a.midi.BPM(now.Add(-liveTempoWindow), now); ok {
		return connected, &bpm
	}
	return connected, nil
}
```

Add the fields to `statusResponse`, after `MinFreeGB`:

```go
	MinFreeGB       float64   `json:"min_free_gb"`
	MIDIConnected   bool      `json:"midi_connected"`
	MIDIBPM         *float64  `json:"midi_bpm"`
```

And in `handleStatus`, before the `writeJSON` call:

```go
	midiConnected, midiBPM := a.midiState()
```

then add to the literal, after `MinFreeGB: a.cfg.MinFreeGB,`:

```go
		MIDIConnected:   midiConnected,
		MIDIBPM:         midiBPM,
```

- [ ] **Step 4: Run the tests and the build to verify they pass**

Run: `cd v2-go && go build ./... && go test ./... -race && go vet ./... && gofmt -l .`
Expected: build succeeds (Task 6's `api.New` call now matches), all tests PASS,
vet silent, `gofmt -l` prints nothing.

- [ ] **Step 5: Commit**

```bash
cd /Users/gabeduke/projects/audio-dashcam
git add v2-go/main.go v2-go/api/api.go v2-go/api/api_test.go
git commit -m "$(cat <<'EOF'
Wire the clock into the process and report it on /api/status

main constructs the clock ring and reader, hands the reader to the saver
as a TempoSource and to the API as a MIDISource, and starts it. No new
config: DEVICE_MATCH already names the interface and the same string
matches the ALSA card entry. A machine with no rawmidi node produces no
BPM, which is what a MIDI_ENABLED flag would have produced anyway.

/api/status gains midi_connected and midi_bpm. The spec stops at the
sidecar, which left its own verification list with no surface -- there
was no way to see that the clock was alive or that the reader had
recovered from an unplug short of saving a take. Added with the owner's
approval.

midi_bpm is null rather than 0 when there is no defensible reading,
which is what makes connected-and-silent visible. That is the exact
shape of the EP shipping with clock-send switched off, a state the
hardware notes record as indistinguishable from firmware that cannot
send clock at all.

The status window is 8 seconds: two quarter notes' worth down to about
15 BPM, so it holds across the whole editable range, while staying short
enough to move with the room.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01UHd1yoZ1gjE864aXud9Lqe
EOF
)"
```

---

## Task 8: Editing the BPM over `PATCH /api/take`

**Files:**
- Modify: `v2-go/api/api.go` (`handleTakePatch` body struct, merge, response)
- Test: `v2-go/api/api_test.go`

Follows the existing label-rename path exactly: `json.RawMessage` so absent,
`null` and a value are three distinct states, as `trim` already does. Absent
leaves the field alone; `null` clears it; a number in 20–400 sets it.

- [ ] **Step 1: Write the failing tests**

Append to `v2-go/api/api_test.go`:

```go
func TestPatchTakeSetsBPM(t *testing.T) {
	r, dir := newTestAPI(t)
	writeTake(t, dir, "jam_a.wav")

	w := patch(t, r, "jam_a.wav", `{"bpm":129.874}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}

	got := audio.ReadMeta(filepath.Join(dir, "jam_a.wav"))
	if got.BPM == nil || *got.BPM != 129.87 {
		t.Errorf("BPM = %v, want 129.87 (rounded to two decimals)", got.BPM)
	}
}

// An empty submission clears the field rather than storing zero. The UI sends
// null when the input is emptied.
func TestPatchTakeClearsBPMWithNull(t *testing.T) {
	r, dir := newTestAPI(t)
	wav := writeTake(t, dir, "jam_a.wav")
	bpm := 120.0
	if err := audio.WriteMeta(wav, audio.Meta{BPM: &bpm}); err != nil {
		t.Fatal(err)
	}

	w := patch(t, r, "jam_a.wav", `{"bpm":null}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}
	if got := audio.ReadMeta(wav); got.BPM != nil {
		t.Errorf("BPM = %v, want nil", *got.BPM)
	}
}

// The merge must stay a merge: setting a tempo cannot silently drop a label.
func TestPatchTakeBPMDoesNotClearTheLabel(t *testing.T) {
	r, dir := newTestAPI(t)
	wav := writeTake(t, dir, "jam_a.wav")
	if err := audio.WriteMeta(wav, audio.Meta{Label: "keep me", Starred: true}); err != nil {
		t.Fatal(err)
	}

	if w := patch(t, r, "jam_a.wav", `{"bpm":92}`); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	got := audio.ReadMeta(wav)
	if got.Label != "keep me" || !got.Starred {
		t.Errorf("patching bpm lost fields: %+v", got)
	}
}

// And the reverse: renaming must not drop a tempo.
func TestPatchTakeLabelDoesNotClearTheBPM(t *testing.T) {
	r, dir := newTestAPI(t)
	wav := writeTake(t, dir, "jam_a.wav")
	bpm := 92.0
	if err := audio.WriteMeta(wav, audio.Meta{BPM: &bpm}); err != nil {
		t.Fatal(err)
	}

	if w := patch(t, r, "jam_a.wav", `{"label":"renamed"}`); w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	got := audio.ReadMeta(wav)
	if got.BPM == nil || *got.BPM != 92 {
		t.Errorf("renaming lost the BPM: %+v", got)
	}
}

// The range is deliberately wider than the EP will ever produce, because the
// whole point of the field is that the owner overrides it.
func TestPatchTakeAcceptsTheRangeBounds(t *testing.T) {
	for _, v := range []string{"20", "400", "20.0", "399.99"} {
		r, dir := newTestAPI(t)
		writeTake(t, dir, "jam_a.wav")
		if w := patch(t, r, "jam_a.wav", `{"bpm":`+v+`}`); w.Code != http.StatusOK {
			t.Errorf("bpm %s: status = %d, want 200 (body %s)", v, w.Code, w.Body.String())
		}
	}
}

func TestPatchTakeRejectsOutOfRangeBPM(t *testing.T) {
	for _, v := range []string{"0", "19.99", "400.01", "1000", "-120"} {
		r, dir := newTestAPI(t)
		wav := writeTake(t, dir, "jam_a.wav")
		w := patch(t, r, "jam_a.wav", `{"bpm":`+v+`}`)
		if w.Code != http.StatusBadRequest {
			t.Errorf("bpm %s: status = %d, want 400", v, w.Code)
		}
		if got := audio.ReadMeta(wav); got.BPM != nil {
			t.Errorf("bpm %s: a rejected value was written anyway (%v)", v, *got.BPM)
		}
	}
}

func TestPatchTakeRejectsNonNumericBPM(t *testing.T) {
	r, dir := newTestAPI(t)
	writeTake(t, dir, "jam_a.wav")

	if w := patch(t, r, "jam_a.wav", `{"bpm":"120"}`); w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

// The response is explicit rather than the Meta struct, so a clear-to-empty
// patch reports what it actually did instead of omitting the field.
func TestPatchTakeResponseCarriesTheBPM(t *testing.T) {
	r, dir := newTestAPI(t)
	writeTake(t, dir, "jam_a.wav")

	w := patch(t, r, "jam_a.wav", `{"bpm":92.5}`)
	var got struct {
		BPM *float64 `json:"bpm"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.BPM == nil || *got.BPM != 92.5 {
		t.Errorf("response bpm = %v, want 92.5", got.BPM)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `cd v2-go && go test ./api/ -run TestPatchTake -v`
Expected: FAIL — `TestPatchTakeSetsBPM` reports `BPM = <nil>, want 129.87`,
because an unknown `bpm` key is currently ignored.

- [ ] **Step 3: Write the implementation**

In `v2-go/api/api.go`, add `"math"` to the imports and these constants next to
`maxLabelLen`:

```go
// minBPM and maxBPM bound an edited tempo. The range is deliberately far wider
// than the EP will ever produce: the free-running clock does not reliably match
// the loaded project tempo, so the point of the field is that the owner
// overrides it — including for takes whose clock reading was confidently wrong.
const (
	minBPM = 20.0
	maxBPM = 400.0
)
```

In `handleTakePatch`, add to the body struct:

```go
	var body struct {
		Label   *string         `json:"label"`
		Starred *bool           `json:"starred"`
		Trim    json.RawMessage `json:"trim"`
		BPM     json.RawMessage `json:"bpm"`
	}
```

and add this merge block after the `Trim` block:

```go
	// RawMessage, like trim: absent, null and a value are three states. An
	// empty submission from the UI arrives as null and clears the field rather
	// than storing a tempo of zero.
	if body.BPM != nil {
		if string(body.BPM) == "null" {
			m.BPM = nil
		} else {
			var v float64
			if err := json.Unmarshal(body.BPM, &v); err != nil {
				writeErr(w, http.StatusBadRequest, "bpm must be a number")
				return
			}
			if math.IsNaN(v) || math.IsInf(v, 0) || v < minBPM || v > maxBPM {
				writeErr(w, http.StatusBadRequest,
					fmt.Sprintf("bpm must be between %g and %g", minBPM, maxBPM))
				return
			}
			v = math.Round(v*100) / 100
			m.BPM = &v
		}
	}
```

And extend the response struct at the end of the handler:

```go
	writeJSON(w, http.StatusOK, struct {
		Label   string      `json:"label"`
		Starred bool        `json:"starred"`
		Trim    *audio.Trim `json:"trim"`
		BPM     *float64    `json:"bpm"`
	}{Label: m.Label, Starred: m.Starred, Trim: m.Trim, BPM: m.BPM})
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `cd v2-go && go test ./... -race && go vet ./... && gofmt -l .`
Expected: all PASS, vet silent, `gofmt -l` prints nothing.

- [ ] **Step 5: Commit**

```bash
cd /Users/gabeduke/projects/audio-dashcam
git add v2-go/api/api.go v2-go/api/api_test.go
git commit -m "$(cat <<'EOF'
Let the owner edit a take's BPM

The clock reading is a starting point, not a fact: the EP's free-running
clock does not reliably match the loaded project tempo, and stability
does not distinguish a correct reading from a confidently wrong one --
one idle window read 129.87 rock-steady against a project set to 92.
Editability is therefore a requirement rather than a convenience, and
the accepted range is deliberately far wider than the EP will ever
produce.

bpm is a json.RawMessage like trim, so absent, null and a value stay
three distinct states. An empty submission arrives as null and clears
the field rather than storing a tempo of zero. Tests pin the merge in
both directions: setting a tempo cannot drop a label, and renaming
cannot drop a tempo.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01UHd1yoZ1gjE864aXud9Lqe
EOF
)"
```

---

## Task 9: The live tempo tile

**Files:**
- Modify: `v2-go/static/index.html:64-73`
- Modify: `v2-go/static/styles.css:236-241`
- Modify: `v2-go/static/app.js:20-22`, `:118-160`

No new file, so `SHELL` in `sw.js` and its cache version stay untouched.

- [ ] **Step 1: Add the tile to the markup**

In `v2-go/static/index.html`, inside `<div class="stats">`, after the
`stat-xruns` block and before the closing `</div>`:

```html
        <div class="stat">
          <div class="k">tempo</div>
          <div class="v" id="stat-tempo">–</div>
        </div>
```

- [ ] **Step 2: Widen the grid**

In `v2-go/static/styles.css`, replace the `.stats` rule:

```css
.stats {
  margin-top: 12px;
  display: grid;
  /* Four tiles at 390px is about 80px each, and .stat .v is 14px tabular mono
     with an ellipsis, so "129.9" fits with room to spare. Verified across
     viewports rather than assumed. */
  grid-template-columns: repeat(4, 1fr);
  gap: 8px;
}
```

- [ ] **Step 3: Render it**

In `v2-go/static/app.js`, add to the `el` map after `statXruns`:

```js
  statTempo: $('stat-tempo'),
```

and in `applyStatus`, next to the other stat assignments:

```js
  // A dash means "no defensible reading", which covers three real states: the
  // EP unplugged, the EP present with clock-send switched off (it ships off,
  // and that looks exactly like firmware that cannot send clock), and a room
  // that has been quiet too briefly to fill two quarter notes.
  el.statTempo.textContent = s.midi_bpm == null ? '–' : s.midi_bpm.toFixed(1);
  el.statTempo.className = s.midi_connected ? 'v' : 'v warn';
```

- [ ] **Step 4: Deploy the static files and check it renders**

```bash
cd /Users/gabeduke/projects/audio-dashcam
./deploy.sh --static
set -a && . ./deploy.local.env && set +a
curl -s "http://${DASHCAM_HOST#*@}/api/status" | python3 -m json.tool | grep midi
```

Expected: `"midi_connected": false` and `"midi_bpm": null` while the interface
is unplugged. (The Go half is not deployed until the next full `./deploy.sh`;
until then these keys are absent and the tile reads `–`, which is also correct.)

Then, following this project's established pattern — a throwaway Playwright
script in the session scratchpad, not committed — load the page at 390x844,
844x390 and 1280x800 and confirm the four tiles fit on one row with no
horizontal scroll and no clipped labels.

- [ ] **Step 5: Commit**

```bash
cd /Users/gabeduke/projects/audio-dashcam
git add v2-go/static/index.html v2-go/static/styles.css v2-go/static/app.js
git commit -m "$(cat <<'EOF'
Show the live tempo as a fourth stat tile

The stats grid goes from three columns to four. At 390px that is about
80px a tile against a 14px tabular-mono value with an ellipsis, so
"129.9" fits; checked across phone portrait, phone landscape and tablet
rather than assumed.

A dash covers three real states, all of which mean the same thing to
someone about to press Capture: the EP unplugged, the EP present with
clock-send switched off, and a window too short to hold two quarter
notes. The second of those is the one worth catching -- clock-send ships
off on the EP, and a run with it off is indistinguishable from firmware
that cannot send clock at all.

No new file under static/, so SHELL in sw.js and its cache version are
deliberately untouched.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01UHd1yoZ1gjE864aXud9Lqe
EOF
)"
```

---

## Task 10: BPM on each take, edited inline

**Files:**
- Modify: `v2-go/static/lib/takes.js:127-145` (`createRow` markup), `:146-162`
  (the `row` object), `:163-220` (handlers), `:238-252` (`updateRow`)
- Modify: `v2-go/static/styles.css` (after `.take-meta`)

The chip sits immediately before `.take-meta` and is styled to read as part of
that line: `129.9 bpm · 0:30 · 12.4 MB`. It reuses the name field's edit
lifecycle — click to edit, Enter or blur commits, Escape cancels.

**Known tap-target deviation:** the chip is smaller than the project's
`--tap: 48px`. That is consistent with `.take-name`'s own `padding: 4px 0`, and
`.seg button` at 40px is already an open item in `STATE.md`. Raise it with the
owner at review rather than solving it here.

- [ ] **Step 1: Add the markup**

In `createRow`, replace the `.take-head` block:

```js
      <div class="take-head">
        <button class="star" type="button" aria-pressed="false" aria-label="Star this take">★</button>
        <button class="take-name" type="button" title="Rename"></button>
        <input class="take-name-input" type="text" maxlength="120" hidden>
        <button class="take-bpm" type="button" title="Set the tempo"></button>
        <input class="take-bpm-input" type="text" inputmode="decimal" maxlength="7" hidden>
        <span class="take-meta"></span>
      </div>
```

and add to the `row` object, after `starBtn`:

```js
      bpmEl: el.querySelector('.take-bpm'),
      bpmInput: el.querySelector('.take-bpm-input'),
      editingBpm: false,
```

- [ ] **Step 2: Add the edit lifecycle**

In `createRow`, after the existing name `keydown` handler and before the
`this.fresh === t.name` block:

```js
    const beginBpmEdit = () => {
      row.editingBpm = true;
      row.bpmInput.value = row.data.bpm == null ? '' : String(row.data.bpm);
      row.bpmInput.placeholder = 'bpm';
      row.bpmEl.hidden = true;
      row.bpmInput.hidden = false;
      row.bpmInput.focus();
      row.bpmInput.select();
    };

    const endBpmEdit = async (commit) => {
      if (!row.editingBpm) return;
      row.editingBpm = false;
      row.bpmInput.hidden = true;
      row.bpmEl.hidden = false;
      if (!commit) return;

      const raw = row.bpmInput.value.trim();
      // Empty clears the field. null is what the server reads as "clear";
      // sending 0 would store a tempo no take can have.
      const next = raw === '' ? null : Number(raw);
      if (next !== null && !Number.isFinite(next)) {
        this.onToast?.('Tempo must be a number', 'bad');
        return;
      }
      const before = row.data.bpm ?? null;
      if (next === before) return;

      try {
        await this.patchTake(row.name, { bpm: next });
      } catch (err) {
        this.onToast?.(`Could not set tempo: ${err.message}`, 'bad');
      }
    };

    row.bpmEl.addEventListener('click', beginBpmEdit);
    row.bpmInput.addEventListener('blur', () => endBpmEdit(true));
    row.bpmInput.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') {
        e.preventDefault();
        row.bpmInput.blur();
      } else if (e.key === 'Escape') {
        e.preventDefault();
        endBpmEdit(false);
      }
    });
```

- [ ] **Step 3: Render it, and protect the in-flight edit**

In `updateRow`, replace the `metaEl` line with:

```js
    // Skip while the user is mid-edit so a 5-second poll cannot overwrite what
    // they are typing — the same reason the label is guarded above.
    if (!row.editingBpm) {
      row.bpmEl.textContent = t.bpm == null ? '+ bpm' : `${t.bpm.toFixed(1)} bpm`;
      row.bpmEl.classList.toggle('unset', t.bpm == null);
    }
    row.metaEl.textContent = `${fmtTime(t.duration_seconds)} · ${fmtSize(t.size_mb)}`;
```

The row's existing reorder guard keys off `row.editing`, which covers the name
only. Extend it in `render` so a poll cannot move a row out from under a tempo
edit either — replace:

```js
        if (row.editing) this.reorderDeferred = true;
```

with:

```js
        if (row.editing || row.editingBpm) this.reorderDeferred = true;
```

- [ ] **Step 4: Style it**

In `v2-go/static/styles.css`, after the `.take-meta` rule:

```css
/* The tempo reads as the first item of the meta line — "129.9 bpm · 0:30 ·
   12.4 MB" — rather than as a separate control, so it costs no new row and no
   new visual weight. It is a smaller tap target than --tap, matching
   .take-name's own padding; see the open item in STATE.md. */
.take-bpm {
  flex: none;
  background: none;
  border: 0;
  padding: 8px 2px 8px 6px;
  font-family: var(--mono); font-size: 11px;
  color: var(--ink-faint);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
  cursor: pointer;
}
.take-bpm::after { content: ' ·'; }
.take-bpm.unset { color: var(--ink-dim); opacity: .7; }
.take-bpm:hover { color: var(--ink); }

.take-bpm-input {
  flex: none;
  width: 5.5em;
  min-width: 0;
  background: var(--panel-2);
  border: 1px solid var(--accent);
  border-radius: 8px;
  padding: 4px 6px;
  font-family: var(--mono); font-size: 12px;
  color: var(--ink);
}
.take-bpm-input:focus { outline: none; }
```

- [ ] **Step 5: Deploy and verify by hand**

```bash
cd /Users/gabeduke/projects/audio-dashcam
./deploy.sh --static
```

In a browser on the tailnet HTTPS URL, against the three existing takes:

- A take with no BPM shows `+ bpm`; clicking it opens an input.
- Typing `92` and pressing Enter shows `92.0 bpm` and survives a reload.
- Clearing it and pressing Enter returns it to `+ bpm`.
- Typing `9` shows the 400-range error as a toast and leaves the value alone.
- Escape cancels without writing.
- Renaming a take does not clear its tempo.
- At 390x844 the head row does not wrap or overflow.

- [ ] **Step 6: Commit**

```bash
cd /Users/gabeduke/projects/audio-dashcam
git add v2-go/static/lib/takes.js v2-go/static/styles.css
git commit -m "$(cat <<'EOF'
Show and edit each take's tempo in the takes list

The chip sits at the head of the meta line and reads as part of it --
"129.9 bpm · 0:30 · 12.4 MB" -- so it costs no new row and no new visual
weight on a phone. A take with no tempo shows "+ bpm", because a take
recorded with no clock present is exactly the case the owner most needs
to type a tempo into.

It reuses the label field's edit lifecycle: click to edit, Enter or blur
commits, Escape cancels, and rendering is skipped mid-edit so the
5-second poll cannot overwrite what is being typed. The reorder guard is
extended to cover it for the same reason the label has one -- moving a
node blurs the focused input, and blur commits, so a poll that reordered
the list would silently save a half-typed value.

An emptied input sends null rather than 0. A take cannot have a tempo of
zero, and the server reads null as a clear.

Co-Authored-By: Claude Opus 5 <noreply@anthropic.com>
Claude-Session: https://claude.ai/code/session_01UHd1yoZ1gjE864aXud9Lqe
EOF
)"
```

---

## Task 11: Deploy and verify on hardware

**Requires the EP-136 plugged in.** As of 2026-09-09 it is not: `/api/status`
reports `capture_healthy: false`, `no input device with >=8 channels`. Tasks
1–10 are all completable without it; this one is not.

**Also required: clock-send must be enabled on the EP.** It ships off, and a
run with it off is indistinguishable from firmware that cannot send clock. If
`midi_connected` is true and the tempo tile shows a dash, check this first.

- [ ] **Step 1: Full deploy**

```bash
cd /Users/gabeduke/projects/audio-dashcam
./deploy.sh
```

If ssh fails to sign, the cause is 1Password holding the agent — see the
blocker in `STATE.md` for the `IdentityAgent none` bypass.

- [ ] **Step 2: Confirm the tests pass on the Pi**

```bash
set -a && . ./deploy.local.env && set +a
ssh "$DASHCAM_HOST" 'cd ~/audio-dashcam/v2-go && go test ./... && go vet ./...'
```

Expected: PASS across `audio`, `api`, `config` and the new `midi` package.

- [ ] **Step 3: Confirm the reader found the device**

```bash
ssh "$DASHCAM_HOST" 'journalctl _SYSTEMD_USER_UNIT=audio-dashcam.service -n 100' \
  | grep -i midi
curl -s "http://${DASHCAM_HOST#*@}/api/status" | python3 -m json.tool | grep midi
```

Expected: `[*] midi: reading clock from /dev/snd/midiC2D0`, and
`"midi_connected": true` with a plausible `midi_bpm`.

If the log instead reads `no device matching "EP-136"`, get the real card list
before guessing:

```bash
ssh "$DASHCAM_HOST" 'cat /proc/asound/cards; ls /dev/snd/'
```

- [ ] **Step 4: Cross-check the estimator against the probe**

Run the probe and compare its rolling-median figure to what `/api/status`
reports over the same period.

```bash
ssh -t "$DASHCAM_HOST" 'python3 ~/audio-dashcam/scripts/midi-probe.py'
# then, while it runs:
curl -s "http://${DASHCAM_HOST#*@}/api/status" | python3 -c \
  "import json,sys; print(json.load(sys.stdin)['midi_bpm'])"
```

**Read the log at `/tmp/midi-probe.log`, not the verdict** — the per-phase
block is where the evidence is. The spec's bar is **within 1 BPM**. Compare
against `rolling median`, not `trust this one`: the estimator here is a port of
the rolling median, and `overall` is a different statistic.

- [ ] **Step 5: Save a take and check its sidecar**

```bash
curl -s -X POST "http://${DASHCAM_HOST#*@}/api/trigger?seconds=30" | python3 -m json.tool
ssh "$DASHCAM_HOST" 'ls -t ~/audio-dashcam/jam_saves/*.meta.json | head -1 | xargs cat'
```

Expected: a sidecar with `"version": 1` and a `"bpm"` within 1 BPM of the
probe's rolling median. The take appears in the UI with that tempo.

- [ ] **Step 6: The unplug test — the one that matters**

With capture running and the clock arriving, unplug the EP.

- [ ] The log shows `no input device with >=8 channels`, **not** `Illegal
      combination of I/O devices`. The second message means the device list has
      gone stale and the rescan has stopped — a hot-plug regression, unrelated
      to this work, and a reason to stop.
- [ ] `midi_connected` goes false and the tempo tile shows a dash.
- [ ] `xruns` does not increase while the reader is retrying.
- [ ] A save during this period succeeds and produces a take with **no** `bpm`
      key at all — not `"bpm": 0`.

Plug it back in.

- [ ] The log shows `midi: reading clock from ...` again without a restart.
- [ ] `midi_connected` returns to true, capture recovers, and a save now
      carries a BPM again.

- [ ] **Step 7: Answer the open transport question**

While the interface is connected, press play and stop on the EP's sequencer,
then:

```bash
ssh "$DASHCAM_HOST" 'journalctl _SYSTEMD_USER_UNIT=audio-dashcam.service -n 200' \
  | grep -i transport
```

A `transport byte 0xfa seen` line settles a question the probe could never
answer — its drain discarded anything buffered before a phase began, and the
device was operated before each phase started. **Record the result in
`STATE.md` either way**: a positive answer is what a future beat grid needs,
and a negative one closes the door on it.

- [ ] **Step 8: Update `STATE.md` and commit any corrections**

Move MIDI clock out of the backlog, record the hardware results, and record any
defect this plan turned out to contain — the ribbon plan's "Corrections found
during execution" section is the pattern.

---

## Self-review against the spec

| Spec requirement | Task |
|---|---|
| MIDI reader, rawmidi device node, no cgo/subprocess/library | 3 |
| Discovery via `/proc/asound/cards` against `DEVICE_MATCH` | 2 |
| Realtime-only parsing, realtime tested before anything else | 1 (`Feed`) |
| Clock ring covering the audio ring's window | 1, sized in 6 |
| Median of rolling quarter-note windows, not the mean | 1 |
| Wall-clock correlation, not frame-exact | 5 |
| Tempo written at save time from the captured window | 5 |
| Under two quarter notes yields no BPM, absent not zero | 1, 5 |
| A save must never fail because of MIDI | 5 |
| `Meta.BPM *float64` with `omitempty`, no `MetaVersion` bump | 4 |
| Editing follows the label-rename path; 20–400; empty clears | 8 |
| BPM beside the label, edited inline, phone-first | 10 |
| Reader owns its own lifecycle, errors never reach capture | 3 |
| Recovery on unplug: detect, back off, rescan, reopen | 3, verified 11 |
| Estimator vs synthetic input, exact to two decimals | 1 |
| Chunked arrival handled | 1 (refuses on a distinct-timestamp guard) |
| Realtime inside another message counted, nothing corrupted | 1, 3 |
| Save with no device gives a valid take, no BPM | 5, verified 11 |
| Save with clock within 1 BPM of `midi-probe.py` | 11 |
| Old sidecars load; new sidecars load in an older build | 4 |
| Hardware: unplug mid-run, capture undisturbed, reader recovers | 11 |

Two spec statements are contradicted rather than implemented, both documented
in "Two corrections to the spec" above: `Saver.Save` does not already know the
captured window, and no MIDI parser is needed.

The spec's out-of-scope list — the buffer scrubber, whole-buffer waveform,
region selection, bar overlay, meter setting, Phase 2 trim, Phase 3 egress, the
beat grid — is untouched by every task here. The one deliberate nod to it is
`Clock.Transport`, which counts what it is given and is read by nothing.
