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
// never got individual timestamps.
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
