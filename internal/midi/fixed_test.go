package midi

import (
	"testing"
	"time"

	"github.com/gabeduke/hindsight/internal/api"
	"github.com/gabeduke/hindsight/internal/audio"
)

func TestFixedClockSatisfiesBothConsumers(t *testing.T) {
	var _ api.MIDISource = NewFixedClock(96)
	var _ audio.TempoSource = NewFixedClock(96)
}

func TestFixedClockAlwaysReports(t *testing.T) {
	c := NewFixedClock(96)

	if !c.Connected() {
		t.Error("Connected() = false; the demo clock is always present")
	}
	bpm, ok := c.BPM(time.Now().Add(-time.Minute), time.Now())
	if !ok {
		t.Fatal("BPM() reported no reading")
	}
	if bpm != 96 {
		t.Errorf("BPM() = %v, want 96", bpm)
	}
}
