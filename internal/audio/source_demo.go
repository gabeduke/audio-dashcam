package audio

import (
	"math"
	"math/rand"
	"sync"
	"time"

	"github.com/gabeduke/hindsight/internal/config"
)

// DemoBPM is the tempo of the synthetic loop. It is exported because the demo
// also stands in for the MIDI clock, and the two must agree.
const DemoBPM = 96.0

// demoSource generates a loop that looks like music: a screenshot of a flat
// sine tells a reader nothing about what the meters, the ribbon or a take
// actually look like.
//
// Everything is derived from a monotonic sample counter rather than the clock,
// so the waveform is identical run to run and screenshots are reproducible.
type demoSource struct {
	cfg *config.Config

	// saved is a precomputed membership set built once from cfg.SaveChannels,
	// so fill does not rebuild it on every block. It is a pure lookup, never
	// iterated for order, so building it once changes nothing about the
	// output.
	saved map[int]bool

	// noise backs the per-block hat bursts. It is allocated once and reseeded
	// per block in fill (via Seed), rather than replaced, since fill runs on
	// a single goroutine at a time and reseeding to n produces the same
	// sequence a fresh rand.New(rand.NewSource(n)) would.
	noise *rand.Rand

	// Guards the channel pair rather than a sync.Once: Open must be able to
	// arm a fresh generator after Close, and re-assigning a sync.Once copies
	// a lock, which go vet rejects.
	mu   sync.Mutex
	stop chan struct{}
	done chan struct{}
}

func NewDemoSource(cfg *config.Config) Source {
	saved := make(map[int]bool, len(cfg.SaveChannels))
	for _, c := range cfg.SaveChannels {
		saved[c] = true
	}
	return &demoSource{
		cfg:   cfg,
		saved: saved,
		noise: rand.New(rand.NewSource(0)),
	}
}

func (s *demoSource) Open(sink func([]int32)) (string, error) {
	// A second Open without an intervening Close would orphan the previous
	// generator goroutine -- which would then be sharing this source's RNG
	// with the new one, and rand.Rand is not safe for concurrent use. The
	// production caller always closes first; this is belt and braces.
	s.Close()

	s.mu.Lock()
	s.stop = make(chan struct{})
	s.done = make(chan struct{})
	stop, done := s.stop, s.done
	s.mu.Unlock()

	block := make([]int32, s.cfg.FramesPerBuf*s.cfg.Channels)
	period := time.Duration(float64(s.cfg.FramesPerBuf) / float64(s.cfg.SampleRate) * float64(time.Second))

	go func() {
		defer close(done)
		t := time.NewTicker(period)
		defer t.Stop()

		var n int64 // frames generated so far
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				s.fill(block, n)
				n += int64(s.cfg.FramesPerBuf)
				sink(block)
			}
		}
	}()

	return "Demo signal generator (synthetic, 96 BPM)", nil
}

// Close stops the generator and waits for it, so no sink call is in flight
// when it returns. Calling it twice, or without Open, is a no-op.
func (s *demoSource) Close() {
	s.mu.Lock()
	stop, done := s.stop, s.done
	s.stop, s.done = nil, nil
	s.mu.Unlock()

	if stop == nil {
		return
	}
	close(stop)
	<-done
}

func (s *demoSource) Reset() error    { return nil }
func (s *demoSource) Shutdown() error { return nil }

// fill writes one interleaved block starting at absolute frame n.
func (s *demoSource) fill(block []int32, n int64) {
	const fullScale = 2147483648.0

	rate := float64(s.cfg.SampleRate)

	// Reseeding to n keeps the hats from being identical every bar while
	// staying deterministic for a given n -- the same sequence a fresh
	// rand.New(rand.NewSource(n)) would produce, without reallocating one
	// every block.
	s.noise.Seed(n)

	for i := 0; i < s.cfg.FramesPerBuf; i++ {
		t := float64(n+int64(i)) / rate
		beat := t * DemoBPM / 60.0

		// An 8-bar arc (32 beats) so the ribbon shows structure rather than a
		// uniform band.
		arc := 0.55 + 0.45*math.Sin(2*math.Pi*beat/32.0)

		// Kick on every beat: a decaying low sine.
		kb := beat - math.Floor(beat)
		kick := math.Exp(-9*kb) * math.Sin(2*math.Pi*55*t)

		// Hats on eighths: a short noise burst.
		hb := beat*2 - math.Floor(beat*2)
		hat := math.Exp(-45*hb) * (s.noise.Float64()*2 - 1) * 0.35

		// Bass: one note per bar, walking a minor pentatonic.
		bar := int(math.Floor(beat / 4))
		bassHz := []float64{82.41, 98.00, 110.00, 73.42}[bar%4]
		bass := 0.45 * math.Sin(2*math.Pi*bassHz*t)

		// Pad: a triad two octaves up, quiet enough to sit under everything.
		pad := 0.12 * (math.Sin(2*math.Pi*bassHz*4*t) +
			math.Sin(2*math.Pi*bassHz*4.75*t) +
			math.Sin(2*math.Pi*bassHz*6*t))

		mix := arc * (0.55*kick + hat + bass + pad)
		// Soft clip, then leave ~6 dB of headroom so nothing reads as pinned.
		mix = math.Tanh(mix) * 0.5

		for c := 0; c < s.cfg.Channels; c++ {
			v := mix
			if !s.saved[c] {
				// Bleed, as the real interface has on its unused pairs.
				v *= 0.03
			}
			block[i*s.cfg.Channels+c] = int32(v * (fullScale - 1))
		}
	}
}
