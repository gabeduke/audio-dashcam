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

// readBuf is one read's worth of bytes. The EP emits about 62 a second, so this
// is never close to full; it exists so a burst after a stall is taken in one
// syscall rather than 64.
const readBuf = 64

// defaultDrainWindow is how long after opening the device its bytes are thrown
// away instead of timestamped.
//
// ALSA starts buffering clock the moment the device node appears, and this
// reader can be a whole RetryDelay behind that. The backlog is then handed over
// in the first read or two, so those pulses share a handful of arrival times,
// and the near-zero intervals between them drag the rolling median far above
// the real tempo. Measured on hardware: three seconds after a replug the
// dashcam reported 223.3 BPM against a true 120.
//
// scripts/midi-probe.py drains for exactly this reason, and its comments
// record the same failure in an ad-hoc reader that over-reported by 10 BPM.
//
// 150ms is generous: a full 4KB rawmidi buffer is handed over in microseconds,
// while the cost is six real pulses at 120 BPM, out of a 900-second ring.
const defaultDrainWindow = 150 * time.Millisecond

// Reader owns the rawmidi device: discovery, opening, reading, timestamping,
// and recovery when the interface disappears.
//
// It mirrors Capture.supervise's shape deliberately -- detect, back off,
// rediscover, reopen -- because the failure it handles is the same one: the
// interface being unplugged. What it does not share is any path back into the
// capture thread. Every error here is logged and dropped. The dashcam's job is
// audio; a MIDI failure produces a take with no BPM and nothing else.
type Reader struct {
	// CardsPath and SndDir default to the real ALSA locations and are fields so
	// tests can point them at a fixture directory.
	CardsPath string
	SndDir    string
	// RetryDelay is the pause between attempts to find and open the device. The
	// default is deliberately unhurried: a missing EP is the normal state, not
	// an outage to race back from.
	RetryDelay time.Duration
	// DrainWindow is how long after opening the device its bytes are discarded
	// as backlog rather than timestamped. See readLoop.
	DrainWindow time.Duration

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
		CardsPath:   DefaultCardsPath,
		SndDir:      DefaultSndDir,
		RetryDelay:  4 * time.Second,
		DrainWindow: defaultDrainWindow,
		match:       match,
		clock:       clock,
		stop:        make(chan struct{}),
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
// shutdown, where a goroutine parked in a read costs nothing -- whereas waiting
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

// readLoop timestamps at arrival, one time.Now() per read rather than per byte.
// Batch-then-timestamp cannot place a beat against an audio frame, and the
// estimator refuses outright once most pulses stop carrying distinct arrival
// times -- so a read that returns as its bytes land is a correctness
// requirement, not an optimisation.
func (r *Reader) readLoop(f *os.File) {
	buf := make([]byte, readBuf)
	var sawTransport bool

	opened := time.Now()
	drainUntil := opened.Add(r.DrainWindow)
	dropped := 0
	draining := r.DrainWindow > 0

	for {
		n, err := f.Read(buf)
		now := time.Now()

		if draining {
			if now.Before(drainUntil) {
				dropped += n
				n = 0
			} else {
				draining = false
				if dropped > 0 {
					log.Printf("[*] midi: dropped %d backlog bytes buffered before the reader opened", dropped)
				}
			}
		}

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
