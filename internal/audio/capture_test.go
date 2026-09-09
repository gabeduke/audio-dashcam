package audio

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gabeduke/hindsight/internal/config"
)

// testConfig is deliberately tiny: the ring is allocated eagerly, and these
// tests care about the supervisor's control flow, not about audio.
func testConfig() *config.Config {
	return &config.Config{
		Channels:     2,
		SampleRate:   8000,
		FramesPerBuf: 64,
		RingSeconds:  1,
		SaveChannels: []int{0, 1},
	}
}

// fakeSource is a Source whose behaviour each test dictates. It records what
// the supervisor did to it, which is the whole point: the supervision loop has
// no return value to assert on.
type fakeSource struct {
	// openErrs are returned by successive Opens, one per call, until
	// exhausted; a nil entry (or running past the end) means success.
	openErrs []error

	// silent makes a successful Open deliver nothing, which is how a stalled
	// device looks to the watchdog.
	silent bool

	mu        sync.Mutex
	opens     int
	closes    int
	resets    int
	shutdowns int
	sink      func([]int32)
	stopFeed  chan struct{}
}

func (f *fakeSource) Open(sink func([]int32)) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.opens++
	if len(f.openErrs) > 0 {
		err := f.openErrs[0]
		f.openErrs = f.openErrs[1:]
		if err != nil {
			return "", err
		}
	}

	f.sink = sink
	if !f.silent {
		stop := make(chan struct{})
		f.stopFeed = stop
		block := make([]int32, 64*2) // FramesPerBuf * Channels
		go func() {
			t := time.NewTicker(20 * time.Millisecond)
			defer t.Stop()
			for {
				select {
				case <-stop:
					return
				case <-t.C:
					sink(block)
				}
			}
		}()
	}
	return "fake device", nil
}

func (f *fakeSource) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.closes++
	if f.stopFeed != nil {
		close(f.stopFeed)
		f.stopFeed = nil
	}
	f.sink = nil
}

func (f *fakeSource) Reset() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resets++
	return nil
}

func (f *fakeSource) Shutdown() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.shutdowns++
	return nil
}

func (f *fakeSource) counts() (opens, closes, resets, shutdowns int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.opens, f.closes, f.resets, f.shutdowns
}

// waitFor polls until cond holds, failing the test with what it was waiting
// for. Polling rather than sleeping keeps these tests honest on a slow
// machine without making them slow on a fast one.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

// A device that is busy or absent at start-up must not be fatal: the
// supervisor retries, and re-enumerates between attempts, which is what lets
// an interface plugged in after boot ever be found.
func TestSupervisorRetriesAfterAFailedOpen(t *testing.T) {
	src := &fakeSource{openErrs: []error{errors.New("device busy")}}
	c := NewCapture(testConfig(), src)

	if err := c.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer c.Stop()

	waitFor(t, 5*time.Second, func() bool { return c.LastError() != "" },
		"the failed open to reach LastError")
	if got := c.LastError(); got == "" {
		t.Error("LastError() is empty after a failed open")
	}

	waitFor(t, 10*time.Second, func() bool {
		opens, _, resets, _ := src.counts()
		return opens >= 2 && resets >= 1
	}, "a second Open, preceded by a Reset")

	waitFor(t, 5*time.Second, func() bool { return c.Healthy() },
		"the retry to bring capture up")

	if got := c.LastError(); got != "" {
		t.Errorf("LastError() = %q after a successful open, want it cleared", got)
	}
	if got := c.DeviceName(); got != "fake device" {
		t.Errorf("DeviceName() = %q, want %q", got, "fake device")
	}
}

// A stream that stops delivering without erroring is the failure this
// watchdog exists for: the device is gone but nothing said so. The supervisor
// must notice and rebuild it.
func TestSupervisorRestartsASilentSource(t *testing.T) {
	src := &fakeSource{silent: true}
	c := NewCapture(testConfig(), src)

	if err := c.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	defer c.Stop()

	waitFor(t, 5*time.Second, func() bool {
		opens, _, _, _ := src.counts()
		return opens >= 1
	}, "the first Open")

	// staleAfter is 2s and the watchdog ticks at 500ms, so the restart lands
	// within about 2.5s; 10s of headroom keeps this off a slow machine's back.
	waitFor(t, 10*time.Second, func() bool {
		opens, closes, _, _ := src.counts()
		return opens >= 2 && closes >= 1
	}, "the stalled source to be closed and reopened")

	// Deliberately not asserted: that LastError still explains the stall.
	// It does not. supervise clears lastErr and re-stamps lastCallback the
	// moment Open succeeds (capture.go, the "backoff = time.Second" block),
	// before the new stream has delivered anything -- so a source that opens
	// cleanly but never delivers reports Healthy with no error for most of
	// every restart cycle. That is pre-existing behaviour, not a regression
	// from the Source refactor, and fixing it is a product decision: it would
	// mean tracking stream-open time separately from last-callback time, so
	// the watchdog can still fire while Healthy stays false until a real
	// callback arrives.
}

// Stop must tear the source down in order and must not hang -- it runs on
// SIGTERM, so a deadlock here means systemd kills the process instead.
func TestStopClosesThenShutsDownTheSource(t *testing.T) {
	src := &fakeSource{}
	c := NewCapture(testConfig(), src)

	if err := c.Start(); err != nil {
		t.Fatalf("Start() error: %v", err)
	}
	waitFor(t, 5*time.Second, func() bool { return c.Healthy() }, "capture to come up")

	done := make(chan struct{})
	go func() {
		c.Stop()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Stop() did not return within 10s")
	}

	_, closes, _, shutdowns := src.counts()
	if closes < 1 {
		t.Errorf("Close() called %d times, want at least 1", closes)
	}
	if shutdowns != 1 {
		t.Errorf("Shutdown() called %d times, want exactly 1", shutdowns)
	}

	// Stop is documented as safe to call twice.
	c.Stop()
	_, _, _, shutdowns = src.counts()
	if shutdowns != 1 {
		t.Errorf("Shutdown() called %d times after a second Stop, want exactly 1", shutdowns)
	}
}
