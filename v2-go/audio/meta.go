package audio

import (
	"encoding/json"
	"fmt"
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

// metaPath returns the sidecar path for a take's wav path.
func metaPath(wav string) string { return strings.TrimSuffix(wav, ".wav") + ".meta.json" }

// ReadMeta loads a take's sidecar. A missing or unparseable sidecar yields
// defaults rather than an error, and the file is left alone: metadata is
// derived, disposable state, and losing it must never obscure the audio.
//
// Every error — missing file, permission denied, corrupt JSON — is swallowed
// rather than logged. ListTakes calls this per take on a 5-second poll, so
// logging here would produce thousands of lines a day for a condition that is
// usually just "no sidecar yet".
func ReadMeta(wav string) Meta {
	def := Meta{Version: MetaVersion}

	b, err := os.ReadFile(metaPath(wav))
	if err != nil {
		return def
	}
	var got Meta
	if err := json.Unmarshal(b, &got); err != nil {
		return def
	}
	// Only stamp a version onto a sidecar that predates the field. A sidecar
	// that already names a (possibly newer) version must keep it: downgrading
	// it here would make a later read-modify-write silently drop any fields
	// this build doesn't know about.
	if got.Version == 0 {
		got.Version = MetaVersion
	}
	return got
}

// WriteMeta writes a take's sidecar atomically. The UI polls the take list
// every five seconds, so a half-written file would be read eventually; a temp
// file plus rename makes that impossible.
func WriteMeta(wav string, m Meta) error {
	// Refuse to rewrite a sidecar written by a newer version: this code cannot
	// represent fields it does not know about, and silently dropping them
	// would be worse than failing.
	if m.Version > MetaVersion {
		return fmt.Errorf("sidecar is version %d, this build writes %d", m.Version, MetaVersion)
	}
	m.Version = MetaVersion

	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}

	p := metaPath(wav)
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
	// Match the 0644 of the sibling sidecars (.peaks.json, _preview.mp3):
	// CreateTemp defaults to 0600, and rename preserves that, which would
	// otherwise make this file uniquely inaccessible to anything else that
	// touches the takes directory (an rsync backup, another user over SSH).
	if err := os.Chmod(name, 0o644); err != nil {
		return err
	}
	return os.Rename(name, p)
}
