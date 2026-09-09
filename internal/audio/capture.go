package audio

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gabeduke/hindsight/internal/config"
	"github.com/gordonklaus/portaudio"
)

// blockPoolSize bounds how much audio can be in flight between the PortAudio
// callback and the ring writer. At 2048 frames per block this is several
// seconds of slack, which is what lets a large Snapshot hold the ring lock
// without the callback ever blocking.
const blockPoolSize = 64

// staleAfter is how long without a callback before capture is declared dead.
const staleAfter = 2 * time.Second

// levelBinMillis is the width of one level bin. The envelope's capacity is
// derived from it, so the two cannot drift.
const levelBinMillis = 10

// Capture owns the audio device, the ring buffer and the level meters.
type Capture struct {
	cfg    *config.Config
	ring   *Ring
	levels *Levels
	env    *Envelope
	pa     *paLifecycle

	free   chan []int32
	filled chan []int32

	lastCallback atomic.Int64 // unix nanos
	xruns        atomic.Uint64
	healthy      atomic.Bool
	deviceName   atomic.Value // string
	lastErr      atomic.Value // string

	mu     sync.Mutex
	stream *portaudio.Stream

	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

func NewCapture(cfg *config.Config) *Capture {
	c := &Capture{
		cfg:    cfg,
		ring:   NewRing(cfg.RingFrames(), cfg.Channels),
		levels: NewLevels(cfg.Channels, cfg.SampleRate, levelBinMillis),
		pa:     newPALifecycle(portaudio.Initialize, portaudio.Terminate),
		free:   make(chan []int32, blockPoolSize),
		filled: make(chan []int32, blockPoolSize),
		stop:   make(chan struct{}),
	}
	blockLen := cfg.FramesPerBuf * cfg.Channels
	for i := 0; i < blockPoolSize; i++ {
		c.free <- make([]int32, blockLen)
	}
	// Capacity comes from the same RingSeconds as the audio ring, so the
	// ribbon's timeline cannot drift from what Capture would actually write.
	// SaveChannels is what the meters and the takes use, so the ribbon shows
	// the same pair even under SAVE_ALL_CHANNELS.
	c.env = NewEnvelope(cfg.RingSeconds*1000/levelBinMillis, cfg.SaveChannels, levelBinMillis)
	c.levels.SetEnvelope(c.env)
	c.deviceName.Store("")
	c.lastErr.Store("")
	return c
}

func (c *Capture) Ring() *Ring     { return c.ring }
func (c *Capture) Levels() *Levels { return c.levels }

func (c *Capture) Envelope() *Envelope { return c.env }

func (c *Capture) Healthy() bool {
	if !c.healthy.Load() {
		return false
	}
	last := c.lastCallback.Load()
	return last != 0 && time.Since(time.Unix(0, last)) < staleAfter
}

func (c *Capture) XRuns() uint64      { return c.xruns.Load() }
func (c *Capture) DeviceName() string { s, _ := c.deviceName.Load().(string); return s }
func (c *Capture) LastError() string  { s, _ := c.lastErr.Load().(string); return s }

// BufferedSeconds reports how much audio is currently retrievable.
func (c *Capture) BufferedSeconds() float64 {
	return float64(c.ring.BufferedFrames()) / float64(c.cfg.SampleRate)
}

// Start brings up the ring writer, the level broadcaster and the supervised
// audio stream. It returns immediately; use Healthy to observe state.
func (c *Capture) Start() error {
	if err := c.pa.Init(); err != nil {
		return err
	}

	c.wg.Add(3)
	go c.ringWriter()
	go c.broadcaster()
	go c.supervise()
	return nil
}

// ringWriter is the single owner of the ring buffer.
func (c *Capture) ringWriter() {
	defer c.wg.Done()
	for {
		select {
		case <-c.stop:
			return
		case block := <-c.filled:
			c.ring.WriteFrames(block)
			select {
			case c.free <- block:
			default:
			}
		}
	}
}

// broadcaster pushes level frames to websocket subscribers at ~25fps.
func (c *Capture) broadcaster() {
	defer c.wg.Done()
	t := time.NewTicker(40 * time.Millisecond)
	defer t.Stop()
	for {
		select {
		case <-c.stop:
			return
		case <-t.C:
			c.levels.Broadcast()
		}
	}
}

// supervise keeps a live stream open, rebuilding it when it dies. The previous
// implementation latched an isRecording flag that nothing ever cleared, so a
// dead stream was never restarted and the UI reported "Active" forever.
func (c *Capture) supervise() {
	defer c.wg.Done()

	backoff := time.Second
	for {
		select {
		case <-c.stop:
			c.closeStream()
			return
		default:
		}

		if err := c.openStream(); err != nil {
			c.lastErr.Store(err.Error())
			c.healthy.Store(false)
			log.Printf("[!] capture: %v (retry in %s)", err, backoff)
			select {
			case <-c.stop:
				return
			case <-time.After(backoff):
			}
			if backoff < 15*time.Second {
				backoff *= 2
			}
			// PortAudio's device list is frozen at Pa_Initialize, so an
			// interface that was power-cycled is invisible until it is rebuilt.
			// Safe here because the open failed: openStream never leaves a
			// stream live on any of its error paths.
			if err := c.pa.Rescan(); err != nil {
				log.Printf("[!] portaudio rescan: %v", err)
			}
			continue
		}

		backoff = time.Second
		c.lastErr.Store("")
		c.healthy.Store(true)
		log.Printf("[*] capture live on %q — ring %ds, %d ch @ %d Hz",
			c.DeviceName(), c.cfg.RingSeconds, c.cfg.Channels, c.cfg.SampleRate)

		// Watch for the stream going stale.
		tick := time.NewTicker(500 * time.Millisecond)
		for alive := true; alive; {
			select {
			case <-c.stop:
				tick.Stop()
				c.closeStream()
				return
			case <-tick.C:
				last := c.lastCallback.Load()
				if last != 0 && time.Since(time.Unix(0, last)) > staleAfter {
					log.Printf("[!] capture stalled (no callback for %s) — restarting", staleAfter)
					c.lastErr.Store("audio stream stalled; restarting")
					c.healthy.Store(false)
					alive = false
				}
			}
		}
		tick.Stop()
		c.closeStream()
	}
}

func (c *Capture) openStream() error {
	dev, err := c.pickDevice()
	if err != nil {
		return err
	}

	p := portaudio.HighLatencyParameters(dev, nil)
	p.Input.Channels = c.cfg.Channels
	p.SampleRate = float64(c.cfg.SampleRate)
	p.FramesPerBuffer = c.cfg.FramesPerBuf
	// An explicit, generous latency is the fix for the busy-poll that pegged a
	// core: LowLatencyParameters asks a USB device for a deadline it cannot
	// meet, so PortAudio spins. A ring buffer has no latency requirement.
	p.Input.Latency = time.Duration(c.cfg.InputLatencyMS) * time.Millisecond

	stream, err := portaudio.OpenStream(p, c.processAudio)
	if err != nil {
		return fmt.Errorf("open %q: %w", dev.Name, err)
	}
	if err := stream.Start(); err != nil {
		stream.Close()
		return fmt.Errorf("start %q: %w", dev.Name, err)
	}

	c.mu.Lock()
	c.stream = stream
	c.mu.Unlock()

	c.deviceName.Store(dev.Name)
	c.lastCallback.Store(time.Now().UnixNano())
	return nil
}

func (c *Capture) closeStream() {
	c.mu.Lock()
	s := c.stream
	c.stream = nil
	c.mu.Unlock()

	if s != nil {
		_ = s.Stop()
		_ = s.Close()
	}
	c.healthy.Store(false)
}

// pickDevice selects the input deterministically. ALSA exposes the same card
// under several PortAudio names (hw, plughw, default, sysdefault, front,
// dsnoop); the plug-based ones can silently add format conversion, so prefer a
// direct one. The old code kept the *last* match, which was arbitrary.
func (c *Capture) pickDevice() (*portaudio.DeviceInfo, error) {
	devices, err := portaudio.Devices()
	if err != nil {
		return nil, fmt.Errorf("enumerate devices: %w", err)
	}

	var match, fallback *portaudio.DeviceInfo
	bestScore := -1

	for _, d := range devices {
		if d.MaxInputChannels < c.cfg.Channels {
			continue
		}
		if fallback == nil {
			fallback = d
		}
		if c.cfg.DeviceMatch == "" || !strings.Contains(d.Name, c.cfg.DeviceMatch) {
			continue
		}
		if s := deviceScore(d.Name); s > bestScore {
			bestScore, match = s, d
		}
	}

	if match != nil {
		return match, nil
	}
	if fallback != nil {
		log.Printf("[!] no input matching %q with >=%d channels; falling back to %q",
			c.cfg.DeviceMatch, c.cfg.Channels, fallback.Name)
		return fallback, nil
	}
	return nil, fmt.Errorf("no input device with >=%d channels (is it in use by another process?)", c.cfg.Channels)
}

func deviceScore(name string) int {
	n := strings.ToLower(name)
	switch {
	case strings.Contains(n, "dsnoop"), strings.Contains(n, "plughw"):
		return 0
	case strings.Contains(n, "sysdefault"), strings.Contains(n, "default"):
		return 1
	case strings.Contains(n, "front"):
		return 2
	default:
		return 3 // bare "EP-136: USB Audio (hw:2,0)" style
	}
}

// processAudio runs on the PortAudio thread. It must never block or allocate,
// so it takes a pooled block, copies into it, and hands it off without waiting.
func (c *Capture) processAudio(in []int32) {
	c.lastCallback.Store(time.Now().UnixNano())
	c.levels.Accumulate(in)

	select {
	case block := <-c.free:
		block = block[:cap(block)]
		if len(block) < len(in) {
			block = make([]int32, len(in))
		}
		n := copy(block, in)
		select {
		case c.filled <- block[:n]:
		default:
			c.xruns.Add(1)
			select {
			case c.free <- block:
			default:
			}
		}
	default:
		// Ring writer is behind; drop this block rather than stall the device.
		c.xruns.Add(1)
	}
}

// Stop tears everything down.
func (c *Capture) Stop() {
	c.stopOnce.Do(func() {
		close(c.stop)
		c.wg.Wait()
		c.closeStream()
		_ = c.pa.Term()
	})
}
