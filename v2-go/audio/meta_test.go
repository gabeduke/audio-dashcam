package audio

import (
	"os"
	"path/filepath"
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
	p := MetaPath(wav)
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

func TestMetaPathReplacesExtension(t *testing.T) {
	if got := MetaPath("/a/b/jam_2026.wav"); got != "/a/b/jam_2026.meta.json" {
		t.Errorf("MetaPath = %q, want %q", got, "/a/b/jam_2026.meta.json")
	}
}
