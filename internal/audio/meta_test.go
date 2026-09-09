package audio

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadMetaMissingFileReturnsDefaults(t *testing.T) {
	wav := filepath.Join(t.TempDir(), "jam_x.wav")
	m := ReadMeta(wav)
	if m.Version != MetaVersion {
		t.Errorf("Version = %d, want %d", m.Version, MetaVersion)
	}
	if m.Label != "" || m.Starred || m.Trim != nil {
		t.Errorf("want zero-value metadata, got %+v", m)
	}
}

func TestWriteThenReadMetaRoundTrips(t *testing.T) {
	wav := filepath.Join(t.TempDir(), "jam_x.wav")
	in := Meta{Label: "the good one", Starred: true, Trim: &Trim{StartFrame: 100, EndFrame: 200}}

	if err := WriteMeta(wav, in); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}
	got := ReadMeta(wav)

	if got.Version != MetaVersion {
		t.Errorf("Version = %d, want %d", got.Version, MetaVersion)
	}
	if got.Label != "the good one" {
		t.Errorf("Label = %q, want %q", got.Label, "the good one")
	}
	if !got.Starred {
		t.Error("Starred = false, want true")
	}
	if got.Trim == nil || got.Trim.StartFrame != 100 || got.Trim.EndFrame != 200 {
		t.Errorf("Trim = %+v, want {100 200}", got.Trim)
	}
}

func TestReadMetaCorruptFileReturnsDefaultsAndKeepsFile(t *testing.T) {
	wav := filepath.Join(t.TempDir(), "jam_x.wav")
	p := metaPath(wav)
	if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := ReadMeta(wav)
	if m.Label != "" || m.Starred {
		t.Errorf("want defaults from corrupt sidecar, got %+v", m)
	}
	// Metadata is disposable, but it is not ours to delete: the audio is fine.
	if _, err := os.Stat(p); err != nil {
		t.Errorf("corrupt sidecar was removed, want it left in place: %v", err)
	}
}

func TestWriteMetaLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	wav := filepath.Join(dir, "jam_x.wav")

	if err := WriteMeta(wav, Meta{Label: "a"}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "jam_x.meta.json" {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("dir = %v, want exactly [jam_x.meta.json]", names)
	}
}

func TestWriteMetaSidecarModeIs0644(t *testing.T) {
	wav := filepath.Join(t.TempDir(), "jam_x.wav")

	if err := WriteMeta(wav, Meta{Label: "a"}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	fi, err := os.Stat(metaPath(wav))
	if err != nil {
		t.Fatal(err)
	}
	// os.Chmod sets the mode explicitly rather than going through the umask,
	// so 0644 is exact here — this assertion is not flaky under a stricter
	// umask the way relying on CreateTemp's default mode would be.
	if got := fi.Mode().Perm(); got != 0o644 {
		t.Errorf("sidecar mode = %v, want 0644 to match the sibling .peaks.json and _preview.mp3", got)
	}
}

func TestMetaPathReplacesExtension(t *testing.T) {
	if got := metaPath("/a/b/jam_2026.wav"); got != "/a/b/jam_2026.meta.json" {
		t.Errorf("metaPath = %q, want %q", got, "/a/b/jam_2026.meta.json")
	}
}

func TestReadMetaPreservesNewerVersionAndWriteMetaRefusesToDowngrade(t *testing.T) {
	wav := filepath.Join(t.TempDir(), "jam_x.wav")
	p := metaPath(wav)
	raw := []byte(`{"version":2,"label":"keep me","tags":["blues"]}`)
	if err := os.WriteFile(p, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	got := ReadMeta(wav)
	if got.Version != 2 {
		t.Errorf("Version = %d, want 2", got.Version)
	}
	if got.Label != "keep me" {
		t.Errorf("Label = %q, want %q", got.Label, "keep me")
	}

	if err := WriteMeta(wav, got); err == nil {
		t.Error("WriteMeta on a newer-version sidecar: want a non-nil error, got nil")
	}

	after, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(raw) {
		t.Errorf("sidecar on disk changed after refused WriteMeta:\nbefore: %s\nafter:  %s", raw, after)
	}
}

func TestReadMetaNormalizesVersionlessSidecar(t *testing.T) {
	wav := filepath.Join(t.TempDir(), "jam_x.wav")
	if err := os.WriteFile(metaPath(wav), []byte(`{"label":"old"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	got := ReadMeta(wav)
	if got.Version != MetaVersion {
		t.Errorf("Version = %d, want %d", got.Version, MetaVersion)
	}
	if got.Label != "old" {
		t.Errorf("Label = %q, want %q", got.Label, "old")
	}
}

func TestWriteMetaOverwritesExistingSidecar(t *testing.T) {
	dir := t.TempDir()
	wav := filepath.Join(dir, "jam_x.wav")

	if err := WriteMeta(wav, Meta{Label: "first"}); err != nil {
		t.Fatalf("WriteMeta (first): %v", err)
	}
	if err := WriteMeta(wav, Meta{Label: "second", Starred: true}); err != nil {
		t.Fatalf("WriteMeta (second): %v", err)
	}

	got := ReadMeta(wav)
	if got.Label != "second" || !got.Starred {
		t.Errorf("got %+v, want the second write to have won", got)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var metaFiles []string
	for _, e := range entries {
		if filepath.Ext(e.Name()) == ".json" {
			metaFiles = append(metaFiles, e.Name())
		}
	}
	if len(metaFiles) != 1 {
		t.Errorf("*.meta.json files = %v, want exactly one", metaFiles)
	}
}

func TestWriteMetaCleansUpTempFileOnRenameError(t *testing.T) {
	dir := t.TempDir()
	wav := filepath.Join(dir, "jam_x.wav")

	// Make the rename destination a non-empty directory, which os.Rename
	// cannot replace a file with — forcing WriteMeta down its error path.
	if err := os.MkdirAll(filepath.Join(metaPath(wav), "blocker"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := WriteMeta(wav, Meta{Label: "a"}); err == nil {
		t.Error("WriteMeta with a blocked rename target: want a non-nil error, got nil")
	}

	leftovers, err := filepath.Glob(filepath.Join(dir, ".meta-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 0 {
		t.Errorf("temp files left behind after failed WriteMeta: %v", leftovers)
	}
}

func TestMetaBPMRoundTrips(t *testing.T) {
	dir := t.TempDir()
	wav := filepath.Join(dir, "jam_a.wav")

	bpm := 129.87
	if err := WriteMeta(wav, Meta{Label: "one", BPM: &bpm}); err != nil {
		t.Fatal(err)
	}

	got := ReadMeta(wav)
	if got.BPM == nil {
		t.Fatal("BPM = nil, want 129.87")
	}
	if *got.BPM != 129.87 {
		t.Errorf("BPM = %v, want 129.87", *got.BPM)
	}
	if got.Label != "one" {
		t.Errorf("Label = %q, want %q", got.Label, "one")
	}
}

// Absent must be distinguishable from zero. A take saved with no MIDI device
// present has no tempo; it does not have a tempo of nothing.
func TestMetaAbsentBPMIsNilNotZero(t *testing.T) {
	dir := t.TempDir()
	wav := filepath.Join(dir, "jam_a.wav")

	if err := WriteMeta(wav, Meta{Label: "one"}); err != nil {
		t.Fatal(err)
	}
	if got := ReadMeta(wav); got.BPM != nil {
		t.Errorf("BPM = %v, want nil", *got.BPM)
	}

	b, err := os.ReadFile(metaPath(wav))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "bpm") {
		t.Errorf("sidecar carries a bpm key when none was set: %s", b)
	}
}

// A sidecar written before this field existed must still load. Version is
// deliberately not bumped: the rule in meta.go is to bump only for a change
// older readers cannot tolerate, and an optional additive field is tolerable.
func TestMetaSidecarWithoutBPMStillLoads(t *testing.T) {
	dir := t.TempDir()
	wav := filepath.Join(dir, "jam_a.wav")
	old := `{"version":1,"label":"before bpm existed","starred":true}`
	if err := os.WriteFile(metaPath(wav), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}

	got := ReadMeta(wav)
	if got.Label != "before bpm existed" || !got.Starred {
		t.Errorf("old sidecar did not load: %+v", got)
	}
	if got.BPM != nil {
		t.Errorf("BPM = %v, want nil", *got.BPM)
	}
	if got.Version != 1 {
		t.Errorf("Version = %d, want 1 (this field must not bump it)", got.Version)
	}
}

// The other direction: a sidecar carrying a BPM must load in a build that
// predates the field. Version 1 is what makes that true, so pin it.
func TestMetaVersionIsUnchangedByBPM(t *testing.T) {
	if MetaVersion != 1 {
		t.Errorf("MetaVersion = %d, want 1; adding an optional field must not bump it", MetaVersion)
	}
}
