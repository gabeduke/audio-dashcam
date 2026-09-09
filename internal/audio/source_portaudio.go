//go:build cgo

package audio

import (
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/gabeduke/hindsight/internal/config"
	"github.com/gordonklaus/portaudio"
)

// deviceSource is the real audio interface, opened through PortAudio.
type deviceSource struct {
	cfg *config.Config
	pa  *paLifecycle

	mu     sync.Mutex
	stream *portaudio.Stream
}

func NewDeviceSource(cfg *config.Config) Source {
	return &deviceSource{
		cfg: cfg,
		pa:  newPALifecycle(portaudio.Initialize, portaudio.Terminate),
	}
}

func (s *deviceSource) Open(sink func([]int32)) (string, error) {
	// Init is idempotent, so opening repeatedly costs nothing and no caller
	// has to track whether PortAudio is up.
	if err := s.pa.Init(); err != nil {
		return "", err
	}

	dev, err := s.pickDevice()
	if err != nil {
		return "", err
	}

	p := portaudio.HighLatencyParameters(dev, nil)
	p.Input.Channels = s.cfg.Channels
	p.SampleRate = float64(s.cfg.SampleRate)
	p.FramesPerBuffer = s.cfg.FramesPerBuf
	// An explicit, generous latency is the fix for the busy-poll that pegged a
	// core: LowLatencyParameters asks a USB device for a deadline it cannot
	// meet, so PortAudio spins. A ring buffer has no latency requirement.
	p.Input.Latency = time.Duration(s.cfg.InputLatencyMS) * time.Millisecond

	stream, err := portaudio.OpenStream(p, sink)
	if err != nil {
		return "", fmt.Errorf("open %q: %w", dev.Name, err)
	}
	if err := stream.Start(); err != nil {
		stream.Close()
		return "", fmt.Errorf("start %q: %w", dev.Name, err)
	}

	s.mu.Lock()
	s.stream = stream
	s.mu.Unlock()

	return dev.Name, nil
}

func (s *deviceSource) Close() {
	s.mu.Lock()
	st := s.stream
	s.stream = nil
	s.mu.Unlock()

	if st != nil {
		_ = st.Stop()
		_ = st.Close()
	}
}

// Reset re-enumerates devices. PortAudio's device list is frozen at
// Pa_Initialize and never rescans, so an interface that was power-cycled is
// invisible until the library is torn down and brought back up.
func (s *deviceSource) Reset() error { return s.pa.Rescan() }

func (s *deviceSource) Shutdown() error { return s.pa.Term() }

// pickDevice selects the input deterministically. ALSA exposes the same card
// under several PortAudio names (hw, plughw, default, sysdefault, front,
// dsnoop); the plug-based ones can silently add format conversion, so prefer a
// direct one. The old code kept the *last* match, which was arbitrary.
func (s *deviceSource) pickDevice() (*portaudio.DeviceInfo, error) {
	devices, err := portaudio.Devices()
	if err != nil {
		return nil, fmt.Errorf("enumerate devices: %w", err)
	}

	var match, fallback *portaudio.DeviceInfo
	bestScore := -1

	for _, d := range devices {
		if d.MaxInputChannels < s.cfg.Channels {
			continue
		}
		if fallback == nil {
			fallback = d
		}
		if s.cfg.DeviceMatch == "" || !strings.Contains(d.Name, s.cfg.DeviceMatch) {
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
			s.cfg.DeviceMatch, s.cfg.Channels, fallback.Name)
		return fallback, nil
	}
	return nil, fmt.Errorf("no input device with >=%d channels (is it in use by another process?)", s.cfg.Channels)
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
