# Take Identity (Phase 1) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Give every take a human label and a star, stored in a per-take JSON sidecar, so the takes list stops being a wall of identical timestamps.

**Architecture:** A `jam_<ts>.meta.json` sidecar sits beside each take. It is written atomically (temp file + rename) so the UI's 5-second poll can never read a half-written file. An absent or corrupt sidecar yields defaults, so every existing take stays valid with no migration. `ListTakes` merges the sidecar into each `Take`, and sorts starred takes first. A single `PATCH /api/take` endpoint merges fields into the sidecar. No audio code is touched.

**Tech Stack:** Go 1.23 (stdlib `testing`, `net/http/httptest`), gorilla/mux, vanilla ES modules on the frontend.

---

## Spec

Implements Phase 1 of `docs/superpowers/specs/2026-09-07-take-triage-trim-design.md`.

**One deliberate deviation from the spec.** The spec calls the sidecar's human name field `name`. `Take.Name` is already the *filename* and is used as the row key throughout `takes.js`, so reusing `name` would give one word two meanings in the same JSON object. This plan uses **`label`** for the human name in both the sidecar and the API. Update the spec to match when this lands.

**Tablet breakpoints are not in this phase.** The spec's tablet section
(`>= 600px`) is a layout change across all the panels, not a property of a take
row. Phase 1 adds a star and a name to an existing row and inherits whatever
layout is already there. Track it as its own task after Phase 2, when the trim
editor — the screen that actually benefits from the width — exists.

**Trim is defined but unused.** `Meta` carries a `Trim` field and `PATCH` round-trips it, so Phase 2 needs no schema change or version bump. Nothing in Phase 1 reads it, and no UI sets it.

---

## File Structure

| File | Responsibility |
|---|---|
| `v2-go/audio/meta.go` | **Create.** The `Meta`/`Trim` types, sidecar path derivation, atomic read/write. Nothing else knows the sidecar's file format. |
| `v2-go/audio/meta_test.go` | **Create.** Round-trip, defaults, corruption tolerance, atomicity. |
| `v2-go/audio/save.go` | **Modify.** `Take` gains `Label`/`Starred`/`Trim`; `ListTakes` merges the sidecar and sorts starred-first; `RemoveTake` deletes the sidecar. |
| `v2-go/audio/save_test.go` | **Create.** Ordering and sidecar merge in `ListTakes`. |
| `v2-go/api/api.go` | **Modify.** Route + handler for `PATCH /api/take`. |
| `v2-go/api/api_test.go` | **Create.** Merge semantics, validation, path guards. |
| `v2-go/static/lib/takes.js` | **Modify.** Star button, inline rename, `patchTake` helper. |
| `v2-go/static/styles.css` | **Modify.** Star and name-edit styling. |

Run all Go commands from `v2-go/`.

**Note on running tests locally:** `audio/capture.go` imports PortAudio via cgo. If `go test ./audio/...` fails to build on a machine without PortAudio, run the tests on the Pi via `ssh "$DASHCAM_HOST" 'cd ~/audio-dashcam/v2-go && go test ./...'` after a `./deploy.sh`. The `api` package tests do not need PortAudio at runtime but do compile the `audio` package, so the same applies.

---

### Task 1: The sidecar type and atomic IO

**Files:**
- Create: `v2-go/audio/meta.go`
- Test: `v2-go/audio/meta_test.go`

- [ ] **Step 1: Write the failing tests**

Create `v2-go/audio/meta_test.go`:

```go
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

func TestMetaPathReplacesExtension(t *testing.T) {
	if got := metaPath("/a/b/jam_2026.wav"); got != "/a/b/jam_2026.meta.json" {
		t.Errorf("metaPath = %q, want %q", got, "/a/b/jam_2026.meta.json")
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./audio/ -run 'Meta' -v`
Expected: FAIL — `undefined: ReadMeta`, `undefined: MetaVersion`, `undefined: Meta`, `undefined: Trim`, `undefined: WriteMeta`, `undefined: metaPath`.

- [ ] **Step 3: Write the implementation**

Create `v2-go/audio/meta.go`:

```go
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

// metaPath returns the sidecar path for a take's wav path. Unexported to match
// its siblings previewPath and peaksPath in save.go, which do the identical job.
func metaPath(wav string) string { return strings.TrimSuffix(wav, ".wav") + ".meta.json" }

// ReadMeta loads a take's sidecar. A missing or unparseable sidecar yields
// defaults rather than an error, and the file is left alone: metadata is
// derived, disposable state, and losing it must never obscure the audio.
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
	// Normalize only the zero case, so a sidecar written before the version
	// field existed gets stamped while a NEWER one survives as a signal. Forcing
	// MetaVersion here would defeat the whole point of versioning the format.
	if got.Version == 0 {
		got.Version = MetaVersion
	}
	return got
}

// WriteMeta writes a take's sidecar atomically. The UI polls the take list
// every five seconds, so a half-written file would be read eventually; a temp
// file plus rename makes that impossible.
func WriteMeta(wav string, m Meta) error {
	// Refuse to rewrite a sidecar written by a newer build: this code cannot
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
	return os.Rename(name, p)
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./audio/ -run 'Meta' -v`
Expected: PASS — 5 tests.

- [ ] **Step 5: Commit**

```bash
git add v2-go/audio/meta.go v2-go/audio/meta_test.go
git commit -m "Add the per-take metadata sidecar

Writes go through a temp file and a rename so the list poll can never read a
half-written sidecar. A missing or corrupt file yields defaults and is left in
place: metadata is disposable, and losing it must not obscure the audio."
```

---

### Task 2: Merge the sidecar into ListTakes

**Files:**
- Modify: `v2-go/audio/save.go` (the `Take` struct, and `ListTakes`)
- Test: `v2-go/audio/save_test.go`

- [ ] **Step 1: Write the failing test**

Create `v2-go/audio/save_test.go`:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./audio/ -run 'ListTakes' -v`
Expected: FAIL — `takes[0].Label undefined (type Take has no field or method Label)`.

- [ ] **Step 3: Add the fields to `Take`**

In `v2-go/audio/save.go`, replace the `Take` struct with:

```go
// Take describes one saved recording.
type Take struct {
	Name       string    `json:"name"`
	SizeMB     float64   `json:"size_mb"`
	Duration   float64   `json:"duration_seconds"`
	Created    time.Time `json:"created"`
	Channels   int       `json:"channels"`
	SampleRate int       `json:"sample_rate"`
	HasPreview bool      `json:"has_preview"`
	HasPeaks   bool      `json:"has_peaks"`
	Preview    string    `json:"preview_name"`

	// From the sidecar. Name above is the filename; Label is what the user
	// called it.
	Label   string `json:"label"`
	Starred bool   `json:"starred"`
	Trim    *Trim  `json:"trim,omitempty"`
}
```

- [ ] **Step 4: Merge the sidecar in `ListTakes`**

In `v2-go/audio/save.go`, inside `ListTakes`, immediately after the
`if wi, err := ReadWAVInfo(full); err == nil { ... }` block and before
`out = append(out, t)`, insert:

```go
		m := ReadMeta(full)
		t.Label = m.Label
		t.Starred = m.Starred
		t.Trim = m.Trim
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./audio/ -run 'ListTakes' -v`
Expected: PASS — 3 tests. The `.meta.json` file is skipped by the existing
`filepath.Ext(e.Name()) != ".wav"` guard, so no change is needed for that.

- [ ] **Step 6: Commit**

```bash
git add v2-go/audio/save.go v2-go/audio/save_test.go
git commit -m "Merge the sidecar into the take list

Take.Name is the filename, so the human name is Label — one word cannot mean
two things in the same JSON object."
```

---

### Task 3: Sort starred takes first

**Files:**
- Modify: `v2-go/audio/save.go` (the `sort.Slice` at the end of `ListTakes`)
- Test: `v2-go/audio/save_test.go`

- [ ] **Step 1: Write the failing test**

Append to `v2-go/audio/save_test.go`:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./audio/ -run 'SortsStarredFirst' -v`
Expected: FAIL — `order = [jam_newest.wav jam_middle.wav jam_old_starred.wav], want [jam_old_starred.wav jam_newest.wav jam_middle.wav]`.

- [ ] **Step 3: Change the sort**

In `v2-go/audio/save.go`, replace:

```go
	sort.Slice(out, func(i, j int) bool { return out[i].Created.After(out[j].Created) })
```

with:

```go
	// Starred first, then newest. Starring is how a take is kept in reach once
	// newer ones have pushed it down the list.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Starred != out[j].Starred {
			return out[i].Starred
		}
		return out[i].Created.After(out[j].Created)
	})
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./audio/ -v`
Expected: PASS — all `Meta` and `ListTakes` tests.

- [ ] **Step 5: Commit**

```bash
git add v2-go/audio/save.go v2-go/audio/save_test.go
git commit -m "Sort starred takes first

Starring is how a take stays in reach once newer captures have pushed it down."
```

---

### Task 4: Remove the sidecar with the take

**Files:**
- Modify: `v2-go/audio/save.go` (`RemoveTake`)
- Test: `v2-go/audio/save_test.go`

- [ ] **Step 1: Write the failing test**

Append to `v2-go/audio/save_test.go`:

```go
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
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./audio/ -run 'RemoveTakeDeletesSidecar' -v`
Expected: FAIL — "sidecar still present after RemoveTake".

- [ ] **Step 3: Delete the sidecar too**

In `v2-go/audio/save.go`, replace `RemoveTake` with:

```go
// RemoveTake deletes a take and its sidecar files.
func RemoveTake(dir, name string) {
	base := filepath.Join(dir, filepath.Base(name))
	os.Remove(base)
	os.Remove(previewPath(base))
	os.Remove(peaksPath(base))
	os.Remove(metaPath(base))
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./audio/ -v`
Expected: PASS — all tests.

- [ ] **Step 5: Commit**

```bash
git add v2-go/audio/save.go v2-go/audio/save_test.go
git commit -m "Delete the sidecar along with its take

Otherwise a later take reusing the timestamp would inherit a stale label."
```

---

### Task 5: PATCH /api/take

**Files:**
- Modify: `v2-go/api/api.go` (`SetupRoutes`, plus a new handler)
- Test: `v2-go/api/api_test.go`

**Known hazard, accepted deliberately.** This handler is a read-modify-write:
`ReadMeta`, mutate, `WriteMeta`. Two browser tabs acting at once — one starring
while the other renames — lose one of the two changes. `WriteMeta` itself is
safe (unique temp name, atomic rename, last write wins); the race is at this
layer. Accepted because this is a single-user device on a LAN, and the cost of
a lost star is one more tap. If it ever bites, the fix is a package-level mutex
around the read-modify-write in this handler, not a change to `meta.go`.

- [ ] **Step 1: Write the failing tests**

Create `v2-go/api/api_test.go`:

```go
package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gabeduke/audio-dashcam/v2-go/audio"
	"github.com/gabeduke/audio-dashcam/v2-go/config"
	"github.com/gorilla/mux"
)

// newTestAPI builds an API over a temp takes directory. Capture and Saver are
// nil because the metadata handler never touches them; a test that needed
// audio would have to run on hardware.
func newTestAPI(t *testing.T) (*mux.Router, string) {
	t.Helper()
	dir := t.TempDir()
	a := New(&config.Config{OutputDir: dir}, nil, nil)
	r := mux.NewRouter()
	a.SetupRoutes(r)
	return r, dir
}

func writeTake(t *testing.T, dir, name string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte("not a real wav"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func patch(t *testing.T, r *mux.Router, file, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPatch, "/api/take?file="+file, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestPatchTakeSetsLabelAndStar(t *testing.T) {
	r, dir := newTestAPI(t)
	wav := writeTake(t, dir, "jam_a.wav")

	w := patch(t, r, "jam_a.wav", `{"label":"the good one","starred":true}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
	}

	m := audio.ReadMeta(wav)
	if m.Label != "the good one" || !m.Starred {
		t.Errorf("sidecar = %+v, want label set and starred", m)
	}
}

func TestPatchTakeMergesRatherThanReplaces(t *testing.T) {
	r, dir := newTestAPI(t)
	wav := writeTake(t, dir, "jam_a.wav")

	if w := patch(t, r, "jam_a.wav", `{"label":"keep me"}`); w.Code != http.StatusOK {
		t.Fatalf("first patch: %d", w.Code)
	}
	// Starring must not clear the label.
	if w := patch(t, r, "jam_a.wav", `{"starred":true}`); w.Code != http.StatusOK {
		t.Fatalf("second patch: %d", w.Code)
	}

	m := audio.ReadMeta(wav)
	if m.Label != "keep me" {
		t.Errorf("Label = %q, want it preserved across a starred-only patch", m.Label)
	}
	if !m.Starred {
		t.Error("Starred = false, want true")
	}
}

func TestPatchTakeExplicitNullClearsTrim(t *testing.T) {
	r, dir := newTestAPI(t)
	wav := writeTake(t, dir, "jam_a.wav")

	if w := patch(t, r, "jam_a.wav", `{"trim":{"start_frame":10,"end_frame":20}}`); w.Code != http.StatusOK {
		t.Fatalf("set trim: %d (%s)", w.Code, w.Body.String())
	}
	if m := audio.ReadMeta(wav); m.Trim == nil {
		t.Fatal("trim was not set")
	}

	if w := patch(t, r, "jam_a.wav", `{"trim":null}`); w.Code != http.StatusOK {
		t.Fatalf("clear trim: %d", w.Code)
	}
	if m := audio.ReadMeta(wav); m.Trim != nil {
		t.Errorf("Trim = %+v, want nil after an explicit null", m.Trim)
	}
}

func TestPatchTakeOmittedTrimIsLeftAlone(t *testing.T) {
	r, dir := newTestAPI(t)
	wav := writeTake(t, dir, "jam_a.wav")

	if w := patch(t, r, "jam_a.wav", `{"trim":{"start_frame":10,"end_frame":20}}`); w.Code != http.StatusOK {
		t.Fatalf("set trim: %d", w.Code)
	}
	if w := patch(t, r, "jam_a.wav", `{"starred":true}`); w.Code != http.StatusOK {
		t.Fatalf("star: %d", w.Code)
	}

	m := audio.ReadMeta(wav)
	if m.Trim == nil || m.Trim.StartFrame != 10 || m.Trim.EndFrame != 20 {
		t.Errorf("Trim = %+v, want it untouched when the field is absent", m.Trim)
	}
}

func TestPatchTakeRejectsBackwardsTrim(t *testing.T) {
	r, dir := newTestAPI(t)
	writeTake(t, dir, "jam_a.wav")

	w := patch(t, r, "jam_a.wav", `{"trim":{"start_frame":20,"end_frame":20}}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 for a zero-length trim", w.Code)
	}
}

func TestPatchTakeUnknownFileIs404(t *testing.T) {
	r, _ := newTestAPI(t)
	w := patch(t, r, "jam_missing.wav", `{"starred":true}`)
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}

func TestPatchTakeRejectsTraversal(t *testing.T) {
	r, _ := newTestAPI(t)
	for _, bad := range []string{"..%2Fescape.wav", "sub%2Fjam.wav", "jam_a.txt"} {
		w := patch(t, r, bad, `{"starred":true}`)
		if w.Code != http.StatusBadRequest {
			t.Errorf("file=%q: status = %d, want 400", bad, w.Code)
		}
	}
}

func TestPatchTakeTrimsAndCapsLabel(t *testing.T) {
	r, dir := newTestAPI(t)
	wav := writeTake(t, dir, "jam_a.wav")

	long := strings.Repeat("x", 500)
	if w := patch(t, r, "jam_a.wav", `{"label":"  spaced  "}`); w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if m := audio.ReadMeta(wav); m.Label != "spaced" {
		t.Errorf("Label = %q, want surrounding whitespace trimmed", m.Label)
	}

	body, _ := json.Marshal(map[string]string{"label": long})
	if w := patch(t, r, "jam_a.wav", string(body)); w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	if m := audio.ReadMeta(wav); len(m.Label) != maxLabelLen {
		t.Errorf("len(Label) = %d, want it capped at %d", len(m.Label), maxLabelLen)
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

Run: `go test ./api/ -v`
Expected: FAIL — `undefined: maxLabelLen`, and every request returns 405 because no `PATCH /api/take` route exists.

- [ ] **Step 3: Add the route**

In `v2-go/api/api.go`, inside `SetupRoutes`, add after the `/api/delete` line:

```go
	r.HandleFunc("/api/take", a.handleTakePatch).Methods(http.MethodPatch)
```

- [ ] **Step 4: Write the handler**

In `v2-go/api/api.go`, add at the end of the file:

```go
// maxLabelLen caps a user-supplied take label. Long enough for a real name,
// short enough that the sidecar cannot be used as storage.
const maxLabelLen = 120

// handleTakePatch merges fields into a take's sidecar. It is a merge, not a
// replace: pointers (and a RawMessage for trim) distinguish "field absent"
// from "field set to its zero value", so starring a take cannot silently clear
// its label.
func (a *API) handleTakePatch(w http.ResponseWriter, r *http.Request) {
	name, err := a.safeTakeName(r.URL.Query().Get("file"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	wav := filepath.Join(a.cfg.OutputDir, name)
	if _, err := os.Stat(wav); err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}

	var body struct {
		Label   *string         `json:"label"`
		Starred *bool           `json:"starred"`
		Trim    json.RawMessage `json:"trim"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}

	m := audio.ReadMeta(wav)

	if body.Label != nil {
		label := strings.TrimSpace(*body.Label)
		if len(label) > maxLabelLen {
			label = label[:maxLabelLen]
		}
		m.Label = label
	}
	if body.Starred != nil {
		m.Starred = *body.Starred
	}
	if body.Trim != nil {
		if string(body.Trim) == "null" {
			m.Trim = nil
		} else {
			var tr audio.Trim
			if err := json.Unmarshal(body.Trim, &tr); err != nil {
				writeErr(w, http.StatusBadRequest, "invalid trim")
				return
			}
			if tr.StartFrame < 0 || tr.EndFrame <= tr.StartFrame {
				writeErr(w, http.StatusBadRequest, "trim end_frame must be greater than start_frame")
				return
			}
			m.Trim = &tr
		}
	}

	if err := audio.WriteMeta(wav, m); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, m)
}
```

- [ ] **Step 5: Run the tests to verify they pass**

Run: `go test ./api/ -v`
Expected: PASS — 8 tests.

The imports `encoding/json`, `net/http`, `os`, `path/filepath`, `strings` and
the `audio` package are all already imported by `api.go`; if `go build` reports
an unused or missing import, fix it before committing.

- [ ] **Step 6: Verify the whole package still builds and passes**

Run: `go build ./... && go test ./... `
Expected: build succeeds; all tests pass.

- [ ] **Step 7: Commit**

```bash
git add v2-go/api/api.go v2-go/api/api_test.go
git commit -m "Add PATCH /api/take for labels, stars and trim bounds

A merge rather than a replace: pointers and a RawMessage distinguish an absent
field from one set to its zero value, so starring a take cannot clear its
label, and an explicit null is the only way to clear a trim."
```

---

### Task 6: Star toggle in the takes list

**Files:**
- Modify: `v2-go/static/lib/takes.js`

There is no JS test harness in this repo, and adding one is out of scope for
this plan. Tasks 6-8 are verified by the manual checks in Task 8.

- [ ] **Step 1: Add the `patchTake` helper**

In `v2-go/static/lib/takes.js`, add this method to the class, immediately
before `async refresh() {`:

```js
  // Same shape as confirmDelete below: drop the ETag and re-fetch. Metadata
  // writes are rare and user-initiated, the server owns ordering, and starring
  // reorders the list.
  async patchTake(name, patch) {
    const res = await fetch(`/api/take?file=${encodeURIComponent(name)}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(patch),
    });
    if (!res.ok) {
      const msg = await res.json().then((j) => j.error).catch(() => res.statusText);
      throw new Error(msg);
    }
    this.etag = null;
    await this.refresh();
  }
```

- [ ] **Step 2: Add the star button to the row markup**

In `createRow`, replace the `<div class="take-head">` block:

```js
      <div class="take-head">
        <span class="take-name"></span>
        <span class="take-meta"></span>
      </div>
```

with:

```js
      <div class="take-head">
        <button class="star" type="button" aria-pressed="false" aria-label="Star this take">★</button>
        <button class="take-name" type="button" title="Rename"></button>
        <input class="take-name-input" type="text" maxlength="120" hidden>
        <span class="take-meta"></span>
      </div>
```

- [ ] **Step 3: Wire the star into the row object**

In `createRow`, in the `const row = {` object literal, replace:

```js
      nameEl: el.querySelector('.take-name'),
```

with:

```js
      nameEl: el.querySelector('.take-name'),
      nameInput: el.querySelector('.take-name-input'),
      starBtn: el.querySelector('.star'),
      editing: false,
```

- [ ] **Step 4: Add the star click handler**

In `createRow`, after the existing `row.delBtn.addEventListener(...)` line, add:

```js
    row.starBtn.addEventListener('click', async () => {
      const next = !row.data.starred;
      row.starBtn.disabled = true;
      try {
        await this.patchTake(row.name, { starred: next });
      } catch (err) {
        this.onToast?.(`Could not star: ${err.message}`, 'bad');
      } finally {
        row.starBtn.disabled = false;
      }
    });
```

- [ ] **Step 5: Reflect the star state in `updateRow`**

In `updateRow`, immediately after `row.data = t;`, add:

```js
    row.starBtn.setAttribute('aria-pressed', t.starred ? 'true' : 'false');
    row.starBtn.classList.toggle('on', !!t.starred);
```

- [ ] **Step 6: Verify in the browser**

Deploy with `./deploy.sh`, open the UI, and tap a star. Expected: the star
fills, and the take jumps to the top of the list on the refresh that follows.
Reload the page — it stays starred and stays first.

- [ ] **Step 7: Commit**

```bash
git add v2-go/static/lib/takes.js
git commit -m "Add a star toggle to each take"
```

---

### Task 7: Inline rename

**Files:**
- Modify: `v2-go/static/lib/takes.js`

- [ ] **Step 1: Show the label, falling back to the timestamp**

In `updateRow`, replace:

```js
    row.nameEl.textContent = t.name.replace(/^jam_|\.wav$/g, '');
```

with:

```js
    // A take with no label still needs something to show, and the timestamp is
    // the only thing it has. Skip while the user is mid-edit so a poll cannot
    // overwrite what they are typing.
    if (!row.editing) {
      const stamp = t.name.replace(/^jam_|\.wav$/g, '');
      row.nameEl.textContent = t.label || stamp;
      row.nameEl.classList.toggle('unlabelled', !t.label);
    }
```

- [ ] **Step 2: Add the edit handlers**

In `createRow`, after the star handler added in Task 6, add:

```js
    const beginEdit = () => {
      row.editing = true;
      row.nameInput.value = row.data.label || '';
      row.nameInput.placeholder = row.data.name.replace(/^jam_|\.wav$/g, '');
      row.nameEl.hidden = true;
      row.nameInput.hidden = false;
      row.nameInput.focus();
      row.nameInput.select();
    };

    const endEdit = async (commit) => {
      if (!row.editing) return;
      row.editing = false;
      row.nameInput.hidden = true;
      row.nameEl.hidden = false;

      if (!commit) return;
      const label = row.nameInput.value.trim();
      if (label === (row.data.label || '')) return;

      try {
        await this.patchTake(row.name, { label });
      } catch (err) {
        this.onToast?.(`Could not rename: ${err.message}`, 'bad');
      }
    };

    row.nameEl.addEventListener('click', beginEdit);
    row.nameInput.addEventListener('blur', () => endEdit(true));
    row.nameInput.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') {
        e.preventDefault();
        row.nameInput.blur();
      } else if (e.key === 'Escape') {
        e.preventDefault();
        endEdit(false);
      }
    });
```

- [ ] **Step 3: Verify in the browser**

Deploy with `./deploy.sh`. Expected:
- Tapping a name turns it into a focused, selected input.
- Enter saves and the new name shows; Escape reverts with nothing saved.
- An empty name falls back to the timestamp, shown dimmed.
- Typing for longer than one poll interval (5s) does not lose the text.

- [ ] **Step 4: Commit**

```bash
git add v2-go/static/lib/takes.js
git commit -m "Rename a take inline

Edits are held while the list polls so a refresh cannot overwrite what is
being typed."
```

---

### Task 8: Styling and final verification

**Files:**
- Modify: `v2-go/static/styles.css`

- [ ] **Step 1: Add the styles**

Append to `v2-go/static/styles.css`:

```css
/* ---------- take identity ---------- */

.take-head {
  display: flex;
  align-items: center;
  gap: 8px;
}

.star {
  flex: none;
  width: var(--tap);
  height: var(--tap);
  margin: -12px 0 -12px -12px;   /* full tap target without bloating the row */
  background: none;
  border: 0;
  padding: 0;
  font-size: 20px;
  line-height: 1;
  color: var(--ink-faint);
  cursor: pointer;
  transition: color .15s ease, transform .15s ease;
}

.star:hover  { color: var(--ink-dim); }
.star.on     { color: var(--warn); }
.star:active { transform: scale(.88); }
.star:disabled { opacity: .5; cursor: default; }

.take-name {
  flex: 1 1 auto;
  min-width: 0;
  background: none;
  border: 0;
  padding: 4px 0;
  font: inherit;
  color: var(--ink);
  text-align: left;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  cursor: text;
}

.take-name.unlabelled { color: var(--ink-dim); font-family: var(--mono); font-size: 13px; }

.take-name-input {
  flex: 1 1 auto;
  min-width: 0;
  background: var(--panel-2);
  border: 1px solid var(--accent);
  border-radius: 8px;
  padding: 6px 8px;
  font: inherit;
  color: var(--ink);
}

.take-name-input:focus { outline: none; }
```

- [ ] **Step 2: Deploy**

```bash
cd /Users/gabeduke/projects/audio-dashcam && ./deploy.sh
```

Expected: sync, build on the Pi, restart, and a `/api/status` line printed.

- [ ] **Step 3: Run the full test suite on the Pi**

```bash
ssh "$DASHCAM_HOST" 'cd ~/audio-dashcam/v2-go && go test ./...'
```

Expected: `ok` for both `audio` and `api`.

- [ ] **Step 4: Manual verification**

Confirm each, on a phone at ~390px:

- [ ] Existing takes recorded before this change still list, with the timestamp shown dimmed and no sidecar on disk.
- [ ] Tapping a star fills it and moves the take to the top; reload confirms it persisted.
- [ ] Unstarring returns it to date order.
- [ ] Renaming shows the new label; reload confirms it persisted.
- [ ] Escape during a rename discards the edit.
- [ ] An empty label falls back to the dimmed timestamp.
- [ ] Deleting a take leaves no `.meta.json` behind: `ssh "$DASHCAM_HOST" 'ls ~/audio-dashcam/jam_saves/'`
- [ ] Playback still survives a list refresh — start a preview, wait through two polls, confirm it does not cut out. (This is the bug the row-patching design exists to prevent; the new markup must not regress it.)
- [ ] Star and name are both comfortably tappable; no horizontal overflow.

- [ ] **Step 5: Commit**

```bash
git add v2-go/static/styles.css
git commit -m "Style the star and inline rename"
```

- [ ] **Step 6: Update the spec's field name and push**

In `docs/superpowers/specs/2026-09-07-take-triage-trim-design.md`, change the
sidecar schema's `"name"` key to `"label"` and note that `Take.Name` remains the
filename.

```bash
git add docs/superpowers/specs/2026-09-07-take-triage-trim-design.md
git commit -m "Spec: the sidecar's human name is label, not name

Take.Name is already the filename and is the row key in takes.js; one word
cannot mean two things in the same object."
git push origin master
```

---

## Done when

- `go test ./...` passes on the Pi.
- Every box in Task 8 Step 4 is checked.
- The takes list shows labels and stars, starred takes sort first, and both
  survive a reload.
- No change to capture, saving, preview, or peaks behaviour.
