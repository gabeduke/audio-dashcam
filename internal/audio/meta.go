package audio

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
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

// Flag marks a moment of interest inside a take, in frames from its first
// frame. Frames rather than seconds so the mark is sample-exact, survives as an
// integer, and lines up with the RIFF cue points other tools read.
//
// Label is the owner's name for the moment. It is mirrored into the WAV as a
// LIST/adtl "labl" record so DAWs show it beside the marker.
type Flag struct {
	Frame int64  `json:"frame"`
	Label string `json:"label,omitempty"`
}

// NormalizeFlags returns flags sorted by frame with duplicates and negative
// frames removed. The first occurrence of a frame wins, so a label already
// attached to it is not lost to a later bare mark.
func NormalizeFlags(in []Flag) []Flag {
	if len(in) == 0 {
		return nil
	}
	out := make([]Flag, 0, len(in))
	seen := make(map[int64]bool, len(in))
	for _, f := range in {
		if f.Frame < 0 || seen[f.Frame] {
			continue
		}
		seen[f.Frame] = true
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Frame < out[j].Frame })
	if len(out) == 0 {
		return nil
	}
	return out
}

// CutSource is a cut's lineage: the take it was cut from and the frame range,
// in the source's frames.
type CutSource struct {
	Name       string `json:"name"`
	StartFrame int64  `json:"start_frame"`
	EndFrame   int64  `json:"end_frame"`
}

// Meta is the per-take sidecar. Every field but Version is optional: an absent
// sidecar means an unnamed, unstarred, untrimmed take, which is what keeps
// takes recorded before this feature valid without migration.
type Meta struct {
	Version int    `json:"version"`
	Label   string `json:"label,omitempty"`
	Starred bool   `json:"starred,omitempty"`
	Trim    *Trim  `json:"trim,omitempty"`

	// DownbeatFrame is where bar 1 beat 1 falls, for the waveform page's
	// grid. Optional; absent means "unknown", and the page then starts the
	// grid at frame 0.
	DownbeatFrame *int64 `json:"downbeat_frame,omitempty"`

	// Source records where a cut came from. Nil for a take saved from the ring.
	Source *CutSource `json:"source,omitempty"`

	// BPM is the tempo the take was played at, read from the EP's MIDI clock
	// at save time and editable afterwards.
	//
	// A pointer because absent and zero are different states: no MIDI device,
	// no clock, or too short a window all mean "no tempo", and a take with a
	// tempo of 0 does not exist. The free-running clock does not reliably
	// match the loaded project tempo -- one idle window read 129.87
	// rock-steady against a project set to 92 -- so this is a starting point
	// the owner overrides, never a fact.
	BPM *float64 `json:"bpm,omitempty"`

	// Flags mark moments of interest, in frames from the take's first frame.
	// Optional and additive, like BPM, so it needs no MetaVersion bump.
	Flags []Flag `json:"flags,omitempty"`
}

// ErrNewerSidecar reports a sidecar written by a build that knew fields this
// one does not. Rewriting it would drop them.
var ErrNewerSidecar = errors.New("sidecar was written by a newer version")

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
		return fmt.Errorf("%w: sidecar is version %d, this build writes %d", ErrNewerSidecar, m.Version, MetaVersion)
	}
	m.Version = MetaVersion
	m.Flags = NormalizeFlags(m.Flags)

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
