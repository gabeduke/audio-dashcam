package midi

import "time"

// FixedClock reports one tempo, always. It stands in for a real MIDI clock in
// demo mode: without it the tempo tile reads "–" and a screenshot shows three
// working stats and one dead one.
type FixedClock struct{ bpm float64 }

func NewFixedClock(bpm float64) *FixedClock { return &FixedClock{bpm: bpm} }

func (c *FixedClock) Connected() bool { return true }

func (c *FixedClock) Device() string { return "demo clock" }

func (c *FixedClock) BPM(_, _ time.Time) (float64, bool) { return c.bpm, true }
