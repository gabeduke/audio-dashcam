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
// A FIFO behaves enough like a rawmidi character device for this -- a blocking
// read that returns as bytes are written -- so discovery, opening, timestamping
// and recovery are all exercised with no hardware and on a Mac.
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
	// Short, and stepped past below: writing the instant the reader connects is
	// exactly what the drain window is there to discard.
	r.DrainWindow = 20 * time.Millisecond
	r.Start()
	defer r.Stop()

	// Opening the write end unblocks the reader's open.
	w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open write end: %v", err)
	}
	defer w.Close()

	waitFor(t, "the reader to connect", r.Connected)
	time.Sleep(60 * time.Millisecond) // past the drain window

	// Written one at a time so each is timestamped as it lands -- the same
	// shape a rawmidi device delivers.
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
	r.DrainWindow = 20 * time.Millisecond
	r.Start()
	defer r.Stop()

	w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open write end: %v", err)
	}
	defer w.Close()
	waitFor(t, "the reader to connect", r.Connected)
	time.Sleep(60 * time.Millisecond) // past the drain window

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
	r.DrainWindow = 20 * time.Millisecond
	r.Start()
	defer r.Stop()

	w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open write end: %v", err)
	}
	defer w.Close()
	waitFor(t, "the reader to connect", r.Connected)
	time.Sleep(60 * time.Millisecond) // past the drain window

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

// Backlog on open must not be counted.
//
// ALSA buffers incoming clock from the moment the device node appears, and the
// reader can be up to RetryDelay behind that. Everything that piled up is then
// delivered in the first read or two, so those pulses share a handful of
// timestamps, and the near-zero intervals between them drag the rolling median
// far above the real tempo.
//
// Seen on hardware: three seconds after a replug the dashcam reported 223.3
// BPM against a true 120. scripts/midi-probe.py drains for exactly this reason.
func TestReaderDropsTheBacklogItFindsOnOpen(t *testing.T) {
	cards, snd, fifo := fifoFixture(t)
	clock := NewClock(10000)

	r := NewReader("EP-136", clock)
	r.CardsPath, r.SndDir = cards, snd
	r.DrainWindow = 300 * time.Millisecond
	r.Start()
	defer r.Stop()

	w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open write end: %v", err)
	}
	defer w.Close()
	waitFor(t, "the reader to connect", r.Connected)

	// A backlog: 200 pulses in one write, so they all share an arrival time.
	burst := make([]byte, 200)
	for i := range burst {
		burst[i] = ClockByte
	}
	if _, err := w.Write(burst); err != nil {
		t.Fatalf("write burst: %v", err)
	}

	time.Sleep(500 * time.Millisecond) // past the drain window
	if n := clock.Pulses(); n != 0 {
		t.Errorf("Pulses = %d after a backlog burst, want 0 (it must be dropped)", n)
	}

	// Live pulses after the window are counted normally.
	start := time.Now()
	for i := 0; i < 120; i++ {
		w.Write([]byte{ClockByte})
		time.Sleep(5 * time.Millisecond)
	}
	waitFor(t, "live pulses", func() bool { return clock.Pulses() >= 120 })

	got, ok := clock.BPM(start.Add(-time.Second), time.Now())
	if !ok {
		t.Fatal("BPM refused after the drain window")
	}
	// 5ms spacing is 500 BPM. With the burst counted this reads far higher.
	if math.Abs(got-500) > 150 {
		t.Errorf("BPM = %.1f, want roughly 500; the backlog is still being counted", got)
	}
}

// The drain must not swallow a device that is simply quiet at first.
func TestReaderCountsPulsesArrivingAfterTheDrainWindow(t *testing.T) {
	cards, snd, fifo := fifoFixture(t)
	clock := NewClock(10000)

	r := NewReader("EP-136", clock)
	r.CardsPath, r.SndDir = cards, snd
	r.DrainWindow = 50 * time.Millisecond
	r.Start()
	defer r.Stop()

	w, err := os.OpenFile(fifo, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open write end: %v", err)
	}
	defer w.Close()
	waitFor(t, "the reader to connect", r.Connected)

	time.Sleep(120 * time.Millisecond)
	for i := 0; i < 60; i++ {
		w.Write([]byte{ClockByte})
		time.Sleep(2 * time.Millisecond)
	}
	waitFor(t, "60 pulses", func() bool { return clock.Pulses() >= 60 })
}
