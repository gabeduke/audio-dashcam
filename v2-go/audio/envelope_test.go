package audio

import "testing"

// binAt builds a one-channel Bin whose peak magnitude is amp.
func binAt(amp float32) Bin {
	return Bin{Min: []float32{-amp}, Max: []float32{amp}, RMS: []float32{0}}
}

func TestEnvelopeCodesDBOnTheMeterScale(t *testing.T) {
	e := NewEnvelope(10, []int{0}, 10)
	// 0.25 is -12.04 dBFS. meter.js maps that to (db+60)/60; scaled to 0..255
	// that is round(47.96 * 4.25) = 204.
	e.PushBin(binAt(0.25))
	if got := e.Buckets(1)[0]; got != 204 {
		t.Fatalf("byte = %d, want 204 (-12.04 dBFS on the 0..255 dB scale)", got)
	}
}

func TestEnvelopeCodesSilenceAndSubFloorAsZero(t *testing.T) {
	e := NewEnvelope(10, []int{0}, 10)
	e.PushBin(binAt(0))
	if got := e.Buckets(1)[0]; got != 0 {
		t.Fatalf("digital silence coded as %d, want 0", got)
	}

	e2 := NewEnvelope(10, []int{0}, 10)
	e2.PushBin(binAt(0.0005)) // about -66 dBFS, below FloorDB
	if got := e2.Buckets(1)[0]; got != 0 {
		t.Fatalf("sub-floor coded as %d, want 0", got)
	}
}

func TestEnvelopeTakesThePeakAcrossSaveChannels(t *testing.T) {
	// Channel 1 is louder, and only channels 0 and 1 are saved, so channel 2
	// being louder still must not show up.
	e := NewEnvelope(10, []int{0, 1}, 10)
	e.PushBin(Bin{
		Min: []float32{-0.01, -0.25, -0.9},
		Max: []float32{0.01, 0.25, 0.9},
		RMS: []float32{0, 0, 0},
	})
	if got := e.Buckets(1)[0]; got != 204 {
		t.Fatalf("byte = %d, want 204 (channel 1 at 0.25, channel 2 not saved)", got)
	}
}

func TestEnvelopeUsesNegativePeaksToo(t *testing.T) {
	// A waveform can be asymmetric; the magnitude is what matters.
	e := NewEnvelope(10, []int{0}, 10)
	e.PushBin(Bin{Min: []float32{-0.25}, Max: []float32{0.001}, RMS: []float32{0}})
	if got := e.Buckets(1)[0]; got != 204 {
		t.Fatalf("byte = %d, want 204 from the negative peak", got)
	}
}

func TestEnvelopeBufferedGrowsThenSaturates(t *testing.T) {
	e := NewEnvelope(10, []int{0}, 10) // 10 bins x 10ms = 0.1s capacity
	if got := e.BufferedSeconds(); got != 0 {
		t.Fatalf("fresh envelope buffered %v, want 0", got)
	}
	for i := 0; i < 4; i++ {
		e.PushBin(binAt(0.25))
	}
	if got := e.BufferedSeconds(); got < 0.039 || got > 0.041 {
		t.Fatalf("buffered = %v, want 0.04 after 4 bins", got)
	}
	for i := 0; i < 50; i++ {
		e.PushBin(binAt(0.25))
	}
	if got := e.BufferedSeconds(); got < 0.099 || got > 0.101 {
		t.Fatalf("buffered = %v, want 0.1 once wrapped", got)
	}
	if got := e.RingSeconds(); got < 0.099 || got > 0.101 {
		t.Fatalf("ring = %v, want 0.1", got)
	}
}

// pushRamp fills the envelope with n bins whose amplitude climbs steadily, so
// the newest audio is the loudest and any ordering mistake is obvious.
func pushRamp(e *Envelope, n int) {
	for i := 0; i < n; i++ {
		e.PushBin(binAt(float32(0.002 + 0.9*float64(i)/float64(n))))
	}
}

func TestBucketsRunOldestFirst(t *testing.T) {
	e := NewEnvelope(1000, []int{0}, 10)
	pushRamp(e, 1000)

	b := e.Buckets(20)
	if len(b) != 20 {
		t.Fatalf("got %d buckets, want 20", len(b))
	}
	if b[0] >= b[len(b)-1] {
		t.Fatalf("buckets are not oldest-first: first=%d last=%d", b[0], b[len(b)-1])
	}
}

func TestBucketsTakeThePeakNotTheMean(t *testing.T) {
	// One loud bin in an otherwise quiet stretch must survive into its bucket.
	// A mean over ~50 bins would bury it.
	e := NewEnvelope(1000, []int{0}, 10)
	for i := 0; i < 1000; i++ {
		if i == 100 { // 900 bins back from the head, i.e. 9s old
			e.PushBin(binAt(0.5))
			continue
		}
		e.PushBin(binAt(0.002))
	}

	b := e.Buckets(20)
	var max byte
	for _, v := range b {
		if v > max {
			max = v
		}
	}
	// 0.5 is -6.02 dBFS -> round(53.98 * 4.25) = 229.
	if max != 229 {
		t.Fatalf("loudest bucket = %d, want 229; a peak was averaged away", max)
	}
}

func TestNewestBucketReachesAgeZero(t *testing.T) {
	// Ages below EdgeSeconds must still land at the right edge, or audio
	// arriving right now is in no bucket at all.
	e := NewEnvelope(90000, []int{0}, 10) // 900s
	for i := 0; i < 90000; i++ {
		e.PushBin(binAt(0.002))
	}
	e.PushBin(binAt(0.5)) // newest bin, ~0s old

	b := e.Buckets(400)
	if b[len(b)-1] != 229 {
		t.Fatalf("newest bucket = %d, want 229; the freshest audio never reached the edge", b[len(b)-1])
	}
}

func TestBucketsAreZeroBeyondWhatIsBuffered(t *testing.T) {
	// A part-filled ring must not report its unwritten region as silence --
	// the client draws that as a hatch, and it can only tell them apart from
	// buffered_seconds.
	e := NewEnvelope(90000, []int{0}, 10) // 900s capacity
	for i := 0; i < 3000; i++ {           // only 30s written
		e.PushBin(binAt(0.5))
	}

	b := e.Buckets(400)
	if b[0] != 0 {
		t.Fatalf("oldest bucket = %d, want 0 for never-written time", b[0])
	}
	if b[len(b)-1] == 0 {
		t.Fatalf("newest bucket is 0, but 30s of loud audio was written")
	}
}

func TestBucketsClampToAtLeastOne(t *testing.T) {
	e := NewEnvelope(100, []int{0}, 10)
	pushRamp(e, 100)
	if got := len(e.Buckets(0)); got != 1 {
		t.Fatalf("Buckets(0) returned %d buckets, want 1", got)
	}
	if got := len(e.Buckets(-5)); got != 1 {
		t.Fatalf("Buckets(-5) returned %d buckets, want 1", got)
	}
}

func TestNewEnvelopeClampsCapBinsAndBinMillis(t *testing.T) {
	e := NewEnvelope(0, []int{0}, 10) // capBins clamps to 1
	if got := e.RingSeconds(); got < 0.0099 || got > 0.0101 {
		t.Fatalf("capBins=0 gave ring %v, want ~0.01 (1 bin x 10ms)", got)
	}

	e2 := NewEnvelope(10, []int{0}, 0) // binMillis clamps to 1
	if got := e2.RingSeconds(); got < 0.0099 || got > 0.0101 {
		t.Fatalf("binMillis=0 gave ring %v, want ~0.01 (10 bins x 1ms)", got)
	}
}

func TestCodeDBClampsOverFullScale(t *testing.T) {
	if got := codeDB(1.5); got != 255 {
		t.Fatalf("codeDB(1.5) = %d, want 255 (over full scale still clamps to the top)", got)
	}
}

func TestNewEnvelopeCopiesSaveChannels(t *testing.T) {
	ch := []int{0}
	e := NewEnvelope(10, ch, 10)
	ch[0] = 1 // mutate the caller's slice after construction

	// Channel 0 is loud, channel 1 is quiet. If the envelope aliased the
	// caller's slice, PushBin would now read channel 1 instead.
	e.PushBin(Bin{
		Min: []float32{-0.9, -0.25},
		Max: []float32{0.9, 0.25},
		RMS: []float32{0, 0},
	})
	// 0.9 is -0.92 dBFS -> round(59.08 * 4.25) = 251.
	if got := e.Buckets(1)[0]; got != 251 {
		t.Fatalf("byte = %d, want 251; NewEnvelope aliased the caller's saveChannels slice", got)
	}
}

// TestEnvelopeSurvivesConcurrentPushAndRead exercises the one property a
// single-goroutine -race run can never check: PushBin (the audio callback
// thread) and Buckets/BufferedSeconds/RingSeconds (an HTTP handler) share
// e.mu, and only Buckets's copy-then-unlock structure keeps that safe.
func TestEnvelopeSurvivesConcurrentPushAndRead(t *testing.T) {
	e := NewEnvelope(5000, []int{0}, 10)

	const iterations = 20000
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < iterations; i++ {
			e.PushBin(binAt(float32(0.002 + 0.5*float64(i%997)/997)))
		}
	}()

	for i := 0; i < 300; i++ {
		e.Buckets(400)
		e.BufferedSeconds()
		e.RingSeconds()
	}
	<-done
}

// TestBucketsNewestBucketNeverCollapsesAtHighBucketCounts guards against a
// regression in an earlier draft: boundaries were carried forward from one
// bucket to the next and clamped to "shrink by at least one bin" whenever the
// formula's own rounding did not move. At n=5000 on a 900s/10ms ring, that
// budget hit zero around bucket 4268 -- 700+ buckets, including the newest
// one, before the loop finished -- and every one of them silently reported 0
// instead of real audio. Each boundary must be computed independently.
func TestBucketsNewestBucketNeverCollapsesAtHighBucketCounts(t *testing.T) {
	e := NewEnvelope(90000, []int{0}, 10) // 900s
	for i := 0; i < 90000; i++ {
		e.PushBin(binAt(0.002))
	}
	e.PushBin(binAt(0.5)) // newest bin, ~0s old

	for _, n := range []int{1, 2, 20, 400, 1000, 5000, 90000} {
		b := e.Buckets(n)
		if len(b) != n {
			t.Fatalf("n=%d: got %d buckets, want %d", n, len(b), n)
		}
		if b[len(b)-1] != 229 {
			t.Fatalf("n=%d: newest bucket = %d, want 229; the edge collapsed before the last bucket", n, b[len(b)-1])
		}
	}
}
