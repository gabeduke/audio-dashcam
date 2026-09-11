package audio

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestFadeFramesIsThreeMilliseconds(t *testing.T) {
	if got := FadeFrames(48000); got != 144 {
		t.Errorf("FadeFrames(48000) = %d, want 144", got)
	}
}

func TestApplyFadesRampsOnlyTheEdges(t *testing.T) {
	const total, fade = 1000, 10
	block := make([]int32, total*2)
	for i := range block {
		block[i] = 1000
	}
	applyFades(block, 0, total, 2, fade)
	// First frame silent, ramps up, plateau exact, ramps down, last frame near silent.
	if block[0] != 0 || block[1] != 0 {
		t.Errorf("frame 0 = %d,%d want 0,0", block[0], block[1])
	}
	if block[5*2] != 500 {
		t.Errorf("frame 5 = %d, want 500 (half way up)", block[5*2])
	}
	if block[fade*2] != 1000 || block[(total-fade-1)*2] != 1000 {
		t.Errorf("plateau touched: %d %d", block[fade*2], block[(total-fade-1)*2])
	}
	if block[(total-1)*2] != 100 {
		t.Errorf("last frame = %d, want 100 (one step above silence)", block[(total-1)*2])
	}
}

func TestApplyFadesWorksAcrossBlocks(t *testing.T) {
	// Region of 100 frames, fade 10, delivered as blocks [0,40) [40,100).
	a := make([]int32, 40)
	b := make([]int32, 60)
	for i := range a {
		a[i] = 1000
	}
	for i := range b {
		b[i] = 1000
	}
	applyFades(a, 0, 100, 1, 10)
	applyFades(b, 40, 100, 1, 10)
	if a[0] != 0 || a[10] != 1000 || a[39] != 1000 {
		t.Errorf("first block wrong: %v", a[:12])
	}
	if b[0] != 1000 || b[49] != 1000 || b[50] != 1000 || b[59] != 100 {
		t.Errorf("second block wrong: %d %d %d %d", b[0], b[49], b[50], b[59])
	}
}

func TestCutWritesAFadedRegionAsANewTake(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "jam_src.wav")
	// A ramp, not a constant: with every frame carrying a different value a
	// copy that is off by even one frame -- or a fade applied where it should
	// not be -- changes the sample, which a flat source would hide.
	data := make([]int32, 48000*2)
	for i := range data {
		f := int32(i/2) << 10
		if i%2 == 0 {
			data[i] = f
		} else {
			data[i] = -f
		}
	}
	if _, err := WriteWAV(src, data, 2, []int{0, 1}, 48000); err != nil {
		t.Fatal(err)
	}
	bpm := 96.0
	if err := WriteMeta(src, Meta{Version: MetaVersion, Label: "jam", BPM: &bpm, Starred: true,
		Trim:  &Trim{StartFrame: 1, EndFrame: 2},
		Flags: []Flag{{Frame: 100, Label: "before"}, {Frame: 10500, Label: "inside"}, {Frame: 30000}}}); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 9, 10, 22, 14, 41, 0, time.UTC)
	name, err := Cut(dir, CutRequest{Source: "jam_src.wav", StartFrame: 10000, EndFrame: 20000}, now)
	if err != nil {
		t.Fatal(err)
	}
	if name != "jam_2026-09-10_221441.wav" {
		t.Errorf("name = %q", name)
	}
	out := filepath.Join(dir, name)

	info, err := ReadWAVInfo(out)
	if err != nil || info.Frames() != 10000 || info.Channels != 2 || info.BitsPerSample != 32 {
		t.Fatalf("info = %+v err = %v", info, err)
	}
	// Output frame i is source frame 10000+i. The fade is 144 frames at each
	// edge, so frames 144 .. 10000-144-1 must be byte-identical to the source.
	fade := int(FadeFrames(48000))
	srcL := func(frame int) int32 { return int32(frame) << 10 }
	var first, afterFadeIn, mid, midR, beforeFadeOut, last int32
	_, err = ReadFrames(out, 0, 10000, 10000, func(b []int32, _ int64) error {
		first = b[0]
		afterFadeIn = b[fade*2]
		mid, midR = b[5000*2], b[5000*2+1]
		beforeFadeOut = b[(10000-fade-1)*2]
		last = b[9999*2]
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if first != 0 {
		t.Errorf("first = %d, want silence at the head of the fade-in", first)
	}
	if want := srcL(10000 + fade); afterFadeIn != want {
		t.Errorf("frame %d = %d, want the exact source sample %d", fade, afterFadeIn, want)
	}
	if want := srcL(15000); mid != want {
		t.Errorf("mid = %d, want the exact source sample %d", mid, want)
	}
	if want := -srcL(15000); midR != want {
		t.Errorf("mid right = %d, want %d: channels not kept in step", midR, want)
	}
	if want := srcL(10000 + 10000 - fade - 1); beforeFadeOut != want {
		t.Errorf("frame %d = %d, want the exact source sample %d", 10000-fade-1, beforeFadeOut, want)
	}
	if full := srcL(19999); last >= full || last <= 0 {
		t.Errorf("last = %d, want a faded tail strictly between 0 and %d", last, full)
	}

	if _, err := os.Stat(peaksPath(out)); err != nil {
		t.Error("no peaks file written")
	}
	m := ReadMeta(out)
	if m.Label != "jam cut" {
		t.Errorf("label = %q, want %q", m.Label, "jam cut")
	}
	if m.BPM == nil || *m.BPM != 96 {
		t.Errorf("bpm = %v, want 96 inherited", m.BPM)
	}
	if len(m.Flags) != 1 || m.Flags[0].Frame != 500 || m.Flags[0].Label != "inside" {
		t.Errorf("flags = %+v, want only the inside one rebased to 500", m.Flags)
	}
	if m.Source == nil || m.Source.Name != "jam_src.wav" || m.Source.StartFrame != 10000 || m.Source.EndFrame != 20000 {
		t.Errorf("source = %+v", m.Source)
	}
	if m.Starred || m.Trim != nil || m.DownbeatFrame != nil {
		t.Errorf("starred/trim/downbeat copied: %+v", m)
	}
	cues, _ := ReadCuePoints(out)
	if len(cues) != 1 || cues[0].Frame != 500 || cues[0].Label != "inside" {
		t.Errorf("cue points = %+v", cues)
	}
	// The source is untouched.
	if sm := ReadMeta(src); len(sm.Flags) != 3 || !sm.Starred {
		t.Errorf("source sidecar changed: %+v", sm)
	}
}

func TestCutUsesTheGivenLabelAndFallsBackToTheStem(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "jam_src.wav")
	if _, err := WriteWAV(src, make([]int32, 2000*2), 2, []int{0, 1}, 48000); err != nil {
		t.Fatal(err)
	}
	name, err := Cut(dir, CutRequest{Source: "jam_src.wav", StartFrame: 0, EndFrame: 1000, Label: "hit"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if m := ReadMeta(filepath.Join(dir, name)); m.Label != "hit" {
		t.Errorf("label = %q", m.Label)
	}
	name2, err := Cut(dir, CutRequest{Source: "jam_src.wav", StartFrame: 0, EndFrame: 1000}, time.Now().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if m := ReadMeta(filepath.Join(dir, name2)); m.Label != "jam_src cut" {
		t.Errorf("fallback label = %q, want %q", m.Label, "jam_src cut")
	}
}

func TestCutNeverOverwritesAnExistingTake(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "jam_src.wav")
	if _, err := WriteWAV(src, make([]int32, 2000*2), 2, []int{0, 1}, 48000); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 10, 1, 2, 3, 0, time.UTC)
	a, _ := Cut(dir, CutRequest{Source: "jam_src.wav", StartFrame: 0, EndFrame: 1000}, now)
	b, _ := Cut(dir, CutRequest{Source: "jam_src.wav", StartFrame: 0, EndFrame: 1000}, now)
	if a == b || b != "jam_2026-09-10_010203_2.wav" {
		t.Errorf("second cut = %q, want a distinct _2 name", b)
	}
}

func TestCutRejectsShortAndOutOfRangeRegions(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "jam_src.wav")
	if _, err := WriteWAV(src, make([]int32, 2000*2), 2, []int{0, 1}, 48000); err != nil {
		t.Fatal(err)
	}
	countWAVs := func() int {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, e := range entries {
			if filepath.Ext(e.Name()) == ".wav" {
				n++
			}
		}
		return n
	}

	before := countWAVs()
	if _, err := Cut(dir, CutRequest{Source: "jam_src.wav", StartFrame: 0, EndFrame: 288}, time.Now()); !errors.Is(err, ErrTooShort) {
		t.Errorf("288 frames (2*144): err = %v, want ErrTooShort", err)
	}
	if _, err := Cut(dir, CutRequest{Source: "jam_src.wav", StartFrame: 1000, EndFrame: 2001}, time.Now()); !errors.Is(err, ErrRange) {
		t.Errorf("past end: err = %v, want ErrRange", err)
	}
	if after := countWAVs(); after != before {
		t.Errorf("rejected cuts left files behind: %d wavs, was %d", after, before)
	}

	if _, err := Cut(dir, CutRequest{Source: "jam_src.wav", StartFrame: 0, EndFrame: 289}, time.Now()); err != nil {
		t.Errorf("289 frames: %v, want ok", err)
	}
	if after := countWAVs(); after != before+1 {
		t.Errorf("accepted cut wrote %d wavs, want %d", after, before+1)
	}
}

func TestMetaRoundTripsDownbeatAndSource(t *testing.T) {
	p := filepath.Join(t.TempDir(), "jam_m.wav")
	db := int64(4800)
	if err := WriteMeta(p, Meta{Version: MetaVersion, DownbeatFrame: &db,
		Source: &CutSource{Name: "jam_a.wav", StartFrame: 1, EndFrame: 2}}); err != nil {
		t.Fatal(err)
	}
	m := ReadMeta(p)
	if m.DownbeatFrame == nil || *m.DownbeatFrame != 4800 || m.Source == nil || m.Source.Name != "jam_a.wav" {
		t.Errorf("round trip lost fields: %+v", m)
	}
	// An older sidecar without them reads fine.
	os.WriteFile(metaPath(p), []byte(`{"version":1,"label":"old"}`), 0o644)
	if m := ReadMeta(p); m.Label != "old" || m.DownbeatFrame != nil || m.Source != nil {
		t.Errorf("old sidecar: %+v", m)
	}
}
