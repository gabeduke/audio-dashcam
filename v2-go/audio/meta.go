package audio

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// MetaVersion is the sidecar schema version. Bump it only for a change that
// older readers cannot tolerate.
const MetaVersion = 1

// Trim marks the region of a take to export, in frames. Frames rather than
// seconds so the bounds are sample-exact and survive as integers, and so they
// line up with the RIFF cue points other tools use.
//
// Nothing in phase 1 reads this; it exists so adding trim later needs no
// schema change.
type Trim struct {
	StartFrame int64 `json:"start_frame"`
	EndFrame   int64 `json:"end_frame"`
}

// Meta is the per-take sidecar. Every field but Version is optional: an absent
// sidecar means an unnamed, unstarred, untrimmed take, which is what keeps
// takes recorded before this feature valid without migration.
type Meta struct {
	Version int    `json:"version"`
	Label   string `json:"label,omitempty"`
	Starred bool   `json:"starred,omitempty"`
	Trim    *Trim  `json:"trim,omitempty"`
}

// MetaPath returns the sidecar path for a take's wav path.
func MetaPath(wav string) string { return strings.TrimSuffix(wav, ".wav") + ".meta.json" }

// ReadMeta loads a take's sidecar. A missing or unparseable sidecar yields
// defaults rather than an error, and the file is left alone: metadata is
// derived, disposable state, and losing it must never obscure the audio.
func ReadMeta(wav string) Meta {
	def := Meta{Version: MetaVersion}

	b, err := os.ReadFile(MetaPath(wav))
	if err != nil {
		return def
	}
	var got Meta
	if err := json.Unmarshal(b, &got); err != nil {
		return def
	}
	got.Version = MetaVersion
	return got
}

// WriteMeta writes a take's sidecar atomically. The UI polls the take list
// every five seconds, so a half-written file would be read eventually; a temp
// file plus rename makes that impossible.
func WriteMeta(wav string, m Meta) error {
	m.Version = MetaVersion

	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}

	p := MetaPath(wav)
	tmp, err := os.CreateTemp(filepath.Dir(p), ".meta-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	// Harmless once the rename below succeeds; the safety net is for the
	// error paths, which must not litter the takes directory.
	defer os.Remove(name)

	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, p)
}
