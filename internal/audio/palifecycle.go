package audio

import (
	"fmt"
	"sync"
)

// paLifecycle owns PortAudio's process-global initialise/terminate pair.
//
// PortAudio enumerates devices exactly once, inside Pa_Initialize, and never
// rescans. An interface that is powered off therefore stays in the device list
// forever, pointing at an ALSA card index that no longer exists, and every
// open attempt fails with "Illegal combination of I/O devices". Terminating
// and initialising again is the only way to see hardware that appeared or
// disappeared after start-up, which is what Rescan is for.
type paLifecycle struct {
	mu     sync.Mutex
	up     bool
	initFn func() error
	termFn func() error
}

func newPALifecycle(initFn, termFn func() error) *paLifecycle {
	return &paLifecycle{initFn: initFn, termFn: termFn}
}

// Init brings PortAudio up. Calling it when it is already up is a no-op, so no
// caller has to track state to stay safe.
func (p *paLifecycle) Init() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.up {
		return nil
	}
	if err := p.initFn(); err != nil {
		// Deliberately stay "down" so the next attempt really retries.
		return fmt.Errorf("portaudio init: %w", err)
	}
	p.up = true
	return nil
}

// Term takes PortAudio down. Terminating twice is a double free in the C
// library, so a second call is deliberately a no-op.
func (p *paLifecycle) Term() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.up {
		return nil
	}
	// Marked down before the call: if Pa_Terminate fails there is nothing
	// useful to do with a half-torn-down library except initialise it again,
	// and latching "up" would block exactly that.
	p.up = false
	if err := p.termFn(); err != nil {
		return fmt.Errorf("portaudio terminate: %w", err)
	}
	return nil
}

// Rescan forces PortAudio to re-enumerate devices, which is what picks up an
// interface that was unplugged or power-cycled while the process was running.
func (p *paLifecycle) Rescan() error {
	if err := p.Term(); err != nil {
		return err
	}
	return p.Init()
}
