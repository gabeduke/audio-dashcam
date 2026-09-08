package audio

import (
	"os"
	"path/filepath"
	"testing"
	"time"
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
