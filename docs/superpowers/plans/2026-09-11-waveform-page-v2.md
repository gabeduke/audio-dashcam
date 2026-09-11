# Waveform Page v2 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rework the waveform page so a musician on a phone can drag out a region, hear it loop at once, and share it as an MP3 in one tap, with fine controls hidden behind a disclosure.

**Architecture:** One new server endpoint (`GET /api/render`) streams an ffmpeg MP3 of a frame range with the cut's declick fades, writing nothing to disk. On the client, a new overview strip owns navigation so the main waveform's one-finger drag can become region selection; the loop toggle disappears because a region always loops; a Share button fetches the render into a blob and hands it to the phone's share sheet. The v1 server primitives, tile cache, geometry, and clock are untouched.

**Tech Stack:** Go stdlib + gorilla/mux (existing), ffmpeg on the Pi (existing dependency for previews), vanilla ES modules, Canvas 2D, Web Share API, `node --test`.

**Spec:** `docs/superpowers/specs/2026-09-11-waveform-page-v2-design.md`

## Global Constraints

- Render fades are **3ms** each edge via `afade` with `d=0.003`, matching `FadeFrames`; trimming is **sample-exact** via `atrim=start_sample=<from>:end_sample=<to>` followed by `asetpts=PTS-STARTPTS`, never `-ss`/`-t`.
- Render cap: `to - from <= 600 * sampleRate` (10 minutes) → 400 "region longer than 10 minutes". Other validation identical to `/api/slice`: `safeTakeName`, integer `0 <= from < to <= frames`, 404 missing take, 400 non-32-bit.
- Render output: `libmp3lame` at `128k`, `-f mp3` to stdout, run under `nice -n 10`, killed when the request context ends. Headers: `Content-Type: audio/mpeg`, `Content-Disposition: inline; filename="<name>"`, `Cache-Control: no-store`, no `Content-Length`.
- Render filename: `<label or stem> <m.ss>-<m.ss>.mp3`; whole take is `<label or stem>.mp3`; the base is reduced to letters, digits, space, dash, underscore, dot.
- Overview window is never narrower than **24px**. Minimum region on drag release is `2*fade+1` frames (`minLen`, already computed in `page.js` and `view.js`); shorter drags create nothing.
- One-finger drag on empty waveform **selects**; it never pans. Panning: overview drag, two-finger drag on the main waveform, shift-wheel or horizontal wheel on desktop. Plain wheel zooms.
- No Loop toggle: a non-null region ⇒ `applyLoop(region)`; null ⇒ `applyLoop(null)`.
- `web/static/sw.js`: `SHELL` gains `/lib/wave/overview.js` and `/lib/wave/share.js`; `CACHE` bumps `hindsight-shell-v3` → `hindsight-shell-v4`.
- JS tests run with `node --test 'web/static/lib/wave/*.test.js'` (quoted glob). Go: `gofmt -l .`, `go vet ./...`, `go test -race ./...` before every Go commit.
- Commit messages end with the attribution trailer used by this repo's recent commits.
- Branch `waveform-v2` off `master` (`0ebe4f6` or later). Run the app locally with `RING_SECONDS=120 CGO_ENABLED=0 PORT=5099 OUTPUT_DIR=<tmp> go run ./cmd/hindsight --demo`; `ffmpeg` is on this Mac's PATH, so renders work locally.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/audio/render.go` (create) | `RenderArgs` (pure ffmpeg argument list), `RenderFilename` (pure), `RenderMP3` (runs ffmpeg, streams stdout) |
| `internal/audio/render_test.go` (create) | argument list golden tests, filename tests, integration test skipped without ffmpeg |
| `internal/api/api.go` (modify) | `handleRender`, route |
| `internal/api/api_test.go` (modify) | validation table, headers, integration |
| `docs/api.md` (modify) | the route |
| `web/static/lib/wave/overview.js` (create) | pure mapping functions + `Overview` class (second canvas) |
| `web/static/lib/wave/overview.test.js` (create) | mapping tests |
| `web/static/lib/wave/view.js` (modify) | `select` gesture replaces `pan`; wheel semantics flip |
| `web/static/lib/wave/share.js` (create) | `looksLikeMP3`, `fmtRegionText`, `shareOrDownload` |
| `web/static/lib/wave/share.test.js` (create) | tests for the two pure functions |
| `web/static/lib/wave/page.js` (modify) | overview wiring, implicit loop, action row, Fine tune, Share |
| `web/static/wave.html` (modify) | new layout |
| `web/static/styles.css` (modify) | overview strip, action row, fixed-width region text, disclosure |
| `web/static/sw.js` (modify) | SHELL + cache bump |
| `README.md`, `docs/development.md` (modify) | page description, checklist |

---

### Task 1: Render argument builder and filename

**Files:**
- Create: `internal/audio/render.go`
- Test: `internal/audio/render_test.go`

**Interfaces:**
- Consumes: `config.Config.SaveChannels` (`[]int`, zero-based), `WAVInfo` (`Channels`, `SampleRate`, `Frames()`), `ReadWAVInfo`, `ErrBitDepth`, `ErrRange`.
- Produces:
  - `const MaxRenderSeconds = 600`
  - `var ErrRenderTooLong = errors.New("region longer than 10 minutes")`
  - `func RenderArgs(wavPath string, info WAVInfo, saveChannels []int, from, to int64) []string` — the full ffmpeg argument list (without the `ffmpeg` word or `nice`).
  - `func RenderFilename(base string, from, to int64, sampleRate int, whole bool) string`.
  - `func RenderMP3(ctx context.Context, w io.Writer, saveChannels []int, wavPath string, from, to int64) error` — validates like `WriteSlice16` (bit depth, range, cap), then runs ffmpeg and copies stdout to `w`.

- [ ] **Step 1: Write the failing tests**

```go
// internal/audio/render_test.go
package audio

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderArgsStereoTake(t *testing.T) {
	info := WAVInfo{Channels: 2, SampleRate: 48000, BitsPerSample: 32, DataBytes: 48000 * 2 * 4 * 10}
	got := RenderArgs("/takes/jam_a.wav", info, []int{0, 1}, 48000, 48000*4)
	want := []string{
		"-hide_banner", "-loglevel", "error", "-i", "/takes/jam_a.wav",
		"-af", "atrim=start_sample=48000:end_sample=192000,asetpts=PTS-STARTPTS,afade=t=in:st=0:d=0.003,afade=t=out:st=2.997:d=0.003",
		"-c:a", "libmp3lame", "-b:a", "128k", "-f", "mp3", "-",
	}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Errorf("args =\n  %s\nwant\n  %s", strings.Join(got, " "), strings.Join(want, " "))
	}
}

func TestRenderArgsMultichannelTakeAppendsPan(t *testing.T) {
	info := WAVInfo{Channels: 8, SampleRate: 48000, BitsPerSample: 32}
	got := RenderArgs("/t.wav", info, []int{0, 1}, 0, 48000)
	af := got[6]
	if !strings.HasSuffix(af, ",pan=stereo|c0=c0|c1=c1") {
		t.Errorf("pan not appended after the fades: %q", af)
	}
	if !strings.Contains(af, "afade=t=out:st=0.997:d=0.003,pan=") {
		t.Errorf("fade-out start for a 1s region should be 0.997: %q", af)
	}
}

func TestRenderArgsMonoTakeUpmixes(t *testing.T) {
	info := WAVInfo{Channels: 1, SampleRate: 48000, BitsPerSample: 32}
	got := RenderArgs("/t.wav", info, []int{0}, 0, 48000)
	if !strings.HasSuffix(got[6], ",pan=stereo|c0=c0|c1=c0") {
		t.Errorf("mono should upmix like MakePreview: %q", got[6])
	}
}

func TestRenderFilename(t *testing.T) {
	cases := []struct {
		base     string
		from, to int64
		whole    bool
		want     string
	}{
		{"the good one", 48000 * 12, 48000*41 + 24000, false, "the good one 0.12-0.41.mp3"},
		{"jam_2026-09-10_103541", 0, 48000 * 75, false, "jam_2026-09-10_103541 0.00-1.15.mp3"},
		{"the good one", 0, 0, true, "the good one.mp3"},
		{`we're "live"/tonight?`, 0, 0, true, "were livetonight.mp3"},
		{"   ", 0, 0, true, "take.mp3"},
	}
	for _, c := range cases {
		if got := RenderFilename(c.base, c.from, c.to, 48000, c.whole); got != c.want {
			t.Errorf("RenderFilename(%q) = %q, want %q", c.base, got, c.want)
		}
	}
}

func TestRenderMP3Validates(t *testing.T) {
	p := filepath.Join(t.TempDir(), "jam_r.wav")
	if _, err := WriteWAV(p, make([]int32, 48000*2*2), 2, []int{0, 1}, 48000); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := RenderMP3(context.Background(), &buf, []int{0, 1}, p, 0, 48000*2+1); !errors.Is(err, ErrRange) {
		t.Errorf("past end: %v, want ErrRange", err)
	}
	if err := RenderMP3(context.Background(), &buf, []int{0, 1}, p, 10, 10); !errors.Is(err, ErrRange) {
		t.Errorf("empty: %v, want ErrRange", err)
	}
	// Cap: a 2s file cannot exceed 600s, so fake the check with a long info
	// through the exported constant instead -- covered in the API test with
	// a long take. Here only the bit-depth guard remains:
	p16 := filepath.Join(t.TempDir(), "jam_16.wav")
	write16bitWAV(t, p16, 1000)
	if err := RenderMP3(context.Background(), &buf, []int{0, 1}, p16, 0, 100); !errors.Is(err, ErrBitDepth) {
		t.Errorf("16-bit: %v, want ErrBitDepth", err)
	}
	if buf.Len() != 0 {
		t.Error("a rejected render must write nothing")
	}
}

// TestRenderMP3ProducesAnMP3 needs ffmpeg. It is skipped where ffmpeg is
// absent (CI's ubuntu runner has it via apt in ci.yml; see Task 2).
func TestRenderMP3ProducesAnMP3(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	p := filepath.Join(t.TempDir(), "jam_r.wav")
	data := make([]int32, 48000*2*2)
	for i := range data {
		data[i] = int32((i%97)-48) << 20 // audible-ish noise so lame has work to do
	}
	if _, err := WriteWAV(p, data, 2, []int{0, 1}, 48000); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := RenderMP3(context.Background(), &buf, []int{0, 1}, p, 4800, 48000*2-4800); err != nil {
		t.Fatalf("RenderMP3: %v", err)
	}
	b := buf.Bytes()
	if len(b) < 4096 {
		t.Fatalf("only %d bytes", len(b))
	}
	if !(bytes.HasPrefix(b, []byte("ID3")) || (b[0] == 0xFF && b[1]&0xE0 == 0xE0)) {
		t.Errorf("body does not start with ID3 or an MP3 sync word: % x", b[:4])
	}
}
```

`write16bitWAV` already exists in `internal/audio/slice_test.go` from the slice work (a hand-built 44-byte header with bits=16); reuse it. If its name differs, use the existing helper's name.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/audio -run 'TestRender' -v`
Expected: compile failure — `RenderArgs`, `RenderFilename`, `RenderMP3` undefined.

- [ ] **Step 3: Create `render.go`**

```go
// internal/audio/render.go
package audio

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"unicode"
)

// MaxRenderSeconds caps a share render. Ten minutes at 128 kbps is ~9.4MB,
// past what any messaging app accepts anyway.
const MaxRenderSeconds = 600

// ErrRenderTooLong is a range over MaxRenderSeconds.
var ErrRenderTooLong = errors.New("region longer than 10 minutes")

// renderFadeSeconds is the declick fade, the same 3ms FadeFrames gives a cut.
const renderFadeSeconds = 0.003

// RenderArgs builds ffmpeg's argument list (without the program name) for an
// MP3 of frames [from, to). The trim is a sample-exact filter rather than
// -ss/-t, so a render and a cut of the same region contain the same frames,
// and the two afades reproduce the cut's linear 3ms declick. When the take
// has more than two channels the configured pair is panned out, exactly as
// MakePreview does; a mono take is upmixed. Output goes to stdout.
func RenderArgs(wavPath string, info WAVInfo, saveChannels []int, from, to int64) []string {
	dur := float64(to-from) / float64(info.SampleRate)
	chain := fmt.Sprintf(
		"atrim=start_sample=%d:end_sample=%d,asetpts=PTS-STARTPTS,afade=t=in:st=0:d=%g,afade=t=out:st=%s:d=%g",
		from, to, renderFadeSeconds, trimFloat(dur-renderFadeSeconds), renderFadeSeconds,
	)
	if info.Channels > 2 {
		l, r := 0, 0
		if len(saveChannels) > 0 {
			l, r = saveChannels[0], saveChannels[0]
		}
		if len(saveChannels) > 1 {
			r = saveChannels[1]
		}
		chain += fmt.Sprintf(",pan=stereo|c0=c%d|c1=c%d", l, r)
	} else if info.Channels == 1 {
		chain += ",pan=stereo|c0=c0|c1=c0"
	}
	return []string{
		"-hide_banner", "-loglevel", "error", "-i", wavPath,
		"-af", chain,
		"-c:a", "libmp3lame", "-b:a", "128k", "-f", "mp3", "-",
	}
}

// trimFloat formats a seconds value without trailing zeros, so the golden
// argument list reads "2.997" rather than "2.997000".
func trimFloat(f float64) string {
	s := fmt.Sprintf("%.3f", f)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

// RenderFilename names a shared MP3: the take's label or stem, reduced to
// filename-safe characters, plus the region's span in m.ss, or nothing for
// the whole take. Messages shows the name, so it should read like a title.
func RenderFilename(base string, from, to int64, sampleRate int, whole bool) string {
	safe := strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' || r == '-' || r == '_' || r == '.' {
			return r
		}
		return -1
	}, base)
	safe = strings.TrimSpace(safe)
	if safe == "" {
		safe = "take"
	}
	if whole {
		return safe + ".mp3"
	}
	return fmt.Sprintf("%s %s-%s.mp3", safe, mmss(from, sampleRate), mmss(to, sampleRate))
}

func mmss(frame int64, sampleRate int) string {
	s := frame / int64(sampleRate)
	return fmt.Sprintf("%d.%02d", s/60, s%60)
}

// RenderMP3 streams an MP3 of frames [from, to) to w straight from ffmpeg's
// stdout. Nothing touches disk. Validation mirrors WriteSlice16 so the
// handler can map errors the same way. ffmpeg is killed when ctx ends, which
// is how a client that closes the tab stops a ten-minute encode.
func RenderMP3(ctx context.Context, w io.Writer, saveChannels []int, wavPath string, from, to int64) error {
	info, err := ReadWAVInfo(wavPath)
	if err != nil {
		return err
	}
	if info.BitsPerSample != 32 {
		return ErrBitDepth
	}
	if from < 0 || to <= from || to > info.Frames() {
		return fmt.Errorf("%w: [%d, %d) of %d frames", ErrRange, from, to, info.Frames())
	}
	if to-from > int64(MaxRenderSeconds*info.SampleRate) {
		return ErrRenderTooLong
	}
	args := append([]string{"-n", "10", "ffmpeg"}, RenderArgs(wavPath, info, saveChannels, from, to)...)
	cmd := exec.CommandContext(ctx, "nice", args...)
	cmd.Stdout = w
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ffmpeg: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
```

Check: `trimFloat(2.997)` → `"2.997"`; `trimFloat(0.997)` → `"0.997"`; a 3ms region would give `st=0`, fine. `%g` for `0.003` prints `0.003`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/audio -run 'TestRender' -v`
Expected: PASS ×6 (the ffmpeg one runs on this Mac).

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go vet ./... && go test -race ./internal/audio
git add internal/audio/render.go internal/audio/render_test.go
git commit -m "Render a faded MP3 of a take's frame range through ffmpeg"
```

---

### Task 2: `GET /api/render`

**Files:**
- Modify: `internal/api/api.go` (route near `/api/slice` ~line 81; handler after `handleSlice`)
- Test: `internal/api/api_test.go`
- Modify: `docs/api.md`, `.github/workflows/ci.yml`

**Interfaces:**
- Consumes: `audio.RenderMP3`, `audio.RenderFilename`, `audio.ErrRenderTooLong`, `audio.ErrRange`, `audio.ErrBitDepth`, `audio.ReadMeta` (for the label), `a.cfg.SaveChannels`, `safeTakeName`, `writeErr`.
- Produces: `GET /api/render?file=&from=&to=` → `200 audio/mpeg` stream; `Content-Disposition: inline; filename="..."`; 400/404 per Global Constraints.

- [ ] **Step 1: Write the failing tests**

```go
func TestRenderValidation(t *testing.T) {
	r, dir := newTestAPI(t)
	writeRealTake(t, dir, "jam_r.wav", 48000)
	cases := map[string]struct {
		url  string
		want int
	}{
		"missing take":  {"/api/render?file=jam_nope.wav&from=0&to=100", http.StatusNotFound},
		"no file":       {"/api/render?from=0&to=100", http.StatusBadRequest},
		"traversal":     {"/api/render?file=../jam_r.wav&from=0&to=100", http.StatusBadRequest},
		"non-integer":   {"/api/render?file=jam_r.wav&from=a&to=100", http.StatusBadRequest},
		"inverted":      {"/api/render?file=jam_r.wav&from=100&to=50", http.StatusBadRequest},
		"past end":      {"/api/render?file=jam_r.wav&from=0&to=48001", http.StatusBadRequest},
		"partial":       {"/api/render?file=jam_r.wav&from=0", http.StatusBadRequest},
	}
	for name, c := range cases {
		if w := do(t, r, http.MethodGet, c.url); w.Code != c.want {
			t.Errorf("%s: status = %d, want %d (%s)", name, w.Code, c.want, w.Body.String())
		}
	}
}

func TestRenderRejectsOverTheCap(t *testing.T) {
	r, dir := newTestAPI(t)
	// 601 seconds of silence: 115MB on disk is too much for a unit test, so
	// write a WAV header claiming 601s and no data -- RenderMP3 checks the
	// cap before ffmpeg ever runs, and ReadWAVInfo only reads the header.
	writeHeaderOnlyWAV(t, filepath.Join(dir, "jam_long.wav"), 48000*601)
	w := do(t, r, http.MethodGet, "/api/render?file=jam_long.wav&from=0&to="+strconv.Itoa(48000*601))
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "10 minutes") {
		t.Errorf("status = %d body = %s", w.Code, w.Body.String())
	}
}

func TestRenderRejectsANonThirtyTwoBitTake(t *testing.T) {
	r, dir := newTestAPI(t)
	write16bitWAV(t, filepath.Join(dir, "jam_16.wav"), 1000)
	w := do(t, r, http.MethodGet, "/api/render?file=jam_16.wav&from=0&to=100")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); strings.HasPrefix(ct, "audio/") {
		t.Errorf("audio Content-Type on a rejected render: %q", ct)
	}
}

func TestRenderStreamsAnMP3WithAName(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	r, dir := newTestAPI(t)
	writeRealTake(t, dir, "jam_r.wav", 48000*2)
	if err := audio.WriteMeta(filepath.Join(dir, "jam_r.wav"), audio.Meta{Version: audio.MetaVersion, Label: "the good one"}); err != nil {
		t.Fatal(err)
	}
	w := do(t, r, http.MethodGet, "/api/render?file=jam_r.wav&from=48000&to=72000")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "audio/mpeg" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); cd != `inline; filename="the good one 0.01-0.01.mp3"` {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q", cc)
	}
	if w.Header().Get("Content-Length") != "" {
		t.Error("Content-Length must be absent on a stream")
	}
	b := w.Body.Bytes()
	if len(b) < 1024 || !(bytes.HasPrefix(b, []byte("ID3")) || (b[0] == 0xFF && b[1]&0xE0 == 0xE0)) {
		t.Errorf("body is not an MP3 (%d bytes)", len(b))
	}
	// The whole take gets the bare name.
	w = do(t, r, http.MethodGet, "/api/render?file=jam_r.wav&from=0&to=96000")
	if cd := w.Header().Get("Content-Disposition"); cd != `inline; filename="the good one.mp3"` {
		t.Errorf("whole-take Content-Disposition = %q", cd)
	}
}
```

Add a helper in `api_test.go` if none exists:

```go
// writeHeaderOnlyWAV writes a canonical 44-byte 32-bit stereo header that
// claims `frames` frames with no data behind it. Good for tests that stop at
// the header (cap checks) and never read samples.
func writeHeaderOnlyWAV(t *testing.T, path string, frames int64) {
	t.Helper()
	dataBytes := uint32(frames * 2 * 4)
	b := make([]byte, 44)
	le := binary.LittleEndian
	copy(b[0:4], "RIFF"); le.PutUint32(b[4:8], dataBytes+36); copy(b[8:12], "WAVE")
	copy(b[12:16], "fmt "); le.PutUint32(b[16:20], 16); le.PutUint16(b[20:22], 1)
	le.PutUint16(b[22:24], 2); le.PutUint32(b[24:28], 48000); le.PutUint32(b[28:32], 48000*2*4)
	le.PutUint16(b[32:34], 8); le.PutUint16(b[34:36], 32)
	copy(b[36:40], "data"); le.PutUint32(b[40:44], dataBytes)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		t.Fatal(err)
	}
}
```

`write16bitWAV` exists in `api_test.go` from the slice bit-depth test; if it lives in the audio package's tests instead, add an equivalent here. Add `"bytes"`, `"encoding/binary"`, `"os/exec"` to the test imports as needed.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/api -run TestRender -v`
Expected: 404 on every request (no route).

- [ ] **Step 3: Route and handler**

```go
	r.HandleFunc("/api/render", a.handleRender).Methods(http.MethodGet)
```

```go
// handleRender streams an MP3 of [from, to) for the share sheet. It is the
// preview's encoder pointed at a region: same bitrate, same channel pan, and
// the cut's 3ms fades, so what gets texted is what a cut would sound like.
// Nothing is written to disk and nothing is cached; a render is a few
// seconds of the Pi's CPU and that is all it costs.
func (a *API) handleRender(w http.ResponseWriter, r *http.Request) {
	name, err := a.safeTakeName(r.URL.Query().Get("file"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	q := r.URL.Query()
	from, err1 := strconv.ParseInt(q.Get("from"), 10, 64)
	to, err2 := strconv.ParseInt(q.Get("to"), 10, 64)
	if err1 != nil || err2 != nil || from < 0 || to <= from {
		writeErr(w, http.StatusBadRequest, "need integer 0 <= from < to")
		return
	}
	path := filepath.Join(a.cfg.OutputDir, name)
	info, err := audio.ReadWAVInfo(path)
	if err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if info.BitsPerSample != 32 {
		writeErr(w, http.StatusBadRequest, "only 32-bit takes can be rendered")
		return
	}
	if to > info.Frames() {
		writeErr(w, http.StatusBadRequest, "range is past the end of the take")
		return
	}
	if to-from > int64(audio.MaxRenderSeconds*info.SampleRate) {
		writeErr(w, http.StatusBadRequest, audio.ErrRenderTooLong.Error())
		return
	}

	base := audio.ReadMeta(path).Label
	if base == "" {
		base = strings.TrimSuffix(name, ".wav")
	}
	whole := from == 0 && to == info.Frames()
	fname := audio.RenderFilename(base, from, to, info.SampleRate, whole)

	w.Header().Set("Content-Type", "audio/mpeg")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`inline; filename="%s"`, fname))
	w.Header().Set("Cache-Control", "no-store")
	if err := audio.RenderMP3(r.Context(), w, a.cfg.SaveChannels, path, from, to); err != nil {
		// Headers are already out. The client sniffs the body and reports a
		// short or non-MP3 result as a failed render.
		log.Printf("render %s [%d,%d): %v", name, from, to, err)
	}
}
```

- [ ] **Step 4: Run to verify pass**

Run: `go test ./internal/api -run TestRender -v` → PASS ×4.

- [ ] **Step 5: CI gets ffmpeg, docs, commit**

In `.github/workflows/ci.yml`'s "Install build dependencies" step, change the apt line to `sudo apt-get install -y portaudio19-dev ffmpeg` so the integration tests run rather than skip in CI.

`docs/api.md` route table row and section:

```markdown
| `GET /api/render?file=&from=&to=` | An MP3 of a region, streamed from ffmpeg with the cut's fades, for the share sheet |
```

````markdown
## `GET /api/render?file=&from=&to=`

Streams frames `[from, to)` as a 128 kbps MP3 straight from ffmpeg — the
preview encoder pointed at a region, with the same 3ms declick fades a cut
gets and a sample-exact trim. Nothing is written to disk or cached. Capped
at 10 minutes. `Content-Disposition` names the file
`<label or stem> <m.ss>-<m.ss>.mp3`, or `<label or stem>.mp3` for the whole
take, so the share sheet shows a readable title. No `Content-Length`: the
stream's size is unknown until it ends.

| Status | When |
|---|---|
| 400 | Bad `file`, non-integer or inverted frames, past the end, over 10 minutes, or a non-32-bit take |
| 404 | No such take |
````

Update the route-count sentence at the top of `docs/api.md` (thirteen → fourteen) and README's endpoint count.

```bash
gofmt -l . && go vet ./... && go test -race ./internal/...
git add internal/api/api.go internal/api/api_test.go docs/api.md README.md .github/workflows/ci.yml
git commit -m "Stream a region as an MP3 over GET /api/render for sharing"
```

---

### Task 3: Overview strip

**Files:**
- Create: `web/static/lib/wave/overview.js`, `web/static/lib/wave/overview.test.js`

**Interfaces:**
- Consumes: `frameToX` from `geometry.js`; file peaks `{channels, buckets, data}`; the page's `state` (`region`, `flags`, `cursor`) and the main view's `{start, fpp, width}`.
- Produces (pure, exported): `OVERVIEW_MIN_WINDOW_PX = 24`; `windowRect(view, totalFrames, stripWidth)` → `{x, w}` in CSS px, `w >= 24` and `x + w <= stripWidth`; `stripXToFrame(x, totalFrames, stripWidth)`; `dragToStart(x, grabOffset, view, totalFrames, stripWidth)` → new `view.start` in frames, clamped so the viewport stays inside the take.
- Produces (class): `class Overview { constructor({canvas, filePeaks, totalFrames, getState, getView, emit}); draw(); destroy(); }` emitting `panTo {start}`, `centerOn {frame}`, `fitAll {}`. Gestures: pointerdown inside the window starts a drag (grab offset kept); a tap outside → `centerOn`; double-tap anywhere → `fitAll`. `touch-action: none` on the canvas.

- [ ] **Step 1: Failing tests**

```js
// web/static/lib/wave/overview.test.js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { windowRect, stripXToFrame, dragToStart, OVERVIEW_MIN_WINDOW_PX } from './overview.js';

const TOTAL = 48000 * 900; // 15 minutes
const W = 390;

test('window rect maps the viewport onto the strip', () => {
  // viewport = first quarter of the take
  const view = { start: 0, fpp: TOTAL / 4 / W, width: W };
  assert.deepEqual(windowRect(view, TOTAL, W), { x: 0, w: 97.5 });
  const mid = { start: TOTAL / 2, fpp: TOTAL / 4 / W, width: W };
  assert.deepEqual(windowRect(mid, TOTAL, W), { x: 195, w: 97.5 });
});

test('window is never narrower than the minimum and never overflows the strip', () => {
  const tiny = { start: TOTAL - 4800, fpp: 1, width: W }; // 390 frames visible at the very end
  const r = windowRect(tiny, TOTAL, W);
  assert.equal(r.w, OVERVIEW_MIN_WINDOW_PX);
  assert.ok(r.x + r.w <= W);
  assert.ok(r.x >= 0);
});

test('strip x to frame', () => {
  assert.equal(stripXToFrame(0, TOTAL, W), 0);
  assert.equal(stripXToFrame(W, TOTAL, W), TOTAL);
  assert.equal(stripXToFrame(195, TOTAL, W), TOTAL / 2);
});

test('dragging the window by its grab offset pans and clamps', () => {
  const view = { start: 0, fpp: TOTAL / 4 / W, width: W }; // window x=0..97.5
  // grabbed 10px into the window, pointer now at x=110 -> window x = 100
  assert.equal(dragToStart(110, 10, view, TOTAL, W), Math.round((100 / W) * TOTAL));
  // dragged past the right edge: clamp so the viewport ends at the take's end
  assert.equal(dragToStart(1000, 10, view, TOTAL, W), TOTAL - view.width * view.fpp);
  // dragged past the left edge
  assert.equal(dragToStart(-50, 10, view, TOTAL, W), 0);
});
```

- [ ] **Step 2: Run** `node --test 'web/static/lib/wave/*.test.js'` → overview tests fail to import.

- [ ] **Step 3: Write `overview.js`**

```js
// web/static/lib/wave/overview.js
// The whole-take strip above the main waveform. It exists so the main
// waveform's one-finger drag can select a region: navigation lives here.
// The pure functions are what the tests cover; the class is the canvas
// and pointer plumbing around them.
export const OVERVIEW_MIN_WINDOW_PX = 24;
const TAP_MOVE = 6;
const TAP_MS = 300;

export function windowRect(view, totalFrames, stripWidth) {
  const span = view.width * view.fpp;
  let w = Math.max(OVERVIEW_MIN_WINDOW_PX, (span / totalFrames) * stripWidth);
  w = Math.min(w, stripWidth);
  let x = (view.start / totalFrames) * stripWidth;
  x = Math.max(0, Math.min(stripWidth - w, x));
  return { x, w };
}

export function stripXToFrame(x, totalFrames, stripWidth) {
  return Math.round(Math.max(0, Math.min(stripWidth, x)) / stripWidth * totalFrames);
}

export function dragToStart(x, grabOffset, view, totalFrames, stripWidth) {
  const span = view.width * view.fpp;
  const maxStart = Math.max(0, totalFrames - span);
  const start = ((x - grabOffset) / stripWidth) * totalFrames;
  return Math.round(Math.max(0, Math.min(maxStart, start)));
}

export class Overview {
  constructor({ canvas, filePeaks, totalFrames, getState, getView, emit }) {
    this.canvas = canvas;
    this.ctx = canvas.getContext('2d');
    this.peaks = filePeaks;
    this.total = totalFrames;
    this.getState = getState;
    this.getView = getView;
    this.emit = emit;
    this.raf = 0;
    this.gesture = null;
    this.lastTap = 0;
    this.ac = new AbortController();
    canvas.style.touchAction = 'none';
    const s = this.ac.signal;
    canvas.addEventListener('pointerdown', (e) => this.down(e), { signal: s });
    canvas.addEventListener('pointermove', (e) => this.move(e), { signal: s });
    canvas.addEventListener('pointerup', (e) => this.up(e), { signal: s });
    canvas.addEventListener('pointercancel', (e) => this.up(e), { signal: s });
    this.ro = new ResizeObserver(() => this.resize());
    this.ro.observe(canvas);
    this.resize();
  }

  destroy() { this.ac.abort(); this.ro.disconnect(); cancelAnimationFrame(this.raf); this.destroyed = true; }

  resize() {
    const r = this.canvas.getBoundingClientRect();
    const dpr = window.devicePixelRatio || 1;
    this.dpr = dpr; this.cssW = r.width; this.cssH = r.height;
    this.canvas.width = Math.round(r.width * dpr);
    this.canvas.height = Math.round(r.height * dpr);
    this.draw();
  }

  draw() {
    if (this.destroyed || this.raf) return;
    this.raf = requestAnimationFrame(() => { this.raf = 0; this.paint(); });
  }

  paint() {
    const { ctx, dpr, cssW: W, cssH: H } = this;
    if (!W) return;
    const st = this.getState();
    const css = getComputedStyle(this.canvas);
    const col = (n, fb) => css.getPropertyValue(n).trim() || fb;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.fillStyle = col('--panel-2', '#1a2437');
    ctx.fillRect(0, 0, W, H);

    // Whole-take waveform, both channels folded into one lane.
    const pd = this.peaks;
    ctx.fillStyle = col('--ink-faint', '#5d6b85');
    const mid = H / 2;
    for (let x = 0; x < W; x++) {
      const b0 = Math.floor((x / W) * pd.buckets), b1 = Math.max(b0, Math.floor(((x + 1) / W) * pd.buckets) - 1);
      let mn = Infinity, mx = -Infinity;
      for (let c = 0; c < pd.channels; c++) for (let b = b0; b <= b1; b++) {
        mn = Math.min(mn, pd.data[c][b * 2]); mx = Math.max(mx, pd.data[c][b * 2 + 1]);
      }
      if (!(mx >= mn)) continue;
      ctx.fillRect(x, mid - mx * mid * 0.9, 1, Math.max(1, (mx - mn) * mid * 0.9));
    }

    // Region band and flags
    if (st.region) {
      ctx.fillStyle = 'rgba(52,211,153,0.25)';
      const x0 = (st.region.start / this.total) * W, x1 = (st.region.end / this.total) * W;
      ctx.fillRect(x0, 0, Math.max(1, x1 - x0), H);
    }
    ctx.fillStyle = col('--flag', '#ffb020');
    for (const f of st.flags || []) ctx.fillRect(Math.round((f.frame / this.total) * W), 0, 1, H);

    // Cursor
    ctx.fillStyle = col('--ink', '#eef2f8');
    ctx.fillRect(Math.round((st.cursor / this.total) * W), 0, 1, H);

    // Viewport window
    const { x, w } = windowRect(this.getView(), this.total, W);
    ctx.fillStyle = 'rgba(238,242,248,0.12)';
    ctx.fillRect(x, 0, w, H);
    ctx.strokeStyle = 'rgba(238,242,248,0.6)';
    ctx.lineWidth = 1;
    ctx.strokeRect(x + 0.5, 0.5, w - 1, H - 1);
  }

  pt(e) { const r = this.canvas.getBoundingClientRect(); return { x: e.clientX - r.left, y: e.clientY - r.top }; }

  down(e) {
    this.canvas.setPointerCapture(e.pointerId);
    const p = this.pt(e);
    const { x, w } = windowRect(this.getView(), this.total, this.cssW);
    const inside = p.x >= x && p.x <= x + w;
    this.gesture = { x0: p.x, t0: performance.now(), moved: false, inside, grabOffset: p.x - x };
  }

  move(e) {
    const g = this.gesture;
    if (!g) return;
    const p = this.pt(e);
    if (Math.abs(p.x - g.x0) > TAP_MOVE) g.moved = true;
    if (g.inside && g.moved) {
      this.emit('panTo', { start: dragToStart(p.x, g.grabOffset, this.getView(), this.total, this.cssW) });
    }
  }

  up(e) {
    const g = this.gesture;
    this.gesture = null;
    if (!g) return;
    const p = this.pt(e);
    const isTap = !g.moved && performance.now() - g.t0 < TAP_MS;
    if (!isTap) return;
    const now = performance.now();
    if (now - this.lastTap < TAP_MS) { this.lastTap = 0; this.emit('fitAll', {}); return; }
    this.lastTap = now;
    if (!g.inside) this.emit('centerOn', { frame: stripXToFrame(p.x, this.total, this.cssW) });
  }
}
```

Check the tests by hand: quarter view → `w = 0.25*390 = 97.5`, `x = 0`; mid → `x = 195`. Tiny view at the end: `w = max(24, 390/43.2M*390) = 24`, `x = min(366, ...)` ✓. `dragToStart(110, 10)`: `(100/390)*TOTAL` ✓; far right: `maxStart = TOTAL - span` ✓.

- [ ] **Step 4: Run** `node --test 'web/static/lib/wave/*.test.js'` → all pass (13 + 4 new). `node --check web/static/lib/wave/overview.js`.

- [ ] **Step 5: Commit**

```bash
git add web/static/lib/wave/overview.js web/static/lib/wave/overview.test.js
git commit -m "Overview strip with a draggable viewport window"
```

---

### Task 4: Drag-select in `view.js`; wheel zooms

**Files:**
- Modify: `web/static/lib/wave/view.js` (`down()` default branch ~line 233; `move()` `pan` case ~260; `up()` `pan` case ~309; `wheel()` ~324)

**Interfaces:**
- Consumes: `clampRegion`, `xToFrame`, `frameToX`; `this.minLen`.
- Produces: gesture kind `select` replaces `pan`. While dragging on empty waveform, emits `regionChange {region, final:false}` with `region = {start: min(anchor, cur), end: max(anchor, cur)}` clamped; on release with `moved`, emits `final:true` **only if** `end - start >= minLen`; otherwise emits nothing (and if the drag had emitted provisional regions, emits `regionChange {region: null, final: true}` so the page discards the sliver). A tap on empty waveform still seeks; double-tap still adds a flag. `wheel`: plain wheel zooms about the pointer by `exp(deltaY * 0.01)`; `shiftKey` or a dominant `deltaX` pans. `pinch` unchanged. Two-finger drag is the existing pinch path (moving midpoint pans).

- [ ] **Step 1: Change `down()`**

Replace `default: this.gesture = { ...base, kind: 'pan' };` with:

```js
      // A one-finger drag on bare waveform selects. Panning lives on the
      // overview strip and in the two-finger gesture, so the one gesture a
      // thumb reaches for on the couch makes the thing you want to share.
      default: this.gesture = { ...base, kind: 'select', anchor: xToFrame(p.x, this.view), emitted: false };
```

- [ ] **Step 2: Change `move()`**

Replace the `case 'pan':` block with:

```js
      case 'select': {
        if (!g.moved) break;
        const cur = xToFrame(p.x, this.view);
        const r = { start: Math.min(g.anchor, cur), end: Math.max(g.anchor, cur) };
        g.emitted = true;
        this.emit('regionChange', { region: clampRegion(r, this.total, Math.max(1, Math.min(this.minLen, r.end - r.start))), final: false });
        this.draw(); break;
      }
```

(The provisional clamp allows a region shorter than `minLen` while the finger is still moving, so the band tracks the finger from the first pixel; the minimum is enforced on release.)

- [ ] **Step 3: Change `up()`**

Replace the `case 'pan':` block with:

```js
      case 'select':
        if (g.moved) {
          const cur = xToFrame(p.x, this.view);
          const r = { start: Math.min(g.anchor, cur), end: Math.max(g.anchor, cur) };
          if (r.end - r.start >= this.minLen) this.emit('regionChange', { region: clampRegion(r, this.total, this.minLen), final: true });
          else if (g.emitted) this.emit('regionChange', { region: null, final: true }); // a sliver: discard, do not create
        } else if (isTap) {
          const now = performance.now();
          if (this.lastTap && now - this.lastTap.t < TAP_MS && Math.hypot(p.x - this.lastTap.x, p.y - this.lastTap.y) < DOUBLE_TAP_MOVE) {
            this.lastTap = null;
            this.emit('addFlag', { frame: xToFrame(p.x, this.view) });
          } else {
            this.lastTap = { t: now, x: p.x, y: p.y };
            this.emit('seek', { frame: xToFrame(p.x, this.view) });
          }
        }
        break;
```

Note: on the page side (Task 5), a `regionChange` with `region: null, final: true` must restore whatever region existed before the drag began — the page snapshots `state.region` on the first `final:false` of a select and restores it on a null final. Simplest: the view carries `g.prev = st.region ? {...st.region} : null` in `down()` and emits `{ region: g.prev, final: true }` instead of null. Do that: in `down()`'s default branch add `prev: st.region ? { ...st.region } : null`, and in `up()` emit `{ region: g.prev, final: true }` for the sliver case.

- [ ] **Step 4: Change `wheel()`**

```js
  wheel(e) {
    e.preventDefault();
    const p = this.pt(e);
    // Plain wheel zooms about the pointer: on a timeline that is the thing a
    // mouse user reaches for first, and the overview strip covers panning.
    // Shift, or a trackpad's horizontal axis, pans.
    const horizontal = Math.abs(e.deltaX) > Math.abs(e.deltaY);
    if (e.shiftKey || horizontal) this.panTo(this.view.start + (horizontal ? e.deltaX : e.deltaY) * this.view.fpp);
    else this.zoomTo(this.view.fpp * Math.exp(e.deltaY * 0.01), p.x);
  }
```

- [ ] **Step 5: Verify and commit**

`node --check web/static/lib/wave/view.js`; `node --test 'web/static/lib/wave/*.test.js'` still green (view.js has no unit tests; the page task verifies by hand). Grep that no `'pan'` string remains in `view.js`.

```bash
git add web/static/lib/wave/view.js
git commit -m "One-finger drag on the waveform selects a region; wheel zooms"
```

---

### Task 5: The page — layout, implicit loop, Fine tune

**Files:**
- Modify: `web/static/wave.html`, `web/static/lib/wave/page.js`, `web/static/styles.css` (the `waveform page` block ~line 794), `web/static/sw.js`
- Create: `web/static/lib/wave/share.js` (only `fmtRegionText` in this task; `looksLikeMP3` and `shareOrDownload` come in Task 6 — create the file now with `fmtRegionText` and its test so Task 6 extends it)
- Test: `web/static/lib/wave/share.test.js`

**Interfaces:**
- Consumes: `Overview` (Task 3) and its three events; `view.js` `select` semantics (Task 4); `applyLoop` (existing in page.js); `fmtTime`.
- Produces: `fmtRegionText(region, sampleRate)` → `"0:12.3 – 0:41.8"` (tenths, en dash) or `"whole take"`; the new DOM ids listed below, which Task 6 wires Share into: `#share`, `#region-text`, `#region-clear`, `#fine` (a `<details>`), `#export`, `#region-delete`, `#downbeat-reset`.

- [ ] **Step 1: Failing test for `fmtRegionText`**

```js
// web/static/lib/wave/share.test.js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { fmtRegionText } from './share.js';

test('region text is tenths with an en dash, or whole take', () => {
  assert.equal(fmtRegionText({ start: 48000 * 12.34, end: 48000 * 41.87 }, 48000), '0:12.3 – 0:41.8');
  assert.equal(fmtRegionText({ start: 0, end: 48000 * 75 }, 48000), '0:00.0 – 1:15.0');
  assert.equal(fmtRegionText(null, 48000), 'whole take');
});
```

Run: `node --test 'web/static/lib/wave/*.test.js'` → fails to import `./share.js`.

- [ ] **Step 2: `share.js` (first half)**

```js
// web/static/lib/wave/share.js
// What the action row says about the region, and (Task 6) how the region
// leaves the phone.
function tenths(frame, sr) {
  const t = Math.floor((frame / sr) * 10) / 10;
  const m = Math.floor(t / 60);
  const s = (t - m * 60).toFixed(1).padStart(4, '0');
  return `${m}:${s}`;
}

export function fmtRegionText(region, sampleRate) {
  if (!region) return 'whole take';
  return `${tenths(region.start, sampleRate)} – ${tenths(region.end, sampleRate)}`;
}
```

Check: 12.34s → floor(123.4)/10 = 12.3 → `0:12.3`; 41.87 → 41.8 ✓; 75s → `1:15.0` ✓.

Run the test → PASS.

- [ ] **Step 3: `wave.html`**

Replace the `<main>` and keep the head, top bar, flag sheet, toasts, and script tag:

```html
<main class="wave-main">
  <div id="wave-error" class="wave-error" hidden></div>
  <canvas id="overview-canvas" class="overview-canvas" aria-label="Whole take"></canvas>
  <canvas id="wave-canvas" class="wave-canvas" aria-label="Waveform"></canvas>

  <div class="wave-row action">
    <button id="play" class="icon-btn" type="button">Play</button>
    <span class="region-text">
      <span id="region-text" class="mono">whole take</span>
      <button id="region-clear" class="region-x" type="button" aria-label="Clear region" hidden>×</button>
    </span>
    <button id="share" class="icon-btn primary" type="button">Share</button>
  </div>

  <details id="fine" class="fine">
    <summary>Fine tune</summary>
    <div class="fine-grid">
      <span class="fine-label">Start</span>
      <span class="edge">
        <button id="start-dec" class="nudge" type="button" aria-label="Region start earlier">−</button>
        <button id="start-inc" class="nudge" type="button" aria-label="Region start later">+</button>
      </span>
      <span class="fine-label">End</span>
      <span class="edge">
        <button id="end-dec" class="nudge" type="button" aria-label="Region end earlier">−</button>
        <button id="end-inc" class="nudge" type="button" aria-label="Region end later">+</button>
      </span>
      <span class="fine-label">Position</span>
      <span class="readout"><span id="pos-bar" class="mono"></span> <span id="pos-time" class="mono"></span></span>
      <span class="fine-label">Downbeat</span>
      <span class="fine-hint">drag the marker on the waveform · <button id="downbeat-reset" class="linkish" type="button">reset</button></span>
      <span class="fine-label"></span>
      <span class="edge">
        <button id="export" class="icon-btn" type="button" disabled>Export as take</button>
        <button id="region-delete" class="icon-btn danger" type="button" disabled>Delete region</button>
      </span>
    </div>
  </details>
</main>
```

The `#loop`, `#region`, `#zoom-*`, `#region-start`, `#region-end` elements are gone.

- [ ] **Step 4: `styles.css`**

Replace the waveform-page block's canvas/row rules with:

```css
.overview-canvas {
  width: 100%; height: 36px; display: block;
  border-radius: 8px; background: var(--panel-2);
  touch-action: none;
}
.wave-canvas {
  width: 100%; height: 36vh; min-height: 160px;
  border-radius: var(--radius); background: var(--panel);
  touch-action: none; display: block;
}
.wave-row.action { display: flex; align-items: center; gap: 8px; flex-wrap: nowrap; }
.wave-row.action .region-text {
  flex: 1; min-width: 0;
  display: inline-flex; align-items: center; justify-content: center; gap: 6px;
  font-size: 14px; color: var(--ink-dim);
}
.region-text .mono { font-variant-numeric: tabular-nums; white-space: nowrap; }
.region-x {
  appearance: none; border: 1px solid var(--line); border-radius: 50%;
  width: 28px; height: 28px; background: var(--panel); color: var(--danger);
  font-size: 16px; line-height: 1; cursor: pointer;
}
.fine { border: 1px solid var(--line); border-radius: var(--radius); padding: 6px 10px; }
.fine > summary { cursor: pointer; color: var(--ink-dim); font-size: 13px; padding: 6px 0; list-style: none; }
.fine > summary::before { content: '▸ '; }
.fine[open] > summary::before { content: '▾ '; }
.fine-grid { display: grid; grid-template-columns: 78px 1fr; gap: 8px 10px; align-items: center; padding: 6px 0 4px; }
.fine-label { color: var(--ink-dim); font-size: 13px; }
.fine-hint { color: var(--ink-faint); font-size: 12px; }
.linkish { appearance: none; background: none; border: none; color: var(--accent); font: inherit; cursor: pointer; padding: 0; }
@media (min-width: 900px) {
  .wave-canvas { height: 55vh; }
}
```

Keep `.wave-row`, `.edge`, `.mono`, `.nudge`, `#flag-sheet` rules as they are; delete the old `.wave-row .readout { margin-left: auto }` rule (the readout now lives in the grid) and the zoom row rules if any.

- [ ] **Step 5: `page.js`**

Edits, in order (the file is otherwise unchanged):

1. Imports: add `import { Overview } from './overview.js';` and `import { fmtRegionText } from './share.js';`.
2. After `const view = new WaveView(...)`, add:

```js
  const overview = new Overview({
    canvas: $('overview-canvas'), filePeaks, totalFrames: total,
    getState: () => state, getView: () => view.view,
    emit: (ev, p) => {
      if (ev === 'panTo') view.panTo(p.start);
      else if (ev === 'centerOn') view.centerOn(p.frame);
      else if (ev === 'fitAll') view.fitAll();
    },
  });
```

and make `viewChange` redraw it: `case 'viewChange': overview.draw(); break;`. Also call `overview.draw()` wherever `view.draw()` follows a state change in `emit` (`seek`, `addFlag`, `regionChange`) and in `onTick`; simplest is a helper `function redraw() { view.draw(); overview.draw(); }` used in those places.

3. Implicit loop. Delete `syncLoopButton`, `loopOn`, and the `$('loop')` listener. Keep `applyLoop` but drop its `syncLoopButton()` call. In `emit`'s `regionChange`:

```js
      case 'regionChange':
        state.region = p.region; updateActionRow(); redraw();
        if (p.final) { saveRegion(); applyLoop(state.region); }
        break;
```

`clearRegion()` becomes:

```js
  function clearRegion() {
    state.region = null;
    updateActionRow();
    saveRegion();
    applyLoop(null);
    redraw();
  }
```

On load, after `view.fitAll()`, if `state.region` exists call `applyLoop(state.region)` so a reloaded region comes back looping. The Play handler is unchanged (it plays whatever the clock is set to).

4. Replace `updateRegionRow` with:

```js
  function updateActionRow() {
    const r = state.region;
    $('region-text').textContent = fmtRegionText(r, sr);
    $('region-clear').hidden = !r;
    $('export').disabled = !r;
    $('region-delete').disabled = !r;
    for (const id of ['start-dec', 'start-inc', 'end-dec', 'end-inc']) $(id).disabled = !r;
  }
```

and rename every call. Wire `$('region-clear')` and `$('region-delete')` to `clearRegion`. Delete the `$('region')` listener (the Region button is gone).

5. Downbeat reset:

```js
  $('downbeat-reset').addEventListener('click', () => {
    state.grid.downbeat = 0;
    patch({ downbeat_frame: null }).catch((e) => toast(`Could not reset downbeat: ${e.message}`, 'bad'));
    redraw();
  });
```

6. Fine tune memory:

```js
  const fine = $('fine');
  try { fine.open = localStorage.getItem('wave.fine') === '1'; } catch {}
  fine.addEventListener('toggle', () => { try { localStorage.setItem('wave.fine', fine.open ? '1' : '0'); } catch {} });
```

7. Keyboard: remove the `l`/`L` case and the zoom-button cases; replace `+`/`-` with `view.zoomTo(view.view.fpp / 2, view.view.width / 2)` / `* 2`; add `case '0': view.fitAll(); break;`.

8. Delete the three `$('zoom-*')` listeners. The Share button gets its listener in Task 6; for now `$('share')` has none.

9. `pagehide`: add `overview.destroy()`.

- [ ] **Step 6: `sw.js`**

`CACHE = 'hindsight-shell-v4'`; add `'/lib/wave/overview.js'` and `'/lib/wave/share.js'` to `SHELL`.

- [ ] **Step 7: Run it and check by hand**

Start the demo (Global Constraints), trigger a 20s take, open `/wave.html?file=<take>` at 430×900 with touch emulation and at desktop width. Check, and record each in the report:

1. Overview strip shows the whole take with a window; dragging the window pans; tapping outside jumps; double-tap fits.
2. One-finger drag on the waveform draws a region live and, on release, the region text reads `m:ss.s – m:ss.s`, the × appears, and playback loops it (Play → the cursor wraps at the region end). A tap seeks; a double-tap adds a flag.
3. A 3px drag creates nothing and leaves the previous region alone.
4. × clears the region; Play then plays the take from the cursor.
5. Reload with a region: it comes back looping.
6. Fine tune: nudges move the region by a beat (demo has 96 BPM); reset clears the downbeat marker; Export as take still writes a cut; Delete region clears; the disclosure state survives a reload.
7. Desktop: wheel zooms about the pointer, shift-wheel pans, `0` fits, `+`/`-` zoom.
8. No console errors. The takes list's confirm dialog is unaffected.

- [ ] **Step 8: Commit**

```bash
node --test 'web/static/lib/wave/*.test.js' && node --check web/static/lib/wave/page.js
git add web/static/wave.html web/static/lib/wave/page.js web/static/lib/wave/share.js web/static/lib/wave/share.test.js web/static/styles.css web/static/sw.js
git commit -m "Waveform page v2 layout: overview, drag-select, implicit loop, fine tune"
```

---

### Task 6: Share

**Files:**
- Modify: `web/static/lib/wave/share.js`, `web/static/lib/wave/share.test.js`, `web/static/lib/wave/page.js`

**Interfaces:**
- Consumes: `GET /api/render` (Task 2); `#share` (Task 5); `state.region`, `total`, `file`, `toast`.
- Produces: `looksLikeMP3(bytes: Uint8Array) → boolean` (ID3 tag or frame sync, and length > 1024); `canShareFiles() → boolean` (`isSecureContext && navigator.canShare && navigator.canShare({files:[probe]})`); `async shareOrDownload(blob, filename, title) → 'shared' | 'downloaded' | 'cancelled'`.

- [ ] **Step 1: Failing tests**

Append to `share.test.js`:

```js
import { looksLikeMP3 } from './share.js';

test('looksLikeMP3 accepts ID3 and frame-sync starts, rejects short or other bytes', () => {
  const big = (head) => { const b = new Uint8Array(2048); b.set(head); return b; };
  assert.equal(looksLikeMP3(big([0x49, 0x44, 0x33])), true);      // "ID3"
  assert.equal(looksLikeMP3(big([0xff, 0xfb, 0x90])), true);      // MPEG-1 layer III sync
  assert.equal(looksLikeMP3(big([0xff, 0xe0])), true);            // minimal sync
  assert.equal(looksLikeMP3(big([0x52, 0x49, 0x46, 0x46])), false); // "RIFF"
  assert.equal(looksLikeMP3(new Uint8Array([0x49, 0x44, 0x33])), false); // too short
});
```

Run → fails: `looksLikeMP3` not exported.

- [ ] **Step 2: Extend `share.js`**

```js
export function looksLikeMP3(bytes) {
  if (!bytes || bytes.length <= 1024) return false;
  if (bytes[0] === 0x49 && bytes[1] === 0x44 && bytes[2] === 0x33) return true;
  return bytes[0] === 0xff && (bytes[1] & 0xe0) === 0xe0;
}

// The share sheet needs a secure context and a browser that shares files.
// Probed once with a tiny File so the button can say "Download" from the
// start on the plain LAN address instead of surprising the user on tap.
export function canShareFiles() {
  try {
    if (!globalThis.isSecureContext || !navigator.canShare) return false;
    const probe = new File([new Uint8Array(4)], 'probe.mp3', { type: 'audio/mpeg' });
    return navigator.canShare({ files: [probe] });
  } catch { return false; }
}

export async function shareOrDownload(blob, filename, title) {
  const file = new File([blob], filename, { type: 'audio/mpeg' });
  if (canShareFiles()) {
    try {
      await navigator.share({ files: [file], title });
      return 'shared';
    } catch (e) {
      if (e && e.name === 'AbortError') return 'cancelled';
      // Anything else: fall through to a download once.
    }
  }
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url; a.download = filename;
  document.body.appendChild(a); a.click(); a.remove();
  setTimeout(() => URL.revokeObjectURL(url), 10000);
  return 'downloaded';
}
```

Run → PASS.

- [ ] **Step 3: Wire Share in `page.js`**

Add `import { fmtRegionText, looksLikeMP3, canShareFiles, shareOrDownload } from './share.js';` (replacing the earlier import). After the export handler:

```js
  // --- share --------------------------------------------------------------
  const shareBtn = $('share');
  const shareLabel = canShareFiles() ? 'Share' : 'Download';
  shareBtn.textContent = shareLabel;
  shareBtn.addEventListener('click', async () => {
    const from = state.region ? state.region.start : 0;
    const to = state.region ? state.region.end : total;
    shareBtn.disabled = true;
    shareBtn.textContent = 'Rendering…';
    try {
      const res = await fetch(`/api/render?file=${encodeURIComponent(file)}&from=${from}&to=${to}`);
      if (!res.ok) {
        const b = await res.json().catch(() => ({}));
        throw new Error(b.error || `status ${res.status}`);
      }
      const cd = res.headers.get('Content-Disposition') || '';
      const m = /filename="([^"]+)"/.exec(cd);
      const filename = m ? m[1] : `${(take.label || file.replace(/\.wav$/, ''))}.mp3`;
      const blob = await res.blob();
      const head = new Uint8Array(await blob.slice(0, 2048).arrayBuffer());
      if (!looksLikeMP3(head) || blob.size <= 1024) throw new Error('render failed, try again');
      const result = await shareOrDownload(blob, filename, filename.replace(/\.mp3$/, ''));
      if (result === 'downloaded' && shareLabel === 'Share') toast('Shared as a download');
    } catch (e) {
      toast(`${shareLabel} failed: ${e.message}`, 'bad');
    } finally {
      shareBtn.disabled = false;
      shareBtn.textContent = shareLabel;
    }
  });
```

- [ ] **Step 4: Check by hand**

With the demo running: on desktop, Share reads "Download" on `http://localhost` (not secure? `localhost` *is* a secure context, so it may read "Share" and, since desktop Chrome cannot share files, `canShare` returns false → "Download"). Click it: a `<label> 0.xx-0.yy.mp3` downloads and plays. With no region, the name is `<label>.mp3`. Set a region over 10 minutes on a long take (or trust Task 2's test) → the toast shows the server's message. On a phone over `tailscale serve`, Share opens the sheet with the MP3; cancel is silent; sending to Messages delivers a playable file. Record what was checked.

- [ ] **Step 5: Commit**

```bash
node --test 'web/static/lib/wave/*.test.js' && node --check web/static/lib/wave/page.js
git add web/static/lib/wave/share.js web/static/lib/wave/share.test.js web/static/lib/wave/page.js
git commit -m "Share a region as an MP3 through the phone's share sheet"
```

---

### Task 7: Docs

**Files:**
- Modify: `README.md` (the waveform page feature line), `docs/development.md` (manual checklist), `docs/architecture.md` (module map: add `overview.js`, `share.js`, `/api/render`)

- [ ] **Step 1: Edit**

README feature line becomes: "**Waveform page** — open any take to zoom and scrub it, drag out a region, hear it loop, flag moments, share the region as an MP3 from your phone, or export it as a new take with declick fades."

`docs/development.md`: after the JS test command, add a short "Checking the waveform page by hand" list: the overview gestures, drag-select, implicit loop, Fine tune, Share on a phone over `tailscale serve` (Download on plain HTTP), Export as take.

`docs/architecture.md`: add the two modules and the render route where the v1 entries are.

- [ ] **Step 2: Commit**

```bash
git add README.md docs/development.md docs/architecture.md
git commit -m "Document the waveform page's share flow and gestures"
```

---

## Self-review

**Spec coverage.** §1 navigation → Task 3 (overview: drag, tap, double-tap fit), Task 4 (wheel flip; pinch/two-finger unchanged), Task 5 (`0` fits, zoom buttons removed). §2 region/loop → Task 4 (select gesture, min length on release, prev restored on a sliver), Task 5 (implicit `applyLoop`, ×, reload comes back looping). §3 layout → Task 5 (html/css; fixed-width tabular region text; Fine tune with labelled nudges, readout, downbeat reset, Export as take, Delete region; localStorage memory; 36vh/55vh). §4 share → Tasks 1, 2 (endpoint, args, filename, headers, cap, context kill) and 6 (button label by capability, Rendering…, blob sniff, share sheet, download fallback, cancel silent). §5 modules → all tasks; sw.js v4 in Task 5. §6 errors → Task 2 (400/404, log after headers), Task 6 (sniff, toast, cancel, fallback), Task 4 (sliver silently discarded). Testing → Go in Tasks 1–2 (args golden with pan variants and fade-out start, filename, validation table, headers, ffmpeg integration skipped without it, CI installs ffmpeg), JS in Tasks 3, 5, 6 (overview mapping incl. 24px min and clamps, region text, MP3 sniff), manual in Tasks 5–6 and docs in 7. Out of scope untouched.

**Placeholders.** None; every step has its code. The one "reuse existing helper" reference (`write16bitWAV`) names where it lives and what to do if the name differs.

**Type consistency.** `RenderArgs(wavPath, info, saveChannels, from, to)` in Tasks 1–2; `RenderMP3(ctx, w, saveChannels, wavPath, from, to)` in 1–2; `RenderFilename(base, from, to, sampleRate, whole)` in 1–2; `windowRect/stripXToFrame/dragToStart` signatures in Task 3's tests and code; `Overview` emits `panTo/centerOn/fitAll` consumed in Task 5; `fmtRegionText(region, sr)` in 5; `looksLikeMP3/canShareFiles/shareOrDownload` in 6; `select` gesture's `regionChange` payloads consumed by the unchanged `emit` case in Task 5.
