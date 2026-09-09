package audio

import (
	"log"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gabeduke/hindsight/internal/config"
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
	src    Source
	ring   *Ring
	levels *Levels
	env    *Envelope

	free   chan []int32
	filled chan []int32

	lastCallback atomic.Int64 // unix nanos
	xruns        atomic.Uint64
	healthy      atomic.Bool
	deviceName   atomic.Value // string
	lastErr      atomic.Value // string

	stop     chan struct{}
	stopOnce sync.Once
	wg       sync.WaitGroup
}

func NewCapture(cfg *config.Config, src Source) *Capture {
	c := &Capture{
		cfg:    cfg,
		src:    src,
		ring:   NewRing(cfg.RingFrames(), cfg.Channels),
		levels: NewLevels(cfg.Channels, cfg.SampleRate, levelBinMillis),
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
			c.src.Close()
			c.healthy.Store(false)
			return
		default:
		}

		name, err := c.src.Open(c.processAudio)
		if err != nil {
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
			if err := c.src.Reset(); err != nil {
				log.Printf("[!] device rescan: %v", err)
			}
			continue
		}

		backoff = time.Second
		c.lastErr.Store("")
		c.deviceName.Store(name)
		c.lastCallback.Store(time.Now().UnixNano())
		c.healthy.Store(true)
		log.Printf("[*] capture live on %q — ring %ds, %d ch @ %d Hz",
			c.DeviceName(), c.cfg.RingSeconds, c.cfg.Channels, c.cfg.SampleRate)

		// Watch for the stream going stale.
		tick := time.NewTicker(500 * time.Millisecond)
		for alive := true; alive; {
			select {
			case <-c.stop:
				tick.Stop()
				c.src.Close()
				c.healthy.Store(false)
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
		c.src.Close()
		c.healthy.Store(false)
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
		c.src.Close()
		_ = c.src.Shutdown()
	})
}
