package audio

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gabeduke/hindsight/internal/config"
)

// writeFakeTake creates a file that ListTakes will pick up. The WAV header is
// not valid, which is deliberate: ListTakes must degrade to filesystem facts
// when ReadWAVInfo fails, and these tests are about metadata, not audio.
func writeFakeTake(t *testing.T, dir, name string, modAgo time.Duration) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("not a real wav"), 0o644); err != nil {
		t.Fatal(err)
	}
	mod := time.Now().Add(-modAgo)
	if err := os.Chtimes(p, mod, mod); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestListTakesMergesSidecar(t *testing.T) {
	dir := t.TempDir()
	wav := writeFakeTake(t, dir, "jam_a.wav", time.Minute)
	if err := WriteMeta(wav, Meta{Label: "the good one", Starred: true}); err != nil {
		t.Fatal(err)
	}

	takes, err := ListTakes(dir)
	if err != nil {
		t.Fatalf("ListTakes: %v", err)
	}
	if len(takes) != 1 {
		t.Fatalf("got %d takes, want 1", len(takes))
	}
	if takes[0].Label != "the good one" {
		t.Errorf("Label = %q, want %q", takes[0].Label, "the good one")
	}
	if !takes[0].Starred {
		t.Error("Starred = false, want true")
	}
}

func TestListTakesWithoutSidecarHasEmptyLabel(t *testing.T) {
	dir := t.TempDir()
	writeFakeTake(t, dir, "jam_a.wav", time.Minute)

	takes, err := ListTakes(dir)
	if err != nil {
		t.Fatalf("ListTakes: %v", err)
	}
	if len(takes) != 1 {
		t.Fatalf("got %d takes, want 1", len(takes))
	}
	if takes[0].Label != "" || takes[0].Starred {
		t.Errorf("want defaults for a take with no sidecar, got %+v", takes[0])
	}
	if takes[0].Name != "jam_a.wav" {
		t.Errorf("Name = %q, want the filename", takes[0].Name)
	}
}

func TestListTakesIgnoresSidecarsAsTakes(t *testing.T) {
	dir := t.TempDir()
	wav := writeFakeTake(t, dir, "jam_a.wav", time.Minute)
	if err := WriteMeta(wav, Meta{Label: "x"}); err != nil {
		t.Fatal(err)
	}

	takes, err := ListTakes(dir)
	if err != nil {
		t.Fatalf("ListTakes: %v", err)
	}
	if len(takes) != 1 {
		t.Fatalf("got %d takes, want 1 — the .meta.json must not be listed", len(takes))
	}
}

func TestListTakesSortsStarredFirstThenNewest(t *testing.T) {
	dir := t.TempDir()
	// Oldest is starred, so ordering by date alone would put it last.
	oldStarred := writeFakeTake(t, dir, "jam_old_starred.wav", 3*time.Hour)
	writeFakeTake(t, dir, "jam_newest.wav", 1*time.Minute)
	writeFakeTake(t, dir, "jam_middle.wav", 1*time.Hour)

	if err := WriteMeta(oldStarred, Meta{Starred: true}); err != nil {
		t.Fatal(err)
	}

	takes, err := ListTakes(dir)
	if err != nil {
		t.Fatalf("ListTakes: %v", err)
	}

	var got []string
	for _, tk := range takes {
		got = append(got, tk.Name)
	}
	want := []string{"jam_old_starred.wav", "jam_newest.wav", "jam_middle.wav"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order = %v, want %v", got, want)
		}
	}
}

func TestRemoveTakeDeletesSidecar(t *testing.T) {
	dir := t.TempDir()
	wav := writeFakeTake(t, dir, "jam_a.wav", time.Minute)
	if err := WriteMeta(wav, Meta{Label: "x", Starred: true}); err != nil {
		t.Fatal(err)
	}

	RemoveTake(dir, "jam_a.wav")

	if _, err := os.Stat(wav); !os.IsNotExist(err) {
		t.Error("wav still present after RemoveTake")
	}
	if _, err := os.Stat(metaPath(wav)); !os.IsNotExist(err) {
		t.Error("sidecar still present after RemoveTake — a new take reusing the name would inherit it")
	}
}

// fakeTempo is a TempoSource that answers from a fixed value and records the
// window it was asked about.
type fakeTempo struct {
	bpm      float64
	ok       bool
	panics   bool
	gotStart time.Time
	gotEnd   time.Time
}

func (f *fakeTempo) BPM(start, end time.Time) (float64, bool) {
	if f.panics {
		panic("a tempo source must never be able to break a save")
	}
	f.gotStart, f.gotEnd = start, end
	return f.bpm, f.ok
}

func TestStampTempoWritesTheSidecar(t *testing.T) {
	dir := t.TempDir()
	wav := writeFakeTake(t, dir, "jam_a.wav", 0)

	src := &fakeTempo{bpm: 129.874321, ok: true}
	stampTempo(wav, src, time.Now(), 30*time.Second)

	got := ReadMeta(wav)
	if got.BPM == nil {
		t.Fatal("BPM = nil, want 129.87")
	}
	// Rounded to two decimals: the estimator's precision is not meaningful
	// past that and a sidecar full of 129.87432100000001 helps nobody.
	if *got.BPM != 129.87 {
		t.Errorf("BPM = %v, want 129.87", *got.BPM)
	}
}

func TestStampTempoAsksAboutTheCapturedWindow(t *testing.T) {
	dir := t.TempDir()
	wav := writeFakeTake(t, dir, "jam_a.wav", 0)

	end := time.Now()
	src := &fakeTempo{bpm: 120, ok: true}
	stampTempo(wav, src, end, 30*time.Second)

	if !src.gotEnd.Equal(end) {
		t.Errorf("end = %v, want %v", src.gotEnd, end)
	}
	if want := end.Add(-30 * time.Second); !src.gotStart.Equal(want) {
		t.Errorf("start = %v, want %v", src.gotStart, want)
	}
}

// No clock, no device, too short a window: all of these leave the take alone.
func TestStampTempoWritesNothingWhenThereIsNoReading(t *testing.T) {
	dir := t.TempDir()
	wav := writeFakeTake(t, dir, "jam_a.wav", 0)

	stampTempo(wav, &fakeTempo{ok: false}, time.Now(), 30*time.Second)

	if _, err := os.Stat(metaPath(wav)); !os.IsNotExist(err) {
		t.Errorf("a sidecar was written for a take with no reading (err = %v)", err)
	}
}

func TestStampTempoWithNoSourceIsANoOp(t *testing.T) {
	dir := t.TempDir()
	wav := writeFakeTake(t, dir, "jam_a.wav", 0)

	stampTempo(wav, nil, time.Now(), 30*time.Second)

	if _, err := os.Stat(metaPath(wav)); !os.IsNotExist(err) {
		t.Errorf("a sidecar was written with no tempo source (err = %v)", err)
	}
}

// The hard rule from the spec: a save must never fail because of MIDI. A tempo
// source that panics is the most hostile version of that.
func TestStampTempoSurvivesAPanickingSource(t *testing.T) {
	dir := t.TempDir()
	wav := writeFakeTake(t, dir, "jam_a.wav", 0)

	stampTempo(wav, &fakeTempo{panics: true}, time.Now(), 30*time.Second)
	// Reaching here without the process dying is the assertion.
}

// Stamping must not clobber a label a user set between the write and the stamp.
func TestStampTempoPreservesExistingSidecarFields(t *testing.T) {
	dir := t.TempDir()
	wav := writeFakeTake(t, dir, "jam_a.wav", 0)
	if err := WriteMeta(wav, Meta{Label: "keep me", Starred: true}); err != nil {
		t.Fatal(err)
	}

	stampTempo(wav, &fakeTempo{bpm: 100, ok: true}, time.Now(), 30*time.Second)

	got := ReadMeta(wav)
	if got.Label != "keep me" || !got.Starred {
		t.Errorf("stamping lost sidecar fields: %+v", got)
	}
	if got.BPM == nil || *got.BPM != 100 {
		t.Errorf("BPM = %v, want 100", got.BPM)
	}
}

func TestListTakesReportsBPM(t *testing.T) {
	dir := t.TempDir()
	wav := writeFakeTake(t, dir, "jam_a.wav", time.Minute)
	bpm := 92.5
	if err := WriteMeta(wav, Meta{BPM: &bpm}); err != nil {
		t.Fatal(err)
	}

	takes, err := ListTakes(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(takes) != 1 || takes[0].BPM == nil {
		t.Fatalf("BPM missing from the listing: %+v", takes)
	}
	if *takes[0].BPM != 92.5 {
		t.Errorf("BPM = %v, want 92.5", *takes[0].BPM)
	}
}

func TestFlagsForWindowTranslatesToTakeRelativeFrames(t *testing.T) {
	// Window covers absolute frames [1000, 1400).
	got := flagsForWindow([]uint64{1000, 1200, 1399}, 1000, 1400)
	want := []int64{0, 200, 399}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %d flags", got, len(want))
	}
	for i := range want {
		if got[i].Frame != want[i] {
			t.Errorf("flag[%d].Frame = %d, want %d", i, got[i].Frame, want[i])
		}
	}
}

func TestFlagsForWindowExcludesMarksOutsideIt(t *testing.T) {
	got := flagsForWindow([]uint64{999, 1400, 5000}, 1000, 1400)
	if len(got) != 0 {
		t.Errorf("got %+v, want none: 999 predates the window and 1400 is past its last frame", got)
	}
}

func TestFlagsForWindowOnAnEmptyWindow(t *testing.T) {
	if got := flagsForWindow([]uint64{5}, 0, 0); got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}

func TestFlagsForWindowWithNoMarks(t *testing.T) {
	if got := flagsForWindow(nil, 1000, 2000); got != nil {
		t.Errorf("got %+v, want nil", got)
	}
}

func TestMarkNowOnAnEmptyRingReportsFalse(t *testing.T) {
	c := NewCapture(&config.Config{Channels: 2, SampleRate: 48000, RingSeconds: 10, SaveChannels: []int{0, 1}}, nil)
	if _, ok := c.MarkNow(); ok {
		t.Error("MarkNow on an empty ring = true, want false")
	}
}

// The regression this pins: a mark placed at TotalFrames() rather than
// TotalFrames()-1 lands one past the last frame of the very take that should
// contain it, and flagsForWindow's half-open window drops it. Marking and then
// capturing is the feature's core path.
func TestAMarkPlacedNowSurvivesAnImmediateCapture(t *testing.T) {
	c := NewCapture(&config.Config{Channels: 2, SampleRate: 48000, RingSeconds: 10, SaveChannels: []int{0, 1}}, nil)
	c.Ring().WriteFrames(make([]int32, 1000*2))

	frame, ok := c.MarkNow()
	if !ok {
		t.Fatal("MarkNow reported no audio")
	}

	_, got, end := c.Ring().SnapshotAt(0) // the whole ring, as Save(0) does
	flags := flagsForWindow(c.Flags().Active(end, 480000), end-uint64(got), end)
	if len(flags) != 1 {
		t.Fatalf("flags in the captured window = %+v, want the mark at %d to survive", flags, frame)
	}
	if flags[0].Frame != int64(got-1) {
		t.Errorf("flag frame = %d, want %d (the take's last frame)", flags[0].Frame, got-1)
	}
}
