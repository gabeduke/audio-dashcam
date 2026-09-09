// Package config holds the runtime configuration for the audio dashcam,
// sourced entirely from environment variables so the buffer length and
// channel routing can change without a rebuild.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	// Capture
	DeviceMatch    string // substring used to pick the PortAudio input device
	Channels       int    // channels to capture from the device
	SampleRate     int
	FramesPerBuf   int
	InputLatencyMS int // explicit latency; low values make PortAudio busy-poll

	// Ring buffer
	RingSeconds int

	// Saving
	OutputDir       string
	SaveChannels    []int // zero-based indices of the pair written to a take
	SaveAllChannels bool
	MinFreeGB       float64
	MaxSaves        int // 0 = unlimited

	// Server
	Port string
}

func Load() (*Config, error) {
	home, _ := os.UserHomeDir()

	c := &Config{
		DeviceMatch:     env("DEVICE_MATCH", "EP-136"),
		Channels:        envInt("CHANNELS", 8),
		SampleRate:      envInt("SAMPLE_RATE", 48000),
		FramesPerBuf:    envInt("FRAMES_PER_BUFFER", 2048),
		InputLatencyMS:  envInt("INPUT_LATENCY_MS", 100),
		RingSeconds:     envInt("RING_SECONDS", 900),
		OutputDir:       env("OUTPUT_DIR", filepath.Join(home, "audio-dashcam", "jam_saves")),
		SaveAllChannels: envBool("SAVE_ALL_CHANNELS", false),
		MinFreeGB:       envFloat("MIN_FREE_GB", 1.0),
		MaxSaves:        envInt("MAX_SAVES", 0),
		Port:            env("PORT", "5000"),
	}

	// SAVE_CHANNELS is 1-indexed in the environment because that is how the
	// hardware labels them; store zero-based. Default 3,4 was measured from a
	// real capture: channels 1/2 carry only bleed, 5-8 are digital silence.
	ch, err := parseChannels(env("SAVE_CHANNELS", "3,4"), c.Channels)
	if err != nil {
		return nil, err
	}
	c.SaveChannels = ch

	if c.RingSeconds < 1 {
		return nil, fmt.Errorf("RING_SECONDS must be >= 1, got %d", c.RingSeconds)
	}
	if c.Channels < 1 {
		return nil, fmt.Errorf("CHANNELS must be >= 1, got %d", c.Channels)
	}
	return c, nil
}

// RingFrames is the ring capacity in frames (one frame = one sample per channel).
func (c *Config) RingFrames() int { return c.RingSeconds * c.SampleRate }

// OutChannels returns the zero-based channel indices actually written to a take.
func (c *Config) OutChannels() []int {
	if c.SaveAllChannels {
		all := make([]int, c.Channels)
		for i := range all {
			all[i] = i
		}
		return all
	}
	return c.SaveChannels
}

func (c *Config) String() string {
	disp := make([]int, len(c.SaveChannels))
	for i, v := range c.SaveChannels {
		disp[i] = v + 1
	}
	return fmt.Sprintf(
		"device=%q channels=%d rate=%d frames/buf=%d latency=%dms ring=%ds (%d frames, %s) "+
			"save_channels=%v save_all=%t min_free=%.1fGB max_saves=%d out=%s",
		c.DeviceMatch, c.Channels, c.SampleRate, c.FramesPerBuf, c.InputLatencyMS,
		c.RingSeconds, c.RingFrames(), humanBytes(int64(c.RingFrames())*int64(c.Channels)*4),
		disp, c.SaveAllChannels, c.MinFreeGB, c.MaxSaves, c.OutputDir,
	)
}

func parseChannels(s string, max int) ([]int, error) {
	parts := strings.Split(s, ",")
	out := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			return nil, fmt.Errorf("SAVE_CHANNELS: %q is not a number", p)
		}
		if n < 1 || n > max {
			return nil, fmt.Errorf("SAVE_CHANNELS: channel %d out of range 1..%d", n, max)
		}
		out = append(out, n-1)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("SAVE_CHANNELS: no channels given")
	}
	return out, nil
}

func humanBytes(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%dB", n)
	}
	div, exp := int64(u), 0
	for m := n / u; m >= u; m /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f%cB", float64(n)/float64(div), "KMGT"[exp])
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v := os.Getenv(k); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func envFloat(k string, def float64) float64 {
	if v := os.Getenv(k); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func envBool(k string, def bool) bool {
	if v := os.Getenv(k); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return def
}
