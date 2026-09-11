# Waveform Page Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A dedicated page per take that zooms and scrubs it from server-side range peaks, edits its flags, sets one region, auditions that region sample-exactly, and exports it as a new take with declick fades.

**Architecture:** Three server additions in Go (range params on `/api/peaks`, `POST /api/cut`, `GET /api/slice`) all built on one streaming frame reader over the take's WAV. A separate static page `/wave.html?file=` with five ES modules under `web/static/lib/wave/`: pure geometry, a tile cache, a canvas view with gestures, a playback clock with two engines behind one `position()`, and a page controller. The region persists in the sidecar's existing `trim` field; the downbeat and cut lineage are new optional sidecar fields.

**Tech Stack:** Go 1.x stdlib + gorilla/mux (existing), vanilla ES modules, Canvas 2D, Web Audio, `node --test` (new, Node 20+).

**Spec:** `docs/superpowers/specs/2026-09-10-waveform-page-design.md`

## Global Constraints

- Takes are **32-bit integer PCM**; every reader in this plan refuses other bit depths with an error rather than guessing.
- Fade length is **3ms**: `fadeFrames = sampleRate * 3 / 1000` (144 at 48kHz). Minimum region is `2*fadeFrames + 1` frames.
- Range peaks: `buckets` in `1..4096`; `0 <= from < to <= frames`.
- Slice cap: `to - from <= 60 * sampleRate`. Slice output is **16-bit PCM**.
- Cut naming: `jam_<2006-01-02_150405>.wav`, same as `Saver.Save`.
- New sidecar fields are optional; **`MetaVersion` stays 1**.
- Every new endpoint validates `file=` with the existing `safeTakeName` (`.wav` only, no path separators).
- Any new static file must be added to `SHELL` in `web/static/sw.js` **and** the `CACHE` version bumped (currently `hindsight-shell-v2` on branch `flag-labels`; bump to `v3`).
- Commit messages end with the attribution trailer used in this repo's recent commits.
- Run `gofmt -l .`, `go vet ./...`, `go test -race ./...` before every commit that touches Go.
- Branch: work on `flag-labels` (it already carries flag labels this page needs) or a branch from it. Never build locally expecting a Pi artifact; run the app with `RING_SECONDS=120 CGO_ENABLED=0 PORT=5099 OUTPUT_DIR=<tmp> go run ./cmd/hindsight --demo`.

---

## File Structure

| File | Responsibility |
|---|---|
| `internal/audio/wav.go` (modify) | `WAVInfo` gains `DataOffset`; `ReadWAVInfo` fills it |
| `internal/audio/frames.go` (create) | `ReadFrames`: stream int32 frames of a range in blocks |
| `internal/audio/rangepeaks.go` (create) | `RangePeaks(path, from, to, buckets)` |
| `internal/audio/fade.go` (create) | `FadeFrames`, `applyFades` on a block |
| `internal/audio/cut.go` (create) | `Cut`: write a faded region as a new take with sidecar |
| `internal/audio/slice.go` (create) | `WriteSlice16`: faded 16-bit WAV of a range to a writer |
| `internal/audio/meta.go` (modify) | `Meta.DownbeatFrame`, `Meta.Source`, `Source` type |
| `internal/audio/save.go` (modify) | export `FreeGB(dir)` and `MakePreview(cfg, wavPath, outCh)` for the cut handler |
| `internal/api/api.go` (modify) | range params on `handlePeaks`; `handleCut`; `handleSlice`; `downbeat_frame` on PATCH; routes |
| `web/static/lib/wave/geometry.js` (create) | pure math, no DOM |
| `web/static/lib/wave/geometry.test.js` (create) | `node --test` |
| `web/static/lib/wave/tiles.js` (create) | tile cache + fetch + fallback |
| `web/static/lib/wave/view.js` (create) | canvas drawing + gestures |
| `web/static/lib/wave/clock.js` (create) | playback engines behind one clock |
| `web/static/lib/wave/page.js` (create) | controller |
| `web/static/wave.html` (create) | the page |
| `web/static/styles.css` (modify) | page styles |
| `web/static/sw.js` (modify) | SHELL + cache bump |
| `web/static/lib/takes.js` (modify) | "Open" link per row |
| `.github/workflows/ci.yml` (modify) | run `node --test` |
| `docs/api.md` (modify) | three endpoints, two sidecar fields |

---

### Task 1: `WAVInfo.DataOffset` and a streaming frame reader

**Files:**
- Modify: `internal/audio/wav.go` (`WAVInfo` struct ~line 226, `ReadWAVInfo` `case "data"` ~line 297)
- Create: `internal/audio/frames.go`
- Test: `internal/audio/frames_test.go`

**Interfaces:**
- Produces: `WAVInfo.DataOffset int64` (byte offset of the first sample), `WAVInfo.Frames() int64`
- Produces: `func ReadFrames(path string, from, to int64, blockFrames int, fn func(block []int32, firstFrame int64) error) (WAVInfo, error)` — calls `fn` with interleaved int32 samples for consecutive sub-ranges of `[from, to)`, each block at most `blockFrames` frames; returns the header info. Errors: `ErrBitDepth` for anything but 32-bit, `ErrRange` for a bad window.

- [ ] **Step 1: Write the failing tests**

```go
// internal/audio/frames_test.go
package audio

import (
	"errors"
	"path/filepath"
	"testing"
)

// rampWAV writes a 2-channel take where left = frame index, right = -frame
// index, so any block's contents identify its position.
func rampWAV(t *testing.T, frames int) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "jam_ramp.wav")
	data := make([]int32, frames*2)
	for i := 0; i < frames; i++ {
		data[i*2] = int32(i)
		data[i*2+1] = -int32(i)
	}
	if _, err := WriteWAV(p, data, 2, []int{0, 1}, 48000); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReadWAVInfoReportsDataOffsetAndFrames(t *testing.T) {
	info, err := ReadWAVInfo(rampWAV(t, 100))
	if err != nil {
		t.Fatal(err)
	}
	if info.DataOffset != 44 {
		t.Errorf("DataOffset = %d, want 44 for a canonical header", info.DataOffset)
	}
	if info.Frames() != 100 {
		t.Errorf("Frames() = %d, want 100", info.Frames())
	}
}

func TestReadFramesStreamsExactlyTheWindowInOrder(t *testing.T) {
	p := rampWAV(t, 1000)
	var got []int32
	var firsts []int64
	info, err := ReadFrames(p, 250, 610, 128, func(b []int32, first int64) error {
		got = append(got, b...)
		firsts = append(firsts, first)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if info.Channels != 2 {
		t.Fatalf("channels = %d", info.Channels)
	}
	if len(got) != 360*2 {
		t.Fatalf("got %d samples, want %d", len(got), 360*2)
	}
	for i := 0; i < 360; i++ {
		if got[i*2] != int32(250+i) || got[i*2+1] != -int32(250+i) {
			t.Fatalf("frame %d = (%d,%d), want (%d,%d)", i, got[i*2], got[i*2+1], 250+i, -(250 + i))
		}
	}
	// 360 frames in 128-frame blocks: 250, 378, 506.
	if len(firsts) != 3 || firsts[0] != 250 || firsts[1] != 378 || firsts[2] != 506 {
		t.Errorf("block starts = %v", firsts)
	}
}

func TestReadFramesRejectsABadWindow(t *testing.T) {
	p := rampWAV(t, 100)
	for _, w := range [][2]int64{{-1, 10}, {10, 10}, {20, 10}, {0, 101}} {
		_, err := ReadFrames(p, w[0], w[1], 64, func([]int32, int64) error { return nil })
		if !errors.Is(err, ErrRange) {
			t.Errorf("window %v: err = %v, want ErrRange", w, err)
		}
	}
}

func TestReadFramesStopsOnCallbackError(t *testing.T) {
	p := rampWAV(t, 1000)
	boom := errors.New("boom")
	calls := 0
	_, err := ReadFrames(p, 0, 1000, 100, func([]int32, int64) error { calls++; return boom })
	if !errors.Is(err, boom) || calls != 1 {
		t.Errorf("err = %v, calls = %d; want boom after one call", err, calls)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/audio -run 'TestReadWAVInfoReportsDataOffset|TestReadFrames' -v`
Expected: compile failure — `info.DataOffset`, `info.Frames`, `ReadFrames`, `ErrRange` undefined.

- [ ] **Step 3: Add `DataOffset` and `Frames()` to `WAVInfo`**

In `internal/audio/wav.go`, change the struct and the `data` case:

```go
// WAVInfo is the subset of a WAV header needed to describe a take.
type WAVInfo struct {
	Channels      int
	SampleRate    int
	BitsPerSample int
	DataBytes     int64
	// DataOffset is the byte offset of the first sample, i.e. just past the
	// data chunk header. 44 for every take this app writes.
	DataOffset int64
}

// Frames reports the number of sample frames in the data chunk.
func (w WAVInfo) Frames() int64 {
	bpf := int64(w.Channels * w.BitsPerSample / 8)
	if bpf <= 0 {
		return 0
	}
	return w.DataBytes / bpf
}
```

and in `ReadWAVInfo`:

```go
		case "data":
			info.DataBytes = size
			if !sawFmt {
				return info, fmt.Errorf("data chunk before fmt chunk")
			}
			off, err := f.Seek(0, io.SeekCurrent)
			if err != nil {
				return info, err
			}
			info.DataOffset = off
			return info, nil
```

- [ ] **Step 4: Create `frames.go`**

```go
// internal/audio/frames.go
package audio

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
)

var (
	// ErrRange is a frame window outside the file or inverted.
	ErrRange = errors.New("frame range out of bounds")
	// ErrBitDepth is a WAV this app did not write; every take is 32-bit.
	ErrBitDepth = errors.New("only 32-bit PCM takes are supported")
)

// ReadFrames streams the interleaved int32 samples of frames [from, to) to
// fn in blocks of at most blockFrames frames, in order. It reads only those
// bytes -- never the whole file -- so a 15-minute take costs the same memory
// as a 1-second one. The block passed to fn is reused between calls; copy it
// if it must outlive the call.
func ReadFrames(path string, from, to int64, blockFrames int, fn func(block []int32, firstFrame int64) error) (WAVInfo, error) {
	info, err := ReadWAVInfo(path)
	if err != nil {
		return info, err
	}
	if info.BitsPerSample != 32 {
		return info, ErrBitDepth
	}
	if from < 0 || to <= from || to > info.Frames() {
		return info, fmt.Errorf("%w: [%d, %d) of %d frames", ErrRange, from, to, info.Frames())
	}
	if blockFrames <= 0 {
		blockFrames = 1 << 14
	}

	f, err := os.Open(path)
	if err != nil {
		return info, err
	}
	defer f.Close()

	ch := int64(info.Channels)
	if _, err := f.Seek(info.DataOffset+from*ch*4, io.SeekStart); err != nil {
		return info, err
	}
	r := bufio.NewReaderSize(f, 1<<18)
	le := binary.LittleEndian

	block := make([]int32, blockFrames*int(ch))
	raw := make([]byte, len(block)*4)
	for pos := from; pos < to; {
		n := int64(blockFrames)
		if to-pos < n {
			n = to - pos
		}
		nb := int(n * ch * 4)
		if _, err := io.ReadFull(r, raw[:nb]); err != nil {
			return info, fmt.Errorf("reading frames at %d: %w", pos, err)
		}
		for i := 0; i < int(n*ch); i++ {
			block[i] = int32(le.Uint32(raw[i*4:]))
		}
		if err := fn(block[:n*ch], pos); err != nil {
			return info, err
		}
		pos += n
	}
	return info, nil
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/audio -run 'TestReadWAVInfoReportsDataOffset|TestReadFrames' -v`
Expected: all four PASS.

- [ ] **Step 6: Run the whole package and commit**

Run: `gofmt -l . && go vet ./... && go test -race ./internal/audio`
Expected: no output from gofmt, tests PASS.

```bash
git add internal/audio/wav.go internal/audio/frames.go internal/audio/frames_test.go
git commit -m "Stream a frame range out of a take without reading the file"
```

---

### Task 2: Range peaks in the audio package

**Files:**
- Create: `internal/audio/rangepeaks.go`
- Test: `internal/audio/rangepeaks_test.go`

**Interfaces:**
- Consumes: `ReadFrames` from Task 1; `PeakData`, `peakAccumulator` from `wav.go`.
- Produces: `func RangePeaks(path string, from, to int64, buckets int) (*PeakData, error)`; `PeakData` gains `From int64 \`json:"from"\`` (zero for the whole-file peaks written at save, harmless for existing files). `const MaxRangeBuckets = 4096`.

- [ ] **Step 1: Write the failing tests**

```go
// internal/audio/rangepeaks_test.go
package audio

import (
	"errors"
	"math"
	"path/filepath"
	"testing"
)

// noiseWAV writes a take with a deterministic pseudo-random signal so peaks
// are non-trivial.
func noiseWAV(t *testing.T, frames int) (string, *PeakData) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "jam_noise.wav")
	data := make([]int32, frames*2)
	x := uint32(12345)
	for i := range data {
		x = x*1664525 + 1013904223
		data[i] = int32(x)
	}
	pd, err := WriteWAV(p, data, 2, []int{0, 1}, 48000)
	if err != nil {
		t.Fatal(err)
	}
	return p, pd
}

func TestRangePeaksOverTheWholeFileMatchesTheSavedPeaks(t *testing.T) {
	p, saved := noiseWAV(t, 1024*7) // 7 frames per bucket, exact
	got, err := RangePeaks(p, 0, 1024*7, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if got.Buckets != saved.Buckets || got.From != 0 || got.Channels != 2 {
		t.Fatalf("shape = %d buckets from %d, want %d from 0", got.Buckets, got.From, saved.Buckets)
	}
	for c := range saved.Data {
		for i := range saved.Data[c] {
			if got.Data[c][i] != saved.Data[c][i] {
				t.Fatalf("ch %d [%d] = %v, want %v", c, i, got.Data[c][i], saved.Data[c][i])
			}
		}
	}
}

func TestRangePeaksOfASubRangeEqualsPeaksOfThatRangeAlone(t *testing.T) {
	p, _ := noiseWAV(t, 5000)
	got, err := RangePeaks(p, 1000, 1640, 64) // 10 frames per bucket
	if err != nil {
		t.Fatal(err)
	}
	if got.From != 1000 || got.Buckets != 64 || math.Abs(got.Duration-640.0/48000) > 1e-9 {
		t.Fatalf("from=%d buckets=%d dur=%v", got.From, got.Buckets, got.Duration)
	}
	// Recompute by hand from the samples.
	var want [2][]float32
	_, err = ReadFrames(p, 1000, 1640, 640, func(b []int32, _ int64) error {
		for c := 0; c < 2; c++ {
			for k := 0; k < 64; k++ {
				mn, mx := float32(math.Inf(1)), float32(math.Inf(-1))
				for j := 0; j < 10; j++ {
					v := float32(b[(k*10+j)*2+c]) / float32(math.MaxInt32)
					if v < mn {
						mn = v
					}
					if v > mx {
						mx = v
					}
				}
				want[c] = append(want[c], mn, mx)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for c := 0; c < 2; c++ {
		for i := range want[c] {
			if got.Data[c][i] != want[c][i] {
				t.Fatalf("ch %d [%d] = %v, want %v", c, i, got.Data[c][i], want[c][i])
			}
		}
	}
}

func TestRangePeaksLastBucketAbsorbsTheRemainder(t *testing.T) {
	p, _ := noiseWAV(t, 1000)
	got, err := RangePeaks(p, 0, 1000, 3) // 333 per bucket, one left over
	if err != nil {
		t.Fatal(err)
	}
	if got.Buckets != 3 {
		t.Errorf("buckets = %d, want exactly 3", got.Buckets)
	}
}

func TestRangePeaksValidates(t *testing.T) {
	p, _ := noiseWAV(t, 100)
	if _, err := RangePeaks(p, 0, 100, 0); err == nil {
		t.Error("buckets=0 accepted")
	}
	if _, err := RangePeaks(p, 0, 100, MaxRangeBuckets+1); err == nil {
		t.Error("buckets over the cap accepted")
	}
	if _, err := RangePeaks(p, 50, 200, 4); !errors.Is(err, ErrRange) {
		t.Errorf("out of range: %v", err)
	}
	// More buckets than frames is fine: buckets past the audio are empty.
	if _, err := RangePeaks(p, 0, 10, 20); err != nil {
		t.Errorf("more buckets than frames: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/audio -run TestRangePeaks -v`
Expected: compile failure, `RangePeaks`, `MaxRangeBuckets`, `got.From` undefined.

- [ ] **Step 3: Add `From` to `PeakData` and write `rangepeaks.go`**

In `wav.go`, add to `PeakData` after `Buckets`:

```go
	// From is the first frame the buckets describe. Zero for the whole-take
	// file; set by RangePeaks.
	From int64 `json:"from"`
```

Create:

```go
// internal/audio/rangepeaks.go
package audio

import (
	"fmt"
	"math"
)

// MaxRangeBuckets caps a range-peaks request. 4096 is two buckets per device
// pixel on the widest phone at 3x, which is more than a waveform can show.
const MaxRangeBuckets = 4096

// RangePeaks computes min/max peaks per channel over frames [from, to) in
// exactly `buckets` buckets, reading only that range of the file. The last
// bucket absorbs any remainder frames so the count is always what was asked
// for. Buckets past the end of a very short range are empty (0,0).
func RangePeaks(path string, from, to int64, buckets int) (*PeakData, error) {
	if buckets < 1 || buckets > MaxRangeBuckets {
		return nil, fmt.Errorf("buckets must be 1..%d", MaxRangeBuckets)
	}
	frames := to - from
	per := frames / int64(buckets)
	if per < 1 {
		per = 1
	}
	var (
		info   WAVInfo
		err    error
		mins   [][]float32
		maxs   [][]float32
		bucket = func(frame int64) int {
			b := int((frame - from) / per)
			if b >= buckets {
				b = buckets - 1
			}
			return b
		}
	)
	info, err = ReadFrames(path, from, to, 1<<14, func(block []int32, first int64) error {
		if mins == nil {
			mins = make([][]float32, info.Channels)
			maxs = make([][]float32, info.Channels)
			for c := range mins {
				mins[c] = make([]float32, buckets)
				maxs[c] = make([]float32, buckets)
				for i := range mins[c] {
					mins[c][i] = float32(math.Inf(1))
					maxs[c][i] = float32(math.Inf(-1))
				}
			}
		}
		ch := info.Channels
		n := len(block) / ch
		for i := 0; i < n; i++ {
			b := bucket(first + int64(i))
			for c := 0; c < ch; c++ {
				v := float32(block[i*ch+c]) / float32(math.MaxInt32)
				if v < mins[c][b] {
					mins[c][b] = v
				}
				if v > maxs[c][b] {
					maxs[c][b] = v
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	data := make([][]float32, info.Channels)
	for c := range data {
		flat := make([]float32, 0, buckets*2)
		for i := 0; i < buckets; i++ {
			mn, mx := mins[c][i], maxs[c][i]
			if math.IsInf(float64(mn), 1) {
				mn, mx = 0, 0
			}
			flat = append(flat, mn, mx)
		}
		data[c] = flat
	}
	return &PeakData{
		Version:    1,
		Channels:   info.Channels,
		SampleRate: info.SampleRate,
		Duration:   float64(frames) / float64(info.SampleRate),
		Buckets:    buckets,
		From:       from,
		Data:       data,
	}, nil
}
```

Note: `ReadFrames` assigns `info` before calling `fn`, because it returns after the loop; but `fn` runs *during* the call. Fix that ordering by reading the header first:

```go
	info, err = ReadWAVInfo(path)
	if err != nil {
		return nil, err
	}
	_, err = ReadFrames(path, from, to, 1<<14, func(block []int32, first int64) error {
```

(Use this form; the `info` inside the closure is then valid. The WhL-file peaks test in Step 1 compares float32 division by `MaxInt32`; confirm `WriteWAV`'s accumulator uses the same divisor — it does: `float32(v) / float32(math.MaxInt32)` in `WriteWAV`. If it differs, match it here so the whole-file test can pass bit for bit.)

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/audio -run TestRangePeaks -v`
Expected: all four PASS. If the whole-file comparison fails on the last bucket only, `WriteWAV`'s accumulator drops remainder frames into an extra bucket; then change the test's frame count so `frames % 1024 == 0` (it already is: 7168) and check `bucketSize` math in `newPeakAccumulator` — with an exact multiple both agree.

- [ ] **Step 5: Commit**

```bash
gofmt -l . && go vet ./... && go test -race ./internal/audio
git add internal/audio/wav.go internal/audio/rangepeaks.go internal/audio/rangepeaks_test.go
git commit -m "Compute peaks over any frame range of a take"
```

---

### Task 3: Range parameters on `GET /api/peaks`

**Files:**
- Modify: `internal/api/api.go` (`handlePeaks` ~line 332)
- Test: `internal/api/api_test.go`
- Modify: `docs/api.md` (the `/api/peaks` section)

**Interfaces:**
- Consumes: `audio.RangePeaks`, `audio.MaxRangeBuckets`, `audio.ErrRange`.
- Produces: `GET /api/peaks?file=&from=&to=&buckets=` returning `PeakData` JSON with `from` set; without the three params, the existing file is served unchanged.

- [ ] **Step 1: Write the failing tests**

Append to `internal/api/api_test.go`:

```go
func TestPeaksRangeComputesOnDemand(t *testing.T) {
	r, dir := newTestAPI(t)
	writeRealTake(t, dir, "jam_r.wav", 4800)

	w := do(t, r, http.MethodGet, "/api/peaks?file=jam_r.wav&from=480&to=960&buckets=8")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	var pd audio.PeakData
	if err := json.Unmarshal(w.Body.Bytes(), &pd); err != nil {
		t.Fatal(err)
	}
	if pd.From != 480 || pd.Buckets != 8 || pd.Channels != 2 || len(pd.Data[0]) != 16 {
		t.Errorf("got from=%d buckets=%d ch=%d len=%d", pd.From, pd.Buckets, pd.Channels, len(pd.Data[0]))
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("Cache-Control = %q, want immutable: a take's samples never change", cc)
	}
}

func TestPeaksRangeRejectsBadWindows(t *testing.T) {
	r, dir := newTestAPI(t)
	writeRealTake(t, dir, "jam_r.wav", 1000)
	for _, q := range []string{
		"from=0&to=1001&buckets=4",  // past the end
		"from=10&to=10&buckets=4",   // empty
		"from=-1&to=10&buckets=4",   // negative
		"from=0&to=10&buckets=0",    // no buckets
		"from=0&to=10&buckets=4097", // over the cap
		"from=0&to=10",              // partial: all three or none
		"from=x&to=10&buckets=4",    // not a number
	} {
		w := do(t, r, http.MethodGet, "/api/peaks?file=jam_r.wav&"+q)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", q, w.Code)
		}
	}
}

func TestPeaksRangeOnAMissingTakeIs404(t *testing.T) {
	r, _ := newTestAPI(t)
	w := do(t, r, http.MethodGet, "/api/peaks?file=jam_nope.wav&from=0&to=10&buckets=4")
	if w.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", w.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/api -run TestPeaksRange -v`
Expected: `TestPeaksRangeComputesOnDemand` FAILS with status 404 ("peaks not generated" — the test take has no peaks file and the params are ignored); the bad-window test fails on the partial and 404 cases.

- [ ] **Step 3: Extend `handlePeaks`**

Replace the body of `handlePeaks` with:

```go
func (a *API) handlePeaks(w http.ResponseWriter, r *http.Request) {
	name, err := a.safeTakeName(r.URL.Query().Get("file"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	q := r.URL.Query()
	hasRange := q.Has("from") || q.Has("to") || q.Has("buckets")
	if hasRange {
		a.handlePeaksRange(w, r, name)
		return
	}
	path := filepath.Join(a.cfg.OutputDir, strings.TrimSuffix(name, ".wav")+".peaks.json")
	f, err := os.Open(path)
	if err != nil {
		writeErr(w, http.StatusNotFound, "peaks not generated")
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	http.ServeContent(w, r, "peaks.json", statModTime(f), f)
}

// handlePeaksRange computes peaks for [from, to) on demand. All three params
// are required together: a partial request is a client bug, not a request
// for the file. The result is immutable for the same reason the file is --
// a take's samples never change after save -- so it is cached the same way.
func (a *API) handlePeaksRange(w http.ResponseWriter, r *http.Request, name string) {
	q := r.URL.Query()
	if !(q.Has("from") && q.Has("to") && q.Has("buckets")) {
		writeErr(w, http.StatusBadRequest, "from, to and buckets are required together")
		return
	}
	from, err1 := strconv.ParseInt(q.Get("from"), 10, 64)
	to, err2 := strconv.ParseInt(q.Get("to"), 10, 64)
	buckets, err3 := strconv.Atoi(q.Get("buckets"))
	if err1 != nil || err2 != nil || err3 != nil {
		writeErr(w, http.StatusBadRequest, "from, to and buckets must be integers")
		return
	}
	if from < 0 || to <= from {
		writeErr(w, http.StatusBadRequest, "need 0 <= from < to")
		return
	}
	if buckets < 1 || buckets > audio.MaxRangeBuckets {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("buckets must be 1..%d", audio.MaxRangeBuckets))
		return
	}
	path := filepath.Join(a.cfg.OutputDir, name)
	if _, err := os.Stat(path); err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	pd, err := audio.RangePeaks(path, from, to, buckets)
	switch {
	case errors.Is(err, audio.ErrRange):
		writeErr(w, http.StatusBadRequest, "range is past the end of the take")
		return
	case err != nil:
		log.Printf("range peaks %s: %v", name, err)
		writeErr(w, http.StatusInternalServerError, "could not compute peaks")
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	writeJSON(w, http.StatusOK, pd)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/api -run TestPeaksRange -v`
Expected: PASS ×3.

- [ ] **Step 5: Document and commit**

In `docs/api.md`, under the `/api/peaks` entry, add:

```markdown
With `from`, `to` (frames, `0 <= from < to <= frames`) and `buckets`
(`1..4096`) given together, the peaks are computed on demand over exactly
that range instead of served from the file. The response has the same shape
plus `"from"`, and `duration` describes the range. All three or none: a
partial set is 400. The waveform page uses this for every zoomed view.
```

```bash
gofmt -l . && go vet ./... && go test -race ./internal/api
git add internal/api/api.go internal/api/api_test.go docs/api.md
git commit -m "Serve peaks for any frame range from GET /api/peaks"
```

---

### Task 4: Fades and the cut writer

**Files:**
- Create: `internal/audio/fade.go`, `internal/audio/cut.go`
- Modify: `internal/audio/meta.go` (Meta struct ~line 65), `internal/audio/save.go` (export helpers)
- Test: `internal/audio/cut_test.go`

**Interfaces:**
- Consumes: `ReadFrames` (Task 1), `writeWAVHeader`, `newPeakAccumulator`, `WritePeaks`, `peaksPath`, `ReadMeta`, `WriteMeta`, `stampFlags`, `NormalizeFlags`.
- Produces:
  - `func FadeFrames(sampleRate int) int64` = `sampleRate * 3 / 1000`.
  - `func applyFades(block []int32, first, total int64, channels int, fade int64)` — in place, linear ramp over `[0, fade)` and `[total-fade, total)` of the region.
  - `type Source struct { Name string; StartFrame, EndFrame int64 }` and `Meta.Source *Source \`json:"source,omitempty"\``, `Meta.DownbeatFrame *int64 \`json:"downbeat_frame,omitempty"\``.
  - `type CutRequest struct { Source string; StartFrame, EndFrame int64; Label string }` (Source is the take filename).
  - `func Cut(dir string, req CutRequest, now time.Time) (name string, err error)` — writes WAV + peaks + sidecar; returns the new filename. Errors: `ErrRange`, `ErrTooShort`.
  - `var ErrTooShort = errors.New("region shorter than two fades")`
  - In `save.go`: `func FreeGB(dir string) (float64, float64)` (package-level; `Saver.FreeGB` calls it) and `func MakePreview(cfg *config.Config, wavPath string, outCh int)` (package-level; `Saver.makePreview` calls it). Check the import: `save.go` already has `cfg := s.cap.cfg` of type `*config.Config`.

- [ ] **Step 1: Write the failing tests**

```go
// internal/audio/cut_test.go
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
	data := make([]int32, 48000*2)
	for i := range data {
		data[i] = 1 << 20
	}
	if _, err := WriteWAV(src, data, 2, []int{0, 1}, 48000); err != nil {
		t.Fatal(err)
	}
	bpm := 96.0
	if err := WriteMeta(src, Meta{Version: MetaVersion, Label: "jam", BPM: &bpm, Starred: true,
		Trim: &Trim{StartFrame: 1, EndFrame: 2},
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
	var first, mid, last int32
	_, err = ReadFrames(out, 0, 10000, 10000, func(b []int32, _ int64) error {
		first, mid, last = b[0], b[5000*2], b[9999*2]
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if first != 0 || mid != 1<<20 || last >= 1<<20 || last == 0 {
		t.Errorf("first=%d mid=%d last=%d: want silence, exact copy, faded tail", first, mid, last)
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

func TestCutRejectsShortAndOutOfRangeRegions(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "jam_src.wav")
	if _, err := WriteWAV(src, make([]int32, 2000*2), 2, []int{0, 1}, 48000); err != nil {
		t.Fatal(err)
	}
	if _, err := Cut(dir, CutRequest{Source: "jam_src.wav", StartFrame: 0, EndFrame: 288}, time.Now()); !errors.Is(err, ErrTooShort) {
		t.Errorf("288 frames (2*144): err = %v, want ErrTooShort", err)
	}
	if _, err := Cut(dir, CutRequest{Source: "jam_src.wav", StartFrame: 0, EndFrame: 289}, time.Now()); err != nil {
		t.Errorf("289 frames: %v, want ok", err)
	}
	if _, err := Cut(dir, CutRequest{Source: "jam_src.wav", StartFrame: 1000, EndFrame: 2001}, time.Now()); !errors.Is(err, ErrRange) {
		t.Errorf("past end: err = %v, want ErrRange", err)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "jam_src.wav" && filepath.Ext(e.Name()) == ".wav" && e.Name() != "" {
			info, _ := ReadWAVInfo(filepath.Join(dir, e.Name()))
			if info.Frames() == 0 {
				t.Errorf("a rejected cut left %s behind", e.Name())
			}
		}
	}
}

func TestMetaRoundTripsDownbeatAndSource(t *testing.T) {
	p := filepath.Join(t.TempDir(), "jam_m.wav")
	db := int64(4800)
	if err := WriteMeta(p, Meta{Version: MetaVersion, DownbeatFrame: &db,
		Source: &Source{Name: "jam_a.wav", StartFrame: 1, EndFrame: 2}}); err != nil {
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
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/audio -run 'TestFade|TestApplyFades|TestCut|TestMetaRoundTripsDownbeat' -v`
Expected: compile failure on `FadeFrames`, `applyFades`, `Cut`, `CutRequest`, `ErrTooShort`, `Source`, `DownbeatFrame`.

- [ ] **Step 3: Add the sidecar fields**

In `internal/audio/meta.go`, inside `Meta` after `Trim`:

```go
	// DownbeatFrame is where bar 1 beat 1 falls, for the waveform page's
	// grid. Optional; absent means "unknown", and the page then starts the
	// grid at frame 0.
	DownbeatFrame *int64 `json:"downbeat_frame,omitempty"`

	// Source records where a cut came from. Nil for a take saved from the ring.
	Source *Source `json:"source,omitempty"`
```

and above `Meta`:

```go
// Source is a cut's lineage: the take it was cut from and the frame range,
// in the source's frames.
type Source struct {
	Name       string `json:"name"`
	StartFrame int64  `json:"start_frame"`
	EndFrame   int64  `json:"end_frame"`
}
```

- [ ] **Step 4: Export the two save helpers**

In `internal/audio/save.go`, replace `Saver.FreeGB` and `Saver.makePreview` with thin wrappers over package functions. Keep the wrapper names so nothing else changes:

```go
// FreeGB reports free space on the output volume.
func (s *Saver) FreeGB() (float64, float64) { return FreeGB(s.cap.cfg.OutputDir) }

// FreeGB reports free gigabytes and used percent for the volume holding dir.
func FreeGB(dir string) (float64, float64) {
	u, err := disk.Usage(dir)
	if err != nil {
		return 0, 0
	}
	return float64(u.Free) / (1024 * 1024 * 1024), u.UsedPercent
}
```

```go
func (s *Saver) makePreview(wavPath string, outCh int) { MakePreview(s.cap.cfg, wavPath, outCh) }

// MakePreview renders the mp3 proxy. The channel mapping is explicit: ...
// (move the existing comment and body here, replacing `cfg := s.cap.cfg`
// with the parameter)
func MakePreview(cfg *config.Config, wavPath string, outCh int) {
	mp3Path := previewPath(wavPath)
	...existing body unchanged...
}
```

Confirm `save.go` imports `github.com/gabeduke/hindsight/internal/config`; it uses `s.cap.cfg` which is `*config.Config`, so the import exists via `capture.go` in the same package — add it to `save.go`'s import block if `go vet` complains.

- [ ] **Step 5: Create `fade.go`**

```go
// internal/audio/fade.go
package audio

// FadeFrames is the declick fade length: 3ms at the given rate. 144 frames
// at 48kHz. Long enough to remove the click a hard cut makes, short enough
// that it is inaudible as a fade.
func FadeFrames(sampleRate int) int64 { return int64(sampleRate) * 3 / 1000 }

// applyFades scales a block of interleaved samples in place with a linear
// fade-in over the region's first `fade` frames and a fade-out over its last
// `fade` frames. `first` is the block's first frame relative to the region
// start, `total` the region length, so blocks can be faded independently as
// they stream past. Frame i is scaled by i/fade on the way in and by
// (total-i)/fade on the way out; frame 0 is silent, the last frame is one
// step above silence. Integer math on int64 keeps it exact.
func applyFades(block []int32, first, total int64, channels int, fade int64) {
	if fade <= 0 {
		return
	}
	n := int64(len(block) / channels)
	for i := int64(0); i < n; i++ {
		pos := first + i
		var num, den int64 = 1, 1
		if pos < fade {
			num, den = pos, fade
		} else if rem := total - pos; rem <= fade {
			num, den = rem, fade
		} else {
			continue
		}
		for c := 0; c < channels; c++ {
			k := int(i)*channels + c
			block[k] = int32(int64(block[k]) * num / den)
		}
	}
}
```

Check against the test: frame `total-1` gives `rem = 1`, scale `1/fade` → 1000/10 = 100. Frame `total-fade` gives `rem = fade`, scale 1 → untouched plateau, which is what the cross-block test asserts at `b[50]`.

- [ ] **Step 6: Create `cut.go`**

```go
// internal/audio/cut.go
package audio

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrTooShort is a region that would be nothing but fades.
var ErrTooShort = errors.New("region shorter than two fades")

// CutRequest names a region of a source take, in the source's frames.
type CutRequest struct {
	Source     string // filename of the source take, e.g. jam_2026-09-10_221441.wav
	StartFrame int64
	EndFrame   int64
	Label      string // optional; defaults to "<source label or stem> cut"
}

// Cut writes frames [StartFrame, EndFrame) of the source as a new take in
// dir with a 3ms declick fade at each edge, plus its peaks file and a
// sidecar that inherits the source's BPM and the flags inside the region,
// rebased. It streams, so memory is one block whatever the region length.
// The source is never modified. The preview mp3 is the caller's job (it
// needs ffmpeg and the capture config); see MakePreview.
//
// Returns the new take's filename. On any failure after the file is created,
// the partial WAV is removed so a half-written take never appears in the list.
func Cut(dir string, req CutRequest, now time.Time) (string, error) {
	srcPath := filepath.Join(dir, filepath.Base(req.Source))
	info, err := ReadWAVInfo(srcPath)
	if err != nil {
		return "", err
	}
	if info.BitsPerSample != 32 {
		return "", ErrBitDepth
	}
	if req.StartFrame < 0 || req.EndFrame <= req.StartFrame || req.EndFrame > info.Frames() {
		return "", fmt.Errorf("%w: [%d, %d) of %d frames", ErrRange, req.StartFrame, req.EndFrame, info.Frames())
	}
	fade := FadeFrames(info.SampleRate)
	total := req.EndFrame - req.StartFrame
	if total < 2*fade+1 {
		return "", ErrTooShort
	}

	name := fmt.Sprintf("jam_%s.wav", now.Format("2006-01-02_150405"))
	outPath := filepath.Join(dir, name)
	if err := writeCutWAV(srcPath, outPath, info, req.StartFrame, req.EndFrame, fade); err != nil {
		os.Remove(outPath)
		os.Remove(peaksPath(outPath))
		return "", err
	}

	srcMeta := ReadMeta(srcPath)
	m := Meta{Version: MetaVersion}
	m.Label = strings.TrimSpace(req.Label)
	if m.Label == "" {
		base := srcMeta.Label
		if base == "" {
			base = strings.TrimSuffix(filepath.Base(req.Source), ".wav")
		}
		m.Label = base + " cut"
	}
	if srcMeta.BPM != nil {
		bpm := *srcMeta.BPM
		m.BPM = &bpm
	}
	m.Source = &Source{Name: filepath.Base(req.Source), StartFrame: req.StartFrame, EndFrame: req.EndFrame}
	var flags []Flag
	for _, f := range srcMeta.Flags {
		if f.Frame >= req.StartFrame && f.Frame < req.EndFrame {
			flags = append(flags, Flag{Frame: f.Frame - req.StartFrame, Label: f.Label})
		}
	}
	if err := WriteMeta(outPath, m); err != nil {
		os.Remove(outPath)
		os.Remove(peaksPath(outPath))
		return "", fmt.Errorf("sidecar: %w", err)
	}
	// stampFlags writes the flags into the sidecar and the cue chunk, and
	// never fails the take (it logs), matching a save.
	stampFlags(outPath, flags)
	log.Printf("[*] cut %s from %s [%d, %d) — %.1fs", name, req.Source, req.StartFrame, req.EndFrame,
		float64(total)/float64(info.SampleRate))
	return name, nil
}

// writeCutWAV streams the region through the fades into a canonical 44-byte
// header WAV and writes the peaks file beside it.
func writeCutWAV(srcPath, outPath string, info WAVInfo, from, to, fade int64) error {
	total := to - from
	ch := info.Channels
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriterSize(f, 1<<18)
	if err := writeWAVHeader(w, uint32(total*int64(ch)*4), ch, info.SampleRate); err != nil {
		return err
	}
	acc := newPeakAccumulator(ch, int(total))
	le := binary.LittleEndian
	var raw [4]byte
	_, err = ReadFrames(srcPath, from, to, 1<<14, func(block []int32, first int64) error {
		rel := first - from
		applyFades(block, rel, total, ch, fade)
		n := len(block) / ch
		for i := 0; i < n; i++ {
			for c := 0; c < ch; c++ {
				v := block[i*ch+c]
				le.PutUint32(raw[:], uint32(v))
				if _, err := w.Write(raw[:]); err != nil {
					return err
				}
				acc.add(c, int(rel)+i, float32(v)/float32(1<<31))
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := WritePeaks(peaksPath(outPath), acc.finish(info.SampleRate, int(total))); err != nil {
		log.Printf("[!] peaks for %s: %v", filepath.Base(outPath), err)
	}
	return nil
}
```

Check `WriteWAV` for the exact float divisor it feeds the accumulator (`float32(1<<31)` or `math.MaxInt32`) and use the same one here, so cut peaks and saved peaks agree.

- [ ] **Step 7: Run tests to verify they pass**

Run: `go test ./internal/audio -run 'TestFade|TestApplyFades|TestCut|TestMetaRoundTripsDownbeat' -v`
Expected: all PASS. If the flag-order or `"jam cut"` label assertions fail, check `stampFlags` normalises and writes `m.Flags` on top of the sidecar `Cut` just wrote (it does `ReadMeta` then sets `Flags` then `WriteMeta`, so the label and BPM survive).

- [ ] **Step 8: Commit**

```bash
gofmt -l . && go vet ./... && go test -race ./internal/...
git add internal/audio/fade.go internal/audio/cut.go internal/audio/cut_test.go internal/audio/meta.go internal/audio/save.go
git commit -m "Cut a faded region of a take into a new take"
```

---

### Task 5: `POST /api/cut`

**Files:**
- Modify: `internal/api/api.go` (routes ~line 69, new handler near `handleTakePatch`)
- Test: `internal/api/api_test.go`
- Modify: `docs/api.md`

**Interfaces:**
- Consumes: `audio.Cut`, `audio.CutRequest`, `audio.ErrRange`, `audio.ErrTooShort`, `audio.FreeGB`, `audio.MakePreview`, `sanitizeLabel`, `a.cfg.MinFreeGB`, `a.cfg.OutChannels()`.
- Produces: `POST /api/cut?file=` body `{"start_frame","end_frame","label"?}` → `200 {"name": "..."}`; 400 bad body/frames/too short, 404 missing take, 507 low disk.

- [ ] **Step 1: Write the failing tests**

```go
func postJSON(t *testing.T, r *mux.Router, url, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, url, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestCutWritesANewTakeAndReturnsItsName(t *testing.T) {
	r, dir := newTestAPI(t)
	writeRealTake(t, dir, "jam_src.wav", 48000)

	w := postJSON(t, r, "/api/cut?file=jam_src.wav", `{"start_frame":1000,"end_frame":9000,"label":" hit "}`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	var body struct{ Name string }
	json.Unmarshal(w.Body.Bytes(), &body)
	if !strings.HasPrefix(body.Name, "jam_") || !strings.HasSuffix(body.Name, ".wav") || body.Name == "jam_src.wav" {
		t.Fatalf("name = %q", body.Name)
	}
	info, err := audio.ReadWAVInfo(filepath.Join(dir, body.Name))
	if err != nil || info.Frames() != 8000 {
		t.Errorf("frames = %d err = %v, want 8000", info.Frames(), err)
	}
	if m := audio.ReadMeta(filepath.Join(dir, body.Name)); m.Label != "hit" {
		t.Errorf("label = %q, want sanitized %q", m.Label, "hit")
	}
	// It shows up in the list.
	lw := do(t, r, http.MethodGet, "/api/jams")
	if !strings.Contains(lw.Body.String(), body.Name) {
		t.Error("cut is not listed")
	}
}

func TestCutValidation(t *testing.T) {
	r, dir := newTestAPI(t)
	writeRealTake(t, dir, "jam_src.wav", 48000)
	cases := map[string]struct {
		url, body string
		want      int
	}{
		"missing take": {"/api/cut?file=jam_nope.wav", `{"start_frame":0,"end_frame":1000}`, http.StatusNotFound},
		"bad json":     {"/api/cut?file=jam_src.wav", `{`, http.StatusBadRequest},
		"inverted":     {"/api/cut?file=jam_src.wav", `{"start_frame":500,"end_frame":100}`, http.StatusBadRequest},
		"past end":     {"/api/cut?file=jam_src.wav", `{"start_frame":0,"end_frame":48001}`, http.StatusBadRequest},
		"too short":    {"/api/cut?file=jam_src.wav", `{"start_frame":0,"end_frame":288}`, http.StatusBadRequest},
		"no file":      {"/api/cut", `{"start_frame":0,"end_frame":1000}`, http.StatusBadRequest},
		"traversal":    {"/api/cut?file=../jam_src.wav", `{"start_frame":0,"end_frame":1000}`, http.StatusBadRequest},
	}
	for name, c := range cases {
		if w := postJSON(t, r, c.url, c.body); w.Code != c.want {
			t.Errorf("%s: status = %d, want %d (%s)", name, w.Code, c.want, w.Body.String())
		}
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("rejected cuts left files: %d entries", len(entries))
	}
}

func TestCutRefusesWhenDiskIsLow(t *testing.T) {
	dir := t.TempDir()
	a := New(&config.Config{OutputDir: dir, MinFreeGB: 1e9}, nil, nil, nil, nil)
	r := mux.NewRouter()
	a.SetupRoutes(r)
	writeRealTake(t, dir, "jam_src.wav", 48000)
	if w := postJSON(t, r, "/api/cut?file=jam_src.wav", `{"start_frame":0,"end_frame":1000}`); w.Code != http.StatusInsufficientStorage {
		t.Errorf("status = %d, want 507", w.Code)
	}
}
```

Add `"os"` to the test file's imports if it is not there.

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/api -run TestCut -v`
Expected: 404 for every request (no route) — fails.

- [ ] **Step 3: Add the route and handler**

Route, next to the take PATCH:

```go
	r.HandleFunc("/api/cut", a.handleCut).Methods(http.MethodPost)
```

Handler:

```go
// handleCut exports a region of a take as a new take. It takes the frames
// from the body, not from the take's trim, so the page can export without a
// round trip to save the region first and a script can cut any range.
//
// The disk guard is the same one Save applies, for the same reason: a cut
// of a 15-minute take is a 15-minute take.
func (a *API) handleCut(w http.ResponseWriter, r *http.Request) {
	name, err := a.safeTakeName(r.URL.Query().Get("file"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if _, err := os.Stat(filepath.Join(a.cfg.OutputDir, name)); err != nil {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	var body struct {
		StartFrame int64  `json:"start_frame"`
		EndFrame   int64  `json:"end_frame"`
		Label      string `json:"label"`
	}
	if err := dec.Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if body.StartFrame < 0 || body.EndFrame <= body.StartFrame {
		writeErr(w, http.StatusBadRequest, "need 0 <= start_frame < end_frame")
		return
	}
	if free, _ := audio.FreeGB(a.cfg.OutputDir); free < a.cfg.MinFreeGB {
		writeErr(w, http.StatusInsufficientStorage,
			fmt.Sprintf("low disk: %.2f GB free, need %.2f GB", free, a.cfg.MinFreeGB))
		return
	}

	out, err := audio.Cut(a.cfg.OutputDir, audio.CutRequest{
		Source: name, StartFrame: body.StartFrame, EndFrame: body.EndFrame,
		Label: sanitizeLabel(body.Label),
	}, time.Now())
	switch {
	case errors.Is(err, audio.ErrRange):
		writeErr(w, http.StatusBadRequest, "region is past the end of the take")
		return
	case errors.Is(err, audio.ErrTooShort):
		writeErr(w, http.StatusBadRequest, "region is too short to cut")
		return
	case err != nil:
		log.Printf("cut %s: %v", name, err)
		writeErr(w, http.StatusInternalServerError, "could not cut")
		return
	}
	// The preview needs ffmpeg and the channel config; never block the
	// response on it, and never fail the cut because of it -- same as Save.
	go audio.MakePreview(a.cfg, filepath.Join(a.cfg.OutputDir, out), len(a.cfg.OutChannels()))
	writeJSON(w, http.StatusOK, map[string]string{"name": out})
}
```

`a.cfg.OutChannels()` on a test config with no `SaveChannels` returns an empty slice; `MakePreview` with `outCh == 0` takes the `else if outCh == 1` neither branch and just runs ffmpeg, which is fine in tests (it fails and logs). Check `OutChannels()` does not panic on an empty config; if it does, guard with `if a.cfg.Channels > 0`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/api -run TestCut -v`
Expected: PASS ×3. Note the two-cuts-in-one-second name collision: `Cut` names by second. In `TestCutValidation` only rejected cuts run so nothing collides. In `TestCutWritesANewTake` one cut runs. If a later test cuts twice within a second, pass distinct `now` values, or accept the second cut overwriting. **Add** a collision guard to `Cut` now rather than later: if `outPath` exists, append `_2`, `_3`, … before `.wav` until it does not. Write a test for it in `cut_test.go`:

```go
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
```

and in `Cut`, after computing `name`:

```go
	outPath := filepath.Join(dir, name)
	for n := 2; exists(outPath); n++ {
		name = fmt.Sprintf("jam_%s_%d.wav", now.Format("2006-01-02_150405"), n)
		outPath = filepath.Join(dir, name)
	}
```

- [ ] **Step 5: Document and commit**

In `docs/api.md`, add to the route table:

```markdown
| `POST /api/cut?file=` | Export a region of a take as a new take, with 3ms declick fades |
```

and a section:

````markdown
## `POST /api/cut?file=`

Body:

```json
{ "start_frame": 480000, "end_frame": 998400, "label": "the drop" }
```

Writes frames `[start_frame, end_frame)` of the take as a new take
`jam_<now>.wav` in the same directory, with a linear 3ms fade at each edge
and the audio between them byte-identical to the source. The new sidecar
carries the given `label` (sanitized like a take label; default
`"<source label or stem> cut"`), the source's `bpm`, any flags inside the
region rebased to it, and a `source` field `{name, start_frame, end_frame}`.
Star, trim and downbeat are not copied. The source is never modified.
The preview mp3 is rendered in the background, as after a save.

Response: `200 {"name": "jam_2026-09-10_221441.wav"}`.

| Status | When |
|---|---|
| 400 | Bad `file`, malformed body, inverted or out-of-range frames, or a region shorter than two fades (289 frames at 48kHz) |
| 404 | No such take |
| 507 | Below `MIN_FREE_GB` |
````

```bash
gofmt -l . && go vet ./... && go test -race ./internal/...
git add internal/api/api.go internal/api/api_test.go internal/audio/cut.go internal/audio/cut_test.go docs/api.md
git commit -m "Export a region of a take as a new take over POST /api/cut"
```

---

### Task 6: Audition slice — `GET /api/slice`

**Files:**
- Create: `internal/audio/slice.go`
- Test: `internal/audio/slice_test.go`, `internal/api/api_test.go`
- Modify: `internal/api/api.go`, `docs/api.md`

**Interfaces:**
- Consumes: `ReadFrames`, `applyFades`, `FadeFrames`, `ErrRange`.
- Produces: `func WriteSlice16(w io.Writer, path string, from, to int64) error` — a complete 16-bit PCM WAV of the faded range; `func SliceBytes(info WAVInfo, from, to int64) int64` — total byte length for `Content-Length`; `const MaxSliceSeconds = 60`; `var ErrTooLong`.
- Produces: `GET /api/slice?file=&from=&to=` → `audio/wav`.

- [ ] **Step 1: Write the failing tests**

```go
// internal/audio/slice_test.go
package audio

import (
	"bytes"
	"encoding/binary"
	"errors"
	"path/filepath"
	"testing"
)

func TestWriteSlice16ProducesAFadedSixteenBitWAV(t *testing.T) {
	p := filepath.Join(t.TempDir(), "jam_s.wav")
	data := make([]int32, 48000*2)
	for i := range data {
		data[i] = 1 << 30 // half scale
	}
	if _, err := WriteWAV(p, data, 2, []int{0, 1}, 48000); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteSlice16(&buf, p, 1000, 3000); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	if int64(len(b)) != SliceBytes(WAVInfo{Channels: 2, SampleRate: 48000, BitsPerSample: 32}, 1000, 3000) {
		t.Errorf("len = %d, want SliceBytes = %d", len(b), SliceBytes(WAVInfo{Channels: 2, SampleRate: 48000, BitsPerSample: 32}, 1000, 3000))
	}
	le := binary.LittleEndian
	if string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" || le.Uint16(b[34:36]) != 16 || le.Uint16(b[22:24]) != 2 {
		t.Fatalf("bad header: bits=%d ch=%d", le.Uint16(b[34:36]), le.Uint16(b[22:24]))
	}
	if le.Uint32(b[40:44]) != 2000*2*2 {
		t.Errorf("data size = %d, want %d", le.Uint32(b[40:44]), 2000*2*2)
	}
	// Frame 0 silent, mid exactly half scale in 16-bit (1<<14), last faded.
	s := func(frame int) int16 { return int16(le.Uint16(b[44+frame*4:])) }
	if s(0) != 0 || s(1000) != 1<<14 || s(1999) >= 1<<14 || s(1999) == 0 {
		t.Errorf("samples: first=%d mid=%d last=%d", s(0), s(1000), s(1999))
	}
}

func TestWriteSlice16Limits(t *testing.T) {
	p := filepath.Join(t.TempDir(), "jam_s.wav")
	if _, err := WriteWAV(p, make([]int32, 48000*61*2), 2, []int{0, 1}, 48000); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := WriteSlice16(&buf, p, 0, 48000*60+1); !errors.Is(err, ErrTooLong) {
		t.Errorf("61s: err = %v, want ErrTooLong", err)
	}
	if err := WriteSlice16(&buf, p, 48000*61-10, 48000*61+1); !errors.Is(err, ErrRange) {
		t.Errorf("past end: err = %v, want ErrRange", err)
	}
}
```

And in `internal/api/api_test.go`:

```go
func TestSliceStreamsASixteenBitWAV(t *testing.T) {
	r, dir := newTestAPI(t)
	writeRealTake(t, dir, "jam_s.wav", 48000)
	w := do(t, r, http.MethodGet, "/api/slice?file=jam_s.wav&from=100&to=2100")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	}
	if ct := w.Header().Get("Content-Type"); ct != "audio/wav" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cl := w.Header().Get("Content-Length"); cl != strconv.Itoa(44+2000*2*2) {
		t.Errorf("Content-Length = %q, want %d", cl, 44+2000*2*2)
	}
	if w.Body.Len() != 44+2000*2*2 {
		t.Errorf("body = %d bytes", w.Body.Len())
	}
	for _, q := range []string{"from=0&to=0", "from=0&to=48001", "from=0", "from=a&to=10"} {
		if w := do(t, r, http.MethodGet, "/api/slice?file=jam_s.wav&"+q); w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", q, w.Code)
		}
	}
	if w := do(t, r, http.MethodGet, "/api/slice?file=jam_nope.wav&from=0&to=10"); w.Code != http.StatusNotFound {
		t.Errorf("missing: status = %d, want 404", w.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/audio -run TestWriteSlice16 -v; go test ./internal/api -run TestSlice -v`
Expected: compile failures / 404.

- [ ] **Step 3: Create `slice.go`**

```go
// internal/audio/slice.go
package audio

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// MaxSliceSeconds caps an audition slice. 60s of stereo decodes to ~23MB of
// float32 in a browser, which a phone handles; the page auditions longer
// regions through the mp3 preview instead.
const MaxSliceSeconds = 60

// ErrTooLong is a slice over MaxSliceSeconds.
var ErrTooLong = errors.New("slice longer than the audition cap")

// SliceBytes is the byte length of the WAV WriteSlice16 emits for [from, to).
func SliceBytes(info WAVInfo, from, to int64) int64 {
	return 44 + (to-from)*int64(info.Channels)*2
}

// WriteSlice16 writes frames [from, to) of a take to w as a complete 16-bit
// PCM WAV with the same 3ms fades a cut applies, so what the page auditions
// is exactly what a cut will produce. 16-bit because browsers -- iOS Safari
// in particular -- do not reliably decode 32-bit integer WAV; the audition
// is not archival. Conversion is an arithmetic shift, no dither.
func WriteSlice16(w io.Writer, path string, from, to int64) error {
	info, err := ReadWAVInfo(path)
	if err != nil {
		return err
	}
	if info.BitsPerSample != 32 {
		return ErrBitDepth
	}
	if from < 0 || to <= from || to > info.Frames() {
		return fmt.Errorf("%w: [%d, %d) of %d frames", ErrRange, from, to, info.Frames())
	}
	if to-from > int64(MaxSliceSeconds*info.SampleRate) {
		return ErrTooLong
	}
	ch := info.Channels
	total := to - from
	fade := FadeFrames(info.SampleRate)

	bw := bufio.NewWriterSize(w, 1<<16)
	le := binary.LittleEndian
	var hdr [44]byte
	dataBytes := uint32(total * int64(ch) * 2)
	copy(hdr[0:4], "RIFF")
	le.PutUint32(hdr[4:8], dataBytes+36)
	copy(hdr[8:12], "WAVE")
	copy(hdr[12:16], "fmt ")
	le.PutUint32(hdr[16:20], 16)
	le.PutUint16(hdr[20:22], 1)
	le.PutUint16(hdr[22:24], uint16(ch))
	le.PutUint32(hdr[24:28], uint32(info.SampleRate))
	le.PutUint32(hdr[28:32], uint32(info.SampleRate*ch*2))
	le.PutUint16(hdr[32:34], uint16(ch*2))
	le.PutUint16(hdr[34:36], 16)
	copy(hdr[36:40], "data")
	le.PutUint32(hdr[40:44], dataBytes)
	if _, err := bw.Write(hdr[:]); err != nil {
		return err
	}

	var s [2]byte
	_, err = ReadFrames(path, from, to, 1<<14, func(block []int32, first int64) error {
		applyFades(block, first-from, total, ch, fade)
		for _, v := range block {
			le.PutUint16(s[:], uint16(int16(v>>16)))
			if _, err := bw.Write(s[:]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return bw.Flush()
}
```

- [ ] **Step 4: Add the route and handler**

```go
	r.HandleFunc("/api/slice", a.handleSlice).Methods(http.MethodGet)
```

```go
// handleSlice streams a faded 16-bit WAV of [from, to) for the waveform
// page's region loop. Content-Length is set from the frame count so the
// browser can show progress; nothing is buffered server-side.
func (a *API) handleSlice(w http.ResponseWriter, r *http.Request) {
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
	if to > info.Frames() {
		writeErr(w, http.StatusBadRequest, "range is past the end of the take")
		return
	}
	if to-from > int64(audio.MaxSliceSeconds*info.SampleRate) {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("slice longer than %ds", audio.MaxSliceSeconds))
		return
	}
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Content-Length", strconv.FormatInt(audio.SliceBytes(info, from, to), 10))
	w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	if err := audio.WriteSlice16(w, path, from, to); err != nil {
		// Headers are gone; all we can do is log and let the client see a
		// short body, which decodeAudioData rejects.
		log.Printf("slice %s: %v", name, err)
	}
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./internal/audio -run TestWriteSlice16 -v; go test ./internal/api -run TestSlice -v`
Expected: PASS.

- [ ] **Step 6: Document and commit**

`docs/api.md` route table:

```markdown
| `GET /api/slice?file=&from=&to=` | A region as a 16-bit WAV with the same fades a cut gets, for auditioning |
```

Section:

```markdown
## `GET /api/slice?file=&from=&to=`

Streams frames `[from, to)` as a complete **16-bit** PCM WAV with the same
3ms fades `POST /api/cut` applies, so what the waveform page loops is exactly
what a cut will produce. 16-bit because browsers cannot reliably decode
32-bit integer WAV. Capped at 60 seconds. `Content-Length` is exact.

| Status | When |
|---|---|
| 400 | Bad `file`, non-integer or inverted frames, past the end, or over 60s |
| 404 | No such take |
```

```bash
gofmt -l . && go vet ./... && go test -race ./internal/...
git add internal/audio/slice.go internal/audio/slice_test.go internal/api/api.go internal/api/api_test.go docs/api.md
git commit -m "Stream a faded 16-bit slice of a take for auditioning"
```

---

### Task 7: `downbeat_frame` on `PATCH /api/take`

**Files:**
- Modify: `internal/api/api.go` (`handleTakePatch` body struct ~line 505 and field handling), `docs/api.md`
- Test: `internal/api/api_test.go`

**Interfaces:**
- Consumes: `Meta.DownbeatFrame` from Task 4.
- Produces: PATCH accepts `"downbeat_frame": n | null`; response echoes `downbeat_frame`.

- [ ] **Step 1: Write the failing test**

```go
func TestPatchTakeSetsAndClearsDownbeat(t *testing.T) {
	r, dir := newTestAPI(t)
	writeRealTake(t, dir, "jam_d.wav", 1000)
	wav := filepath.Join(dir, "jam_d.wav")

	if w := patch(t, r, "jam_d.wav", `{"downbeat_frame":480}`); w.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", w.Code, w.Body.String())
	} else if !strings.Contains(w.Body.String(), `"downbeat_frame":480`) {
		t.Errorf("response does not echo downbeat: %s", w.Body.String())
	}
	if m := audio.ReadMeta(wav); m.DownbeatFrame == nil || *m.DownbeatFrame != 480 {
		t.Errorf("sidecar downbeat = %v", m.DownbeatFrame)
	}
	if w := patch(t, r, "jam_d.wav", `{"label":"x"}`); w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
	if m := audio.ReadMeta(wav); m.DownbeatFrame == nil {
		t.Error("an unrelated patch cleared the downbeat")
	}
	for _, bad := range []string{`{"downbeat_frame":-1}`, `{"downbeat_frame":1000}`, `{"downbeat_frame":"x"}`} {
		if w := patch(t, r, "jam_d.wav", bad); w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", bad, w.Code)
		}
	}
	if w := patch(t, r, "jam_d.wav", `{"downbeat_frame":null}`); w.Code != http.StatusOK {
		t.Fatal(w.Code)
	}
	if m := audio.ReadMeta(wav); m.DownbeatFrame != nil {
		t.Error("null did not clear the downbeat")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/api -run TestPatchTakeSetsAndClearsDownbeat -v`
Expected: FAIL — the field is ignored, sidecar downbeat nil.

- [ ] **Step 3: Implement**

In the PATCH body struct add `Downbeat json.RawMessage \`json:"downbeat_frame"\``. After the BPM block:

```go
	// RawMessage like the others: absent, null and a value are three states.
	if body.Downbeat != nil {
		if string(body.Downbeat) == "null" {
			m.DownbeatFrame = nil
		} else {
			var v int64
			if err := json.Unmarshal(body.Downbeat, &v); err != nil || v < 0 {
				writeErr(w, http.StatusBadRequest, "downbeat_frame must be a non-negative integer")
				return
			}
			if info, err := audio.ReadWAVInfo(wav); err == nil && v >= info.Frames() {
				writeErr(w, http.StatusBadRequest, "downbeat_frame is past the end of the take")
				return
			}
			m.DownbeatFrame = &v
		}
	}
```

Add `Downbeat *int64 \`json:"downbeat_frame"\`` to the response struct with `Downbeat: m.DownbeatFrame`.

- [ ] **Step 4: Run to verify it passes, document, commit**

Run: `go test ./internal/api -run TestPatchTake -v` — all PASS.

`docs/api.md`, PATCH field table:

```markdown
| `downbeat_frame` | integer or `null` | Where bar 1 falls, for the waveform page's grid. `>= 0` and less than the take's frame count; `null` clears |
```

Also add `"downbeat_frame": null` and `"source"` to the documented `/api/jams` entry shape, noting `source` is present only on cuts. Check `audio.Take` in `save.go` — add `DownbeatFrame *int64 \`json:"downbeat_frame,omitempty"\`` and `Source *Source \`json:"source,omitempty"\`` to `Take` and copy them from the sidecar in `ListTakes` where `Label`, `BPM`, `Flags` are copied, so the page can read the downbeat from the listing. Add one assertion to `TestCutWritesANewTakeAndReturnsItsName` that the `/api/jams` body contains `"source"`.

```bash
gofmt -l . && go vet ./... && go test -race ./internal/...
git add internal/api/api.go internal/api/api_test.go internal/audio/save.go docs/api.md
git commit -m "Store a take's downbeat and expose cut lineage in the listing"
```

---

### Task 8: `geometry.js` with `node --test`, wired into CI

**Files:**
- Create: `web/static/lib/wave/geometry.js`, `web/static/lib/wave/geometry.test.js`
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Produces (all pure, all exported):
  - `frameToX(frame, view)` / `xToFrame(x, view)` where `view = {start, fpp, width}` (start frame at x=0, frames per CSS pixel, width in CSS px).
  - `TILE_BUCKETS = 1024`
  - `levelFor(fpp, dpr)` → smallest integer `k >= 0` with `2^k >= fpp * dpr / 2` (at most two buckets per device pixel).
  - `tileSpan(level)` → frames per tile = `TILE_BUCKETS * 2^level`.
  - `tilesFor(view, level, totalFrames)` → `{first, last}` tile indices covering the viewport plus one on each side, clamped to the file.
  - `fileLevel(totalFrames)` → the level at which the 1024-bucket file peaks are exact: `ceil(log2(totalFrames / 1024))`. Levels `>= fileLevel` are never fetched.
  - `gridLines(view, {bpm, sampleRate, downbeat})` → `[{frame, bar: bool}]` for lines inside the viewport, with beats omitted when `< 8px` apart and bars omitted when `< 4px` apart. Empty when `bpm` is falsy.
  - `barBeat(frame, {bpm, sampleRate, downbeat})` → `"3.2"` (1-based bar and beat, 4 beats per bar; negative bars before the downbeat count down from 0: `"-1.4"`).
  - `fmtTime(frame, sampleRate)` → `"m:ss.mmm"`.
  - `clampRegion(region, totalFrames, minLen)` → `{start, end}` with `0 <= start`, `end <= total`, `end - start >= minLen`.

- [ ] **Step 1: Write the failing tests**

```js
// web/static/lib/wave/geometry.test.js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  frameToX, xToFrame, levelFor, tileSpan, tilesFor, fileLevel,
  gridLines, barBeat, fmtTime, clampRegion, TILE_BUCKETS,
} from './geometry.js';

const view = { start: 48000, fpp: 100, width: 390 };

test('frame and pixel round-trip', () => {
  assert.equal(frameToX(48000, view), 0);
  assert.equal(frameToX(48000 + 39000, view), 390);
  assert.equal(xToFrame(195, view), 48000 + 19500);
  assert.equal(xToFrame(frameToX(50123, view), view), 50123);
});

test('level picks at most two buckets per device pixel', () => {
  assert.equal(levelFor(1, 1), 0);      // 1 frame/px: 2^0 >= 0.5
  assert.equal(levelFor(4, 1), 1);      // need 2^k >= 2
  assert.equal(levelFor(4, 3), 3);      // need 2^k >= 6 -> 8
  assert.equal(levelFor(1000, 2), 10);  // need >= 1000 -> 1024
});

test('tile span and indices with one tile margin, clamped', () => {
  assert.equal(tileSpan(0), TILE_BUCKETS);
  assert.equal(tileSpan(3), TILE_BUCKETS * 8);
  // level 2: 4096 frames per tile. View [48000, 87000).
  const t = tilesFor(view, 2, 1_000_000);
  assert.deepEqual(t, { first: Math.floor(48000 / 4096) - 1, last: Math.floor(86999 / 4096) + 1 });
  const edge = tilesFor({ start: 0, fpp: 10, width: 100 }, 0, 2000);
  assert.deepEqual(edge, { first: 0, last: 1 }); // 2000 frames = tiles 0..1, no tile -1 or 2
});

test('file level is where the saved peaks are exact', () => {
  assert.equal(fileLevel(1024), 0);
  assert.equal(fileLevel(1024 * 8), 3);
  assert.equal(fileLevel(1024 * 8 + 1), 4);
  assert.equal(fileLevel(48000 * 900), 16); // 15 min: 43.2M/1024 = 42187 -> 2^16
});

test('grid lines: bars and beats from the downbeat, hidden when dense', () => {
  const g = { bpm: 120, sampleRate: 48000, downbeat: 0 }; // 24000 frames per beat
  // 100 fpp: beat = 240px apart, all shown.
  const lines = gridLines({ start: 0, fpp: 100, width: 390 }, g);
  assert.deepEqual(lines.map((l) => [l.frame, l.bar]), [[0, true], [24000, false]]);
  // 4000 fpp: beats 6px apart -> hidden; bars 24px -> shown.
  const coarse = gridLines({ start: 0, fpp: 4000, width: 100 }, g);
  assert.ok(coarse.every((l) => l.bar));
  assert.equal(coarse.length, 5); // bars at 0, 96000, 192000, 288000, 384000 within 400000
  // 30000 fpp: bars 3.2px -> nothing.
  assert.deepEqual(gridLines({ start: 0, fpp: 30000, width: 100 }, g), []);
  // No bpm, no grid.
  assert.deepEqual(gridLines(view, { bpm: null, sampleRate: 48000, downbeat: 0 }), []);
  // Downbeat offset shifts everything; lines before the downbeat still draw.
  const shifted = gridLines({ start: 0, fpp: 100, width: 390 }, { ...g, downbeat: 10000 });
  assert.deepEqual(shifted.map((l) => l.frame), [10000, 34000]);
});

test('bar.beat readout', () => {
  const g = { bpm: 120, sampleRate: 48000, downbeat: 0 };
  assert.equal(barBeat(0, g), '1.1');
  assert.equal(barBeat(24000 * 5, g), '2.2');
  assert.equal(barBeat(24000 * 5 + 100, g), '2.2');
  assert.equal(barBeat(-1, g), '-1.4');
  assert.equal(barBeat(0, { ...g, bpm: null }), '');
});

test('time readout', () => {
  assert.equal(fmtTime(0, 48000), '0:00.000');
  assert.equal(fmtTime(48000 * 61.5, 48000), '1:01.500');
  assert.equal(fmtTime(47, 48000), '0:00.000'); // floors to ms
});

test('region clamp keeps order, bounds and minimum length', () => {
  assert.deepEqual(clampRegion({ start: -5, end: 100 }, 1000, 10), { start: 0, end: 100 });
  assert.deepEqual(clampRegion({ start: 900, end: 2000 }, 1000, 10), { start: 900, end: 1000 });
  assert.deepEqual(clampRegion({ start: 500, end: 503 }, 1000, 10), { start: 500, end: 510 });
  assert.deepEqual(clampRegion({ start: 995, end: 998 }, 1000, 10), { start: 990, end: 1000 });
});
```

- [ ] **Step 2: Run to verify it fails**

Run: `node --test web/static/lib/wave/`
Expected: fails to import `./geometry.js`.

- [ ] **Step 3: Write `geometry.js`**

```js
// web/static/lib/wave/geometry.js
// Pure math for the waveform page. No DOM, no fetch, so it runs under
// `node --test`. Every other wave module imports from here rather than
// re-deriving frame/pixel/tile arithmetic.

export const TILE_BUCKETS = 1024;

// view = { start: first frame at x=0, fpp: frames per CSS pixel, width: CSS px }
export function frameToX(frame, view) { return (frame - view.start) / view.fpp; }
export function xToFrame(x, view) { return Math.round(view.start + x * view.fpp); }

// The finest tile level with at most two buckets per device pixel.
export function levelFor(fpp, dpr) {
  const need = (fpp * dpr) / 2;
  let k = 0;
  while (2 ** k < need) k++;
  return k;
}

export function tileSpan(level) { return TILE_BUCKETS * 2 ** level; }

export function tilesFor(view, level, totalFrames) {
  const span = tileSpan(level);
  const endFrame = Math.min(totalFrames, view.start + view.width * view.fpp) - 1;
  const maxTile = Math.floor(Math.max(0, totalFrames - 1) / span);
  const first = Math.max(0, Math.floor(Math.max(0, view.start) / span) - 1);
  const last = Math.min(maxTile, Math.floor(Math.max(0, endFrame) / span) + 1);
  return { first, last };
}

export function fileLevel(totalFrames) {
  return Math.max(0, Math.ceil(Math.log2(totalFrames / TILE_BUCKETS)));
}

const BEATS_PER_BAR = 4;
const MIN_BEAT_PX = 8;
const MIN_BAR_PX = 4;

export function framesPerBeat({ bpm, sampleRate }) { return (sampleRate * 60) / bpm; }

export function gridLines(view, grid) {
  if (!grid.bpm) return [];
  const fpb = framesPerBeat(grid);
  const beatPx = fpb / view.fpp;
  const barPx = beatPx * BEATS_PER_BAR;
  if (barPx < MIN_BAR_PX) return [];
  const showBeats = beatPx >= MIN_BEAT_PX;
  const step = showBeats ? fpb : fpb * BEATS_PER_BAR;
  const endFrame = view.start + view.width * view.fpp;
  const out = [];
  let n = Math.ceil((view.start - grid.downbeat) / step);
  for (;;) {
    const frame = grid.downbeat + n * step;
    if (frame >= endFrame) break;
    const beatIndex = showBeats ? n : n * BEATS_PER_BAR;
    out.push({ frame: Math.round(frame), bar: ((beatIndex % BEATS_PER_BAR) + BEATS_PER_BAR) % BEATS_PER_BAR === 0 });
    n++;
  }
  return out;
}

export function barBeat(frame, grid) {
  if (!grid.bpm) return '';
  const fpb = framesPerBeat(grid);
  const beats = Math.floor((frame - grid.downbeat) / fpb);
  const bar = Math.floor(beats / BEATS_PER_BAR);
  const beat = ((beats % BEATS_PER_BAR) + BEATS_PER_BAR) % BEATS_PER_BAR;
  const barLabel = bar >= 0 ? bar + 1 : bar;
  return `${barLabel}.${beat + 1}`;
}

export function fmtTime(frame, sampleRate) {
  const ms = Math.floor((frame / sampleRate) * 1000);
  const m = Math.floor(ms / 60000);
  const s = Math.floor((ms % 60000) / 1000);
  const r = ms % 1000;
  return `${m}:${String(s).padStart(2, '0')}.${String(r).padStart(3, '0')}`;
}

export function clampRegion(region, totalFrames, minLen) {
  let start = Math.max(0, Math.round(region.start));
  let end = Math.min(totalFrames, Math.round(region.end));
  if (end - start < minLen) {
    end = start + minLen;
    if (end > totalFrames) {
      end = totalFrames;
      start = Math.max(0, end - minLen);
    }
  }
  return { start, end };
}
```

Check `barBeat(-1)`: beats = floor(-1/24000) = -1, bar = floor(-1/4) = -1, beat = ((-1 % 4)+4)%4 = 3 → `"-1.4"`. ✓

- [ ] **Step 4: Run to verify it passes**

Run: `node --test web/static/lib/wave/`
Expected: 8 tests pass. If `tilesFor`'s edge case fails, recheck: `endFrame = min(2000, 0+1000) - 1 = 999`, `maxTile = floor(1999/1024) = 1`, `first = max(0, 0-1) = 0`, `last = min(1, floor(999/1024)+1) = 1`. ✓

- [ ] **Step 5: Wire into CI and commit**

In `.github/workflows/ci.yml`, after the `go test` step:

```yaml
      - uses: actions/setup-node@v4
        with:
          node-version: '22'

      # The waveform page's geometry is pure JS with node's built-in runner;
      # no package.json, no dependencies.
      - name: JS unit tests
        run: node --test web/static/lib/wave/
```

Verify a local `node --version` is 20 or newer (the Mac has 23.6).

```bash
node --test web/static/lib/wave/
git add web/static/lib/wave/geometry.js web/static/lib/wave/geometry.test.js .github/workflows/ci.yml
git commit -m "Pure geometry for the waveform page, tested under node --test"
```

---

### Task 9: `tiles.js` — tile cache with coarse fallback

**Files:**
- Create: `web/static/lib/wave/tiles.js`, `web/static/lib/wave/tiles.test.js`

**Interfaces:**
- Consumes: `levelFor`, `tileSpan`, `tilesFor`, `fileLevel`, `TILE_BUCKETS` from `geometry.js`.
- Produces: `class TileCache` constructed with `{ file, totalFrames, filePeaks, fetchFn = fetch, onChange, maxTiles = 256 }`:
  - `columns(view, dpr)` → `{ level, from, fpp, cols }` where `cols` is `Float32Array` of `[min0,max0,min1,max1,...]` per column (two channels interleaved, `2*channels` values per column) covering `view.width` CSS px, computed from the best available tiles for that level, falling back per-tile to the coarsest available ancestor and finally to the file peaks. Requests any missing tiles at `level` as a side effect.
  - `stop()` cancels retries.
  - Internals: cache `Map` key `${level}:${index}` → `PeakData`; in-flight `Set`; backoff per key from 500ms doubling to 8s; `onChange()` called when a tile lands; eviction of least-recently-drawn beyond `maxTiles`. A 404 sets `this.gone = true` and stops fetching.

- [ ] **Step 1: Write the failing tests**

```js
// web/static/lib/wave/tiles.test.js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { TileCache } from './tiles.js';

const SR = 48000;
const TOTAL = 1024 * 64; // 65536 frames: fileLevel = 6

function fakePeaks(from, buckets, value) {
  const data = [[], []];
  for (let i = 0; i < buckets; i++) { data[0].push(-value, value); data[1].push(-value / 2, value / 2); }
  return { version: 1, channels: 2, sample_rate: SR, duration: 0, buckets, from, data };
}

const filePeaks = fakePeaks(0, 1024, 0.1);

function fakeFetch(log, { fail = () => false } = {}) {
  return async (url) => {
    log.push(url);
    const u = new URL(url, 'http://x');
    const from = Number(u.searchParams.get('from'));
    const to = Number(u.searchParams.get('to'));
    const buckets = Number(u.searchParams.get('buckets'));
    if (fail(url)) return { ok: false, status: 500, json: async () => ({}) };
    return { ok: true, status: 200, json: async () => fakePeaks(from, buckets, 0.9) };
  };
}

test('zoomed-out view uses the file peaks and fetches nothing', () => {
  const log = [];
  const tc = new TileCache({ file: 'a.wav', totalFrames: TOTAL, filePeaks, fetchFn: fakeFetch(log), onChange() {} });
  const r = tc.columns({ start: 0, fpp: TOTAL / 390, width: 390 }, 1);
  assert.equal(log.length, 0);
  assert.equal(r.cols.length, 390 * 4);
  assert.ok(Math.abs(r.cols[1] - 0.1) < 1e-6);
});

test('zoomed-in view requests the tiles it needs once and then draws them', async () => {
  const log = [];
  let changes = 0;
  const tc = new TileCache({ file: 'a.wav', totalFrames: TOTAL, filePeaks, fetchFn: fakeFetch(log), onChange() { changes++; } });
  const view = { start: 5000, fpp: 4, width: 390 }; // level 1 at dpr 1: tile span 2048
  const first = tc.columns(view, 1);
  assert.ok(Math.abs(first.cols[1] - 0.1) < 1e-6, 'falls back to file peaks while loading');
  // tiles 1..4 (view [5000, 6560) -> tiles 2,3 plus margins 1 and 4)
  assert.equal(log.length, 4);
  assert.ok(log[0].includes('from=2048&to=4096&buckets=1024'));
  await new Promise((r) => setTimeout(r, 0));
  assert.equal(changes, 4);
  const second = tc.columns(view, 1);
  assert.equal(log.length, 4, 'no refetch');
  assert.ok(Math.abs(second.cols[1] - 0.9) < 1e-6, 'drawn from the tile now');
});

test('a failed tile keeps the fallback and retries with backoff', async () => {
  const log = [];
  let failing = true;
  const tc = new TileCache({ file: 'a.wav', totalFrames: TOTAL, filePeaks, fetchFn: fakeFetch(log, { fail: () => failing }), onChange() {} });
  tc.retryBase = 1; // ms, keep the test fast
  const view = { start: 0, fpp: 1, width: 100 }; // level 0, tile 0 only (+ margin 1)
  tc.columns(view, 1);
  await new Promise((r) => setTimeout(r, 5));
  assert.ok(log.length >= 3, `retried: ${log.length}`);
  failing = false;
  await new Promise((r) => setTimeout(r, 20));
  const r = tc.columns(view, 1);
  assert.ok(Math.abs(r.cols[1] - 0.9) < 1e-6);
  tc.stop();
});

test('a 404 marks the take gone and stops fetching', async () => {
  const log = [];
  const fetchFn = async (url) => { log.push(url); return { ok: false, status: 404, json: async () => ({}) }; };
  const tc = new TileCache({ file: 'a.wav', totalFrames: TOTAL, filePeaks, fetchFn, onChange() {} });
  tc.columns({ start: 0, fpp: 1, width: 100 }, 1);
  await new Promise((r) => setTimeout(r, 0));
  assert.equal(tc.gone, true);
  const n = log.length;
  tc.columns({ start: 3000, fpp: 1, width: 100 }, 1);
  assert.equal(log.length, n);
});

test('evicts least recently drawn tiles beyond maxTiles', async () => {
  const log = [];
  const tc = new TileCache({ file: 'a.wav', totalFrames: TOTAL, filePeaks, fetchFn: fakeFetch(log), onChange() {}, maxTiles: 3 });
  for (let i = 0; i < 6; i++) tc.columns({ start: i * 1024, fpp: 1, width: 10 }, 1);
  await new Promise((r) => setTimeout(r, 0));
  assert.ok(tc.cache.size <= 3, `cache size ${tc.cache.size}`);
});
```

- [ ] **Step 2: Run to verify it fails**

Run: `node --test web/static/lib/wave/`
Expected: `tiles.test.js` fails to import.

- [ ] **Step 3: Write `tiles.js`**

```js
// web/static/lib/wave/tiles.js
import { levelFor, tileSpan, tilesFor, fileLevel, TILE_BUCKETS } from './geometry.js';

// TileCache turns a viewport into drawable min/max columns, fetching range
// peaks for whatever tiles it lacks and drawing the coarsest thing it has
// in the meantime -- ultimately the 1024-bucket file peaks, which are
// always present. Nothing here touches the DOM.
export class TileCache {
  constructor({ file, totalFrames, filePeaks, fetchFn = globalThis.fetch, onChange, maxTiles = 256 }) {
    this.file = file;
    this.totalFrames = totalFrames;
    this.filePeaks = filePeaks;
    this.fetchFn = fetchFn;
    this.onChange = onChange;
    this.maxTiles = maxTiles;
    this.fileLevel = fileLevel(totalFrames);
    this.cache = new Map();     // key -> { pd, used }
    this.inflight = new Set();
    this.timers = new Map();    // key -> timeout id
    this.attempts = new Map();  // key -> count
    this.retryBase = 500;
    this.gone = false;
    this.tick = 0;
  }

  stop() {
    for (const t of this.timers.values()) clearTimeout(t);
    this.timers.clear();
  }

  key(level, i) { return `${level}:${i}`; }

  // Best available peaks covering tile (level, i): the tile itself, else the
  // nearest coarser ancestor that is cached, else the file. Returns
  // { pd, level } so the caller can map frames to buckets.
  best(level, i) {
    for (let l = level, idx = i; l < this.fileLevel; l++, idx = Math.floor(idx / 2)) {
      const hit = this.cache.get(this.key(l, idx));
      if (hit) { hit.used = ++this.tick; return { pd: hit.pd, level: l }; }
    }
    return { pd: this.filePeaks, level: this.fileLevel, isFile: true };
  }

  request(level, i) {
    const k = this.key(level, i);
    if (this.gone || this.cache.has(k) || this.inflight.has(k) || this.timers.has(k)) return;
    const span = tileSpan(level);
    const from = i * span;
    const to = Math.min(this.totalFrames, from + span);
    if (from >= to) return;
    this.inflight.add(k);
    const url = `/api/peaks?file=${encodeURIComponent(this.file)}&from=${from}&to=${to}&buckets=${TILE_BUCKETS}`;
    this.fetchFn(url).then(async (res) => {
      if (res.status === 404) { this.gone = true; this.stop(); return; }
      if (!res.ok) throw new Error(`status ${res.status}`);
      const pd = await res.json();
      this.cache.set(k, { pd, used: ++this.tick });
      this.attempts.delete(k);
      this.evict();
      this.onChange?.();
    }).catch(() => {
      const n = (this.attempts.get(k) || 0) + 1;
      this.attempts.set(k, n);
      const delay = Math.min(this.retryBase * 2 ** (n - 1), this.retryBase * 16);
      this.timers.set(k, setTimeout(() => { this.timers.delete(k); this.request(level, i); }, delay));
    }).finally(() => this.inflight.delete(k));
  }

  evict() {
    while (this.cache.size > this.maxTiles) {
      let oldest = null;
      for (const [k, v] of this.cache) if (!oldest || v.used < oldest[1].used) oldest = [k, v];
      this.cache.delete(oldest[0]);
    }
  }

  // Columns for the viewport: 2*channels floats per CSS pixel column.
  columns(view, dpr) {
    let level = levelFor(view.fpp, dpr);
    const ch = this.filePeaks.channels;
    const cols = new Float32Array(Math.ceil(view.width) * ch * 2);
    const useFile = level >= this.fileLevel;
    if (!useFile) {
      const { first, last } = tilesFor(view, level, this.totalFrames);
      for (let i = first; i <= last; i++) this.request(level, i);
    }
    for (let x = 0; x < Math.ceil(view.width); x++) {
      const f0 = view.start + x * view.fpp;
      const f1 = f0 + view.fpp;
      if (f1 <= 0 || f0 >= this.totalFrames) continue;
      const src = useFile
        ? { pd: this.filePeaks, level: this.fileLevel, isFile: true }
        : this.best(level, Math.floor(f0 / tileSpan(level)));
      const pd = src.pd;
      const bucketFrames = src.isFile ? this.totalFrames / pd.buckets : 2 ** src.level;
      const base = src.isFile ? 0 : pd.from;
      let b0 = Math.floor((f0 - base) / bucketFrames);
      let b1 = Math.ceil((f1 - base) / bucketFrames) - 1;
      b0 = Math.max(0, Math.min(pd.buckets - 1, b0));
      b1 = Math.max(b0, Math.min(pd.buckets - 1, b1));
      for (let c = 0; c < ch; c++) {
        let mn = Infinity, mx = -Infinity;
        for (let b = b0; b <= b1; b++) {
          const v0 = pd.data[c][b * 2], v1 = pd.data[c][b * 2 + 1];
          if (v0 < mn) mn = v0;
          if (v1 > mx) mx = v1;
        }
        cols[(x * ch + c) * 2] = mn;
        cols[(x * ch + c) * 2 + 1] = mx;
      }
    }
    return { level, cols, channels: ch };
  }
}
```

Note the tile-ancestor fallback in `best` only walks levels below `fileLevel`; a column whose tile is absent at every cached level lands on the file, which is what the first test asserts.

- [ ] **Step 4: Run to verify it passes**

Run: `node --test web/static/lib/wave/`
Expected: all pass. If the "requests once" test sees 4 fetches but a different first URL, recompute: level 1 span 2048; view [5000, 6560) → tiles 2..3; margins → 1..4; first URL is tile 1: `from=2048&to=4096`. ✓

- [ ] **Step 5: Commit**

```bash
git add web/static/lib/wave/tiles.js web/static/lib/wave/tiles.test.js
git commit -m "Tile cache for the waveform page with coarse fallback and backoff"
```

---

### Task 10: `clock.js` — one clock, two engines

**Files:**
- Create: `web/static/lib/wave/clock.js`

**Interfaces:**
- Produces `class Clock` constructed with `{ previewUrl, sampleRate, file, onTick, onError }`:
  - `position()` → frame (integer).
  - `play()`, `pause()`, `playing` (boolean), `seek(frame)`.
  - `async setLoop(region | null)` — region `{start, end}`; picks the slice engine when `end - start <= 60 * sampleRate`, else the preview engine loops by seeking.
  - `loop` (current region or null), `engine` (`'preview' | 'slice'`).
  - `destroy()`.
- No unit tests (Web Audio and `<audio>` are browser-only); verified by hand in Task 12.

- [ ] **Step 1: Write `clock.js`**

```js
// web/static/lib/wave/clock.js
// One clock for the page. Everything that needs "where are we" calls
// position(); nothing keeps its own time. Two engines sit behind it and
// exactly one is active:
//   preview -- an <audio> on the take's mp3, for scrubbing the whole take
//              and for looping regions too long to slice.
//   slice   -- /api/slice decoded into Web Audio and looped with an
//              AudioBufferSourceNode, so the loop point is sample-exact
//              and what plays is exactly what a cut will be.

export const SLICE_CAP_SECONDS = 60;

export class Clock {
  constructor({ previewUrl, sampleRate, file, onTick, onError }) {
    this.sr = sampleRate;
    this.file = file;
    this.onTick = onTick;
    this.onError = onError;
    this.audio = new Audio(previewUrl);
    this.audio.preload = 'auto';
    this.ctx = null;          // AudioContext, created on first play (iOS gesture rule)
    this.engine = 'preview';
    this.loop = null;
    this.playing = false;
    this.slice = null;        // { buffer, start, end } decoded region
    this.src = null;          // AudioBufferSourceNode while playing a slice
    this.sliceStartedAt = 0;  // ctx.currentTime when src started
    this.sliceOffset = 0;     // frame offset into the slice at start
    this.raf = 0;
    this.pendingFetch = 0;
    this.audio.addEventListener('ended', () => { this.playing = false; });
  }

  position() {
    if (this.engine === 'slice' && this.src && this.playing) {
      const elapsed = (this.ctx.currentTime - this.sliceStartedAt) * this.sr + this.sliceOffset;
      const len = this.slice.end - this.slice.start;
      return this.slice.start + Math.floor(elapsed % len);
    }
    return Math.floor(this.audio.currentTime * this.sr);
  }

  seek(frame) {
    frame = Math.max(0, frame);
    if (this.engine === 'slice' && this.slice) {
      const inside = frame >= this.slice.start && frame < this.slice.end;
      if (inside && this.playing) { this.stopSource(); this.startSource(frame - this.slice.start); return; }
      if (!inside) this.setLoop(null);
    }
    this.audio.currentTime = frame / this.sr;
  }

  async play() {
    if (this.engine === 'slice' && this.slice) {
      this.ensureCtx();
      if (this.ctx.state === 'suspended') await this.ctx.resume();
      this.startSource(Math.max(0, this.position() - this.slice.start));
    } else {
      try { await this.audio.play(); } catch (e) { this.onError?.('could not play the preview'); return; }
    }
    this.playing = true;
    this.tick();
  }

  pause() {
    if (this.engine === 'slice') {
      const at = this.position();
      this.stopSource();
      this.audio.currentTime = at / this.sr; // keep the preview in step
    } else {
      this.audio.pause();
    }
    this.playing = false;
    cancelAnimationFrame(this.raf);
  }

  // region null clears the loop and returns to the preview engine at the
  // current position. A region over the cap loops through the preview by
  // seeking back at its end (see tick).
  async setLoop(region) {
    const wasPlaying = this.playing;
    const at = this.position();
    this.loop = region;
    if (!region || region.end - region.start > SLICE_CAP_SECONDS * this.sr) {
      if (this.engine === 'slice') { this.stopSource(); this.engine = 'preview'; this.slice = null; }
      this.audio.currentTime = at / this.sr;
      if (wasPlaying && !region) this.audio.play().catch(() => {});
      else if (wasPlaying) this.audio.play().catch(() => {});
      return;
    }
    const id = ++this.pendingFetch;
    let buffer;
    try {
      const res = await fetch(`/api/slice?file=${encodeURIComponent(this.file)}&from=${region.start}&to=${region.end}`);
      if (!res.ok) throw new Error(`status ${res.status}`);
      this.ensureCtx();
      buffer = await this.ctx.decodeAudioData(await res.arrayBuffer());
    } catch (e) {
      if (id !== this.pendingFetch) return;
      this.onError?.('could not load the region for looping; using the preview');
      this.engine = 'preview';
      return;
    }
    if (id !== this.pendingFetch) return; // a newer region superseded this one
    // Keep the old slice playing until the new one is ready, then switch
    // without a gap of silence.
    const playing = this.playing;
    this.stopSource();
    this.audio.pause();
    this.slice = { buffer, start: region.start, end: region.end };
    this.engine = 'slice';
    if (playing) {
      const off = at >= region.start && at < region.end ? at - region.start : 0;
      this.startSource(off);
      this.playing = true;
      this.tick();
    }
  }

  ensureCtx() {
    if (!this.ctx) this.ctx = new (window.AudioContext || window.webkitAudioContext)({ sampleRate: this.sr });
  }

  startSource(offsetFrames) {
    this.ensureCtx();
    const src = this.ctx.createBufferSource();
    src.buffer = this.slice.buffer;
    src.loop = true;
    src.connect(this.ctx.destination);
    src.start(0, offsetFrames / this.sr);
    this.src = src;
    this.sliceStartedAt = this.ctx.currentTime;
    this.sliceOffset = offsetFrames;
  }

  stopSource() {
    if (this.src) { try { this.src.stop(); } catch {} this.src.disconnect(); this.src = null; }
  }

  tick() {
    cancelAnimationFrame(this.raf);
    const step = () => {
      if (!this.playing) return;
      if (this.engine === 'preview' && this.loop) {
        const p = this.position();
        if (p >= this.loop.end || p < this.loop.start) this.audio.currentTime = this.loop.start / this.sr;
      }
      this.onTick?.(this.position());
      this.raf = requestAnimationFrame(step);
    };
    this.raf = requestAnimationFrame(step);
  }

  destroy() {
    this.pause();
    this.audio.src = '';
    this.ctx?.close?.();
  }
}
```

- [ ] **Step 2: Sanity-check it parses and commit**

Run: `node --check web/static/lib/wave/clock.js`
Expected: no output.

```bash
git add web/static/lib/wave/clock.js
git commit -m "Playback clock for the waveform page: preview and slice engines behind one position()"
```

---

### Task 11: `view.js` — canvas drawing and gestures

**Files:**
- Create: `web/static/lib/wave/view.js`

**Interfaces:**
- Consumes: `geometry.js` (all), `TileCache.columns`.
- Produces `class WaveView` constructed with `{ canvas, tiles, totalFrames, sampleRate, getState, emit }` where
  - `getState()` returns `{ region, flags, grid: {bpm, sampleRate, downbeat}, cursor, selectedFlag }` (the page owns state; the view reads it each draw).
  - `emit(event, payload)` is called with: `'seek' {frame}`, `'addFlag' {frame}`, `'selectFlag' {flag}`, `'regionChange' {region, final}`, `'downbeatChange' {frame, final}`, `'viewChange' {view}`.
  - Methods: `draw()` (schedules a rAF, coalesced), `zoomTo(fpp, aroundX)`, `panTo(startFrame)`, `fitAll()`, `centerOn(frame)`, `view` (the current `{start, fpp, width}`), `destroy()`.
  - Gestures via Pointer Events with `touch-action: none` on the canvas: one pointer drag on empty waveform pans; two pointers pinch-zoom about their midpoint; a tap (< 8px movement, < 300ms) seeks; two taps within 300ms and 20px add a flag; a tap within 12px of a flag tick or on its chip selects it; pointer down within 24px of a region handle drags that edge; pointer down inside the region (not near a handle) drags the whole region; pointer down within 24px of the downbeat marker drags it. Wheel: `ctrl`/pinch-wheel zooms, plain wheel pans.

- [ ] **Step 1: Write `view.js`**

```js
// web/static/lib/wave/view.js
import { frameToX, xToFrame, gridLines, clampRegion } from './geometry.js';

const HANDLE_HIT = 24;   // CSS px each side of a handle
const FLAG_HIT = 12;
const TAP_MOVE = 8;
const TAP_MS = 300;
const MIN_FPP = 1 / 8;   // 8 px per frame: far enough
const CHIP_H = 16;

export class WaveView {
  constructor({ canvas, tiles, totalFrames, sampleRate, getState, emit }) {
    this.canvas = canvas;
    this.ctx = canvas.getContext('2d');
    this.tiles = tiles;
    this.total = totalFrames;
    this.sr = sampleRate;
    this.getState = getState;
    this.emit = emit;
    this.view = { start: 0, fpp: 1, width: 1 };
    this.raf = 0;
    this.pointers = new Map();
    this.gesture = null;      // { kind, ...}
    this.lastTap = null;
    this.minLen = Math.floor(sampleRate * 3 / 1000) * 2 + 1;
    this.chipRects = [];      // [{flag, x, y, w, h}] from the last draw
    this.resize = this.resize.bind(this);
    this.ro = new ResizeObserver(this.resize);
    this.ro.observe(canvas);
    canvas.style.touchAction = 'none';
    canvas.addEventListener('pointerdown', (e) => this.down(e));
    canvas.addEventListener('pointermove', (e) => this.move(e));
    canvas.addEventListener('pointerup', (e) => this.up(e));
    canvas.addEventListener('pointercancel', (e) => this.up(e));
    canvas.addEventListener('wheel', (e) => this.wheel(e), { passive: false });
    this.resize();
  }

  destroy() { this.ro.disconnect(); cancelAnimationFrame(this.raf); }

  resize() {
    const r = this.canvas.getBoundingClientRect();
    const dpr = window.devicePixelRatio || 1;
    this.dpr = dpr;
    this.canvas.width = Math.round(r.width * dpr);
    this.canvas.height = Math.round(r.height * dpr);
    this.cssW = r.width; this.cssH = r.height;
    const fitting = this.view.width === 1;
    this.view.width = r.width;
    if (fitting) this.fitAll(); else this.clampView();
    this.draw();
  }

  maxFpp() { return Math.max(MIN_FPP, this.total / this.view.width); }
  fitAll() { this.view.fpp = this.maxFpp(); this.view.start = 0; this.changed(); }
  centerOn(frame) { this.view.start = frame - (this.view.width * this.view.fpp) / 2; this.clampView(); this.changed(); }
  panTo(start) { this.view.start = start; this.clampView(); this.changed(); }
  zoomTo(fpp, aroundX) {
    const f = xToFrame(aroundX, this.view);
    this.view.fpp = Math.min(this.maxFpp(), Math.max(MIN_FPP, fpp));
    this.view.start = f - aroundX * this.view.fpp;
    this.clampView(); this.changed();
  }
  clampView() {
    const span = this.view.width * this.view.fpp;
    this.view.start = Math.max(0, Math.min(this.total - span, this.view.start));
    if (span >= this.total) this.view.start = 0;
  }
  changed() { this.emit('viewChange', { view: { ...this.view } }); this.draw(); }

  draw() {
    if (this.raf) return;
    this.raf = requestAnimationFrame(() => { this.raf = 0; this.paint(); });
  }

  paint() {
    const { ctx, view, dpr } = this;
    const W = this.canvas.width, H = this.canvas.height;
    const st = this.getState();
    const css = getComputedStyle(this.canvas);
    const col = (name, fb) => css.getPropertyValue(name).trim() || fb;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, this.cssW, this.cssH);
    ctx.fillStyle = col('--panel', '#131c2e');
    ctx.fillRect(0, 0, this.cssW, this.cssH);

    // Grid
    for (const g of gridLines(view, st.grid)) {
      const x = frameToX(g.frame, view);
      ctx.fillStyle = g.bar ? col('--line', '#26324a') : 'rgba(255,255,255,0.06)';
      ctx.fillRect(Math.round(x), 0, g.bar ? 1 : 1, this.cssH);
    }

    // Waveform: channels stacked
    const { cols, channels } = this.tiles.columns(view, dpr);
    const laneH = (this.cssH - CHIP_H) / channels;
    ctx.fillStyle = col('--accent', '#34d399');
    for (let x = 0; x < Math.ceil(view.width); x++) {
      for (let c = 0; c < channels; c++) {
        const mn = cols[(x * channels + c) * 2], mx = cols[(x * channels + c) * 2 + 1];
        if (!(mx >= mn)) continue;
        const mid = CHIP_H + laneH * c + laneH / 2;
        const y0 = mid - mx * (laneH / 2) * 0.95, y1 = mid - mn * (laneH / 2) * 0.95;
        ctx.fillRect(x, y0, 1, Math.max(1, y1 - y0));
      }
    }

    // Region
    if (st.region) {
      const x0 = frameToX(st.region.start, view), x1 = frameToX(st.region.end, view);
      ctx.fillStyle = 'rgba(52,211,153,0.14)';
      ctx.fillRect(x0, CHIP_H, x1 - x0, this.cssH - CHIP_H);
      ctx.fillStyle = col('--accent', '#34d399');
      for (const x of [x0, x1]) {
        ctx.fillRect(Math.round(x) - 1, CHIP_H, 2, this.cssH - CHIP_H);
        ctx.fillRect(Math.round(x) - 8, 0, 16, CHIP_H - 2); // grip tab
      }
    }

    // Downbeat marker
    if (st.grid.bpm) {
      const x = frameToX(st.grid.downbeat, view);
      ctx.fillStyle = col('--warn', '#fbbf24');
      ctx.fillRect(Math.round(x) - 1, 0, 2, this.cssH);
      ctx.beginPath(); ctx.moveTo(x - 6, 0); ctx.lineTo(x + 6, 0); ctx.lineTo(x, 8); ctx.fill();
    }

    // Flags with chips (a chip is hidden if it would overlap the previous one)
    this.chipRects = [];
    let lastChipRight = -Infinity;
    ctx.font = '11px ' + col('--font', 'system-ui');
    for (const f of st.flags) {
      const x = frameToX(f.frame, view);
      if (x < -200 || x > this.cssW + 200) continue;
      const sel = st.selectedFlag && st.selectedFlag.frame === f.frame;
      ctx.fillStyle = col('--flag', '#ffb020');
      ctx.fillRect(Math.round(x) - (sel ? 2 : 1), CHIP_H, sel ? 4 : 2, this.cssH - CHIP_H);
      if (f.label && x + 4 > lastChipRight) {
        const w = Math.min(160, ctx.measureText(f.label).width + 10);
        ctx.fillRect(x + 4, 1, w, CHIP_H - 3);
        ctx.fillStyle = '#000';
        ctx.fillText(f.label, x + 9, CHIP_H - 5, w - 10);
        this.chipRects.push({ flag: f, x: x + 4, y: 0, w, h: CHIP_H });
        lastChipRight = x + 4 + w + 4;
      }
    }

    // Cursor
    const cx = frameToX(st.cursor, view);
    ctx.fillStyle = col('--ink', '#eef2f8');
    ctx.fillRect(Math.round(cx), 0, 1, this.cssH);
  }

  // --- hit testing -------------------------------------------------------
  hit(x, y) {
    const st = this.getState();
    for (const r of this.chipRects) if (x >= r.x && x <= r.x + r.w && y <= r.h) return { kind: 'flag', flag: r.flag };
    if (st.region) {
      const x0 = frameToX(st.region.start, this.view), x1 = frameToX(st.region.end, this.view);
      if (Math.abs(x - x0) <= HANDLE_HIT) return { kind: 'handle', edge: 'start' };
      if (Math.abs(x - x1) <= HANDLE_HIT) return { kind: 'handle', edge: 'end' };
      if (x > x0 && x < x1) return { kind: 'region' };
    }
    if (st.grid.bpm && Math.abs(x - frameToX(st.grid.downbeat, this.view)) <= HANDLE_HIT && y < 24) return { kind: 'downbeat' };
    for (const f of st.flags) if (Math.abs(x - frameToX(f.frame, this.view)) <= FLAG_HIT) return { kind: 'flag', flag: f };
    return { kind: 'wave' };
  }

  // --- pointer events ----------------------------------------------------
  pt(e) { const r = this.canvas.getBoundingClientRect(); return { x: e.clientX - r.left, y: e.clientY - r.top }; }

  down(e) {
    this.canvas.setPointerCapture(e.pointerId);
    const p = this.pt(e);
    this.pointers.set(e.pointerId, p);
    if (this.pointers.size === 2) {
      const [a, b] = [...this.pointers.values()];
      this.gesture = { kind: 'pinch', dist: Math.abs(a.x - b.x) || 1, mid: (a.x + b.x) / 2, fpp: this.view.fpp, start: this.view.start };
      return;
    }
    const st = this.getState();
    const h = this.hit(p.x, p.y);
    const base = { x0: p.x, y0: p.y, t0: performance.now(), moved: false, start: this.view.start };
    switch (h.kind) {
      case 'handle': this.gesture = { ...base, kind: 'handle', edge: h.edge, region: { ...st.region } }; break;
      case 'region': this.gesture = { ...base, kind: 'moveRegion', region: { ...st.region } }; break;
      case 'downbeat': this.gesture = { ...base, kind: 'downbeat' }; break;
      case 'flag': this.gesture = { ...base, kind: 'flag', flag: h.flag }; break;
      default: this.gesture = { ...base, kind: 'pan' };
    }
  }

  move(e) {
    if (!this.pointers.has(e.pointerId)) return;
    const p = this.pt(e);
    this.pointers.set(e.pointerId, p);
    const g = this.gesture;
    if (!g) return;
    if (g.kind === 'pinch' && this.pointers.size === 2) {
      const [a, b] = [...this.pointers.values()];
      const dist = Math.abs(a.x - b.x) || 1;
      const mid = (a.x + b.x) / 2;
      const fpp = Math.min(this.maxFpp(), Math.max(MIN_FPP, g.fpp * (g.dist / dist)));
      const anchor = g.start + g.mid * g.fpp; // frame under the original midpoint
      this.view.fpp = fpp;
      this.view.start = anchor - mid * fpp;
      this.clampView(); this.changed();
      return;
    }
    const dx = p.x - g.x0;
    if (Math.abs(dx) > TAP_MOVE || Math.abs(p.y - g.y0) > TAP_MOVE) g.moved = true;
    const st = this.getState();
    switch (g.kind) {
      case 'pan':
        this.view.start = g.start - dx * this.view.fpp; this.clampView(); this.changed(); break;
      case 'handle': {
        const f = xToFrame(p.x, this.view);
        const r = { ...g.region, [g.edge]: f };
        if (g.edge === 'start') r.start = Math.min(r.start, r.end - this.minLen);
        else r.end = Math.max(r.end, r.start + this.minLen);
        this.emit('regionChange', { region: clampRegion(r, this.total, this.minLen), final: false });
        this.draw(); break;
      }
      case 'moveRegion': {
        const d = Math.round(dx * this.view.fpp);
        const len = g.region.end - g.region.start;
        let start = Math.max(0, Math.min(this.total - len, g.region.start + d));
        this.emit('regionChange', { region: { start, end: start + len }, final: false });
        this.draw(); break;
      }
      case 'downbeat':
        this.emit('downbeatChange', { frame: Math.max(0, Math.min(this.total - 1, xToFrame(p.x, this.view))), final: false });
        this.draw(); break;
      default: break;
    }
  }

  up(e) {
    const p = this.pt(e);
    this.pointers.delete(e.pointerId);
    const g = this.gesture;
    if (!g) return;
    if (g.kind === 'pinch') { if (this.pointers.size < 2) this.gesture = null; return; }
    this.gesture = null;
    const st = this.getState();
    const isTap = !g.moved && performance.now() - g.t0 < TAP_MS;
    switch (g.kind) {
      case 'handle':
      case 'moveRegion':
        if (g.moved) this.emit('regionChange', { region: st.region, final: true });
        else if (g.kind === 'moveRegion') this.emit('seek', { frame: xToFrame(p.x, this.view) });
        break;
      case 'downbeat':
        if (g.moved) this.emit('downbeatChange', { frame: st.grid.downbeat, final: true });
        break;
      case 'flag':
        if (isTap) this.emit('selectFlag', { flag: g.flag });
        break;
      case 'pan':
        if (isTap) {
          const now = performance.now();
          if (this.lastTap && now - this.lastTap.t < TAP_MS && Math.abs(p.x - this.lastTap.x) < 20) {
            this.lastTap = null;
            this.emit('addFlag', { frame: xToFrame(p.x, this.view) });
          } else {
            this.lastTap = { t: now, x: p.x };
            this.emit('seek', { frame: xToFrame(p.x, this.view) });
          }
        }
        break;
    }
  }

  wheel(e) {
    e.preventDefault();
    const p = this.pt(e);
    if (e.ctrlKey || e.metaKey) this.zoomTo(this.view.fpp * Math.exp(e.deltaY * 0.01), p.x);
    else this.panTo(this.view.start + (e.deltaX || e.deltaY) * this.view.fpp);
  }
}
```

- [ ] **Step 2: Parse-check and commit**

Run: `node --check web/static/lib/wave/view.js`

```bash
git add web/static/lib/wave/view.js
git commit -m "Canvas waveform view with pan, pinch, region handles and flag hit-testing"
```

---

### Task 12: The page — `wave.html`, `page.js`, styles, service worker, list link

**Files:**
- Create: `web/static/wave.html`, `web/static/lib/wave/page.js`
- Modify: `web/static/styles.css`, `web/static/sw.js`, `web/static/lib/takes.js` (row markup ~line 132 and `dlEl` wiring ~line 336)

**Interfaces:**
- Consumes: `TileCache`, `WaveView`, `Clock`, `geometry.js` (`barBeat`, `fmtTime`, `framesPerBeat`, `clampRegion`), `GET /api/jams`, `GET /api/peaks?file=`, `PATCH /api/take`, `POST /api/cut`.
- Produces: the page at `/wave.html?file=<take>`, and an **Open** link in each take row.

- [ ] **Step 1: `wave.html`**

```html
<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<title>Hindsight — take</title>
<meta name="theme-color" content="#0b1120">
<meta name="color-scheme" content="dark">
<link rel="manifest" href="/manifest.json">
<link rel="icon" href="/icons/favicon-32.png" sizes="32x32">
<link rel="apple-touch-icon" href="/icons/apple-touch-icon.png">
<meta name="apple-mobile-web-app-capable" content="yes">
<meta name="mobile-web-app-capable" content="yes">
<link rel="stylesheet" href="/styles.css">
</head>
<body class="wave-page">

<header class="topbar">
  <a class="back" href="/" aria-label="Back to takes">‹</a>
  <div class="wave-title">
    <span id="wave-name" class="wave-name">…</span>
    <span id="wave-bpm" class="wave-bpm"></span>
  </div>
</header>

<main class="wave-main">
  <div id="wave-error" class="wave-error" hidden></div>
  <canvas id="wave-canvas" class="wave-canvas" aria-label="Waveform"></canvas>

  <div class="wave-row transport">
    <button id="play" class="icon-btn" type="button">Play</button>
    <button id="loop" class="icon-btn" type="button" aria-pressed="false">Loop region</button>
    <span class="readout"><span id="pos-bar" class="mono"></span> <span id="pos-time" class="mono"></span></span>
  </div>

  <div class="wave-row region">
    <button id="region" class="icon-btn" type="button">Region</button>
    <span class="edge">
      <button id="start-dec" class="nudge" type="button" aria-label="Region start earlier">−</button>
      <span id="region-start" class="mono">—</span>
      <button id="start-inc" class="nudge" type="button" aria-label="Region start later">+</button>
    </span>
    <span class="edge">
      <button id="end-dec" class="nudge" type="button" aria-label="Region end earlier">−</button>
      <span id="region-end" class="mono">—</span>
      <button id="end-inc" class="nudge" type="button" aria-label="Region end later">+</button>
    </span>
    <button id="export" class="icon-btn primary" type="button" disabled>Export</button>
  </div>

  <div class="wave-row zoom">
    <button id="zoom-out" class="nudge" type="button" aria-label="Zoom out">−</button>
    <button id="zoom-fit" class="nudge" type="button">Fit</button>
    <button id="zoom-in" class="nudge" type="button" aria-label="Zoom in">+</button>
  </div>
</main>

<div id="flag-sheet" class="sheet" hidden>
  <input id="flag-label" type="text" maxlength="120" placeholder="name this moment">
  <button id="flag-delete" class="icon-btn danger" type="button">Delete</button>
  <button id="flag-done" class="icon-btn" type="button">Done</button>
</div>

<div id="toasts" aria-live="polite"></div>
<script type="module" src="/lib/wave/page.js"></script>
</body>
</html>
```

- [ ] **Step 2: `page.js`**

```js
// web/static/lib/wave/page.js
import { TileCache } from './tiles.js';
import { WaveView } from './view.js';
import { Clock } from './clock.js';
import { barBeat, fmtTime, framesPerBeat, clampRegion } from './geometry.js';

const $ = (id) => document.getElementById(id);
const file = new URLSearchParams(location.search).get('file');

function toast(msg, kind = 'ok', ms = 4000) {
  const t = document.createElement('div');
  t.className = `toast ${kind}`;
  t.textContent = msg;
  $('toasts').appendChild(t);
  setTimeout(() => t.remove(), ms);
}

function fail(msg) {
  $('wave-error').textContent = msg;
  $('wave-error').hidden = false;
  $('wave-canvas').hidden = true;
}

async function main() {
  if (!file) return fail('No take given.');
  const [jamsRes, peaksRes] = await Promise.all([
    fetch('/api/jams'),
    fetch(`/api/peaks?file=${encodeURIComponent(file)}`),
  ]);
  if (!jamsRes.ok) return fail('Could not load takes.');
  const take = (await jamsRes.json()).find((t) => t.name === file);
  if (!take) return fail('That take is gone.');
  if (!peaksRes.ok) return fail('This take has no waveform yet. Try again in a moment.');
  const filePeaks = await peaksRes.json();

  const sr = take.sample_rate || 48000;
  const total = Math.round(take.duration_seconds * sr);
  const minLen = Math.floor(sr * 3 / 1000) * 2 + 1;

  // --- state (the page owns it; the view reads it each draw) -------------
  const state = {
    region: take.trim ? { start: take.trim.start_frame, end: take.trim.end_frame } : null,
    flags: (take.flags || []).map((f) => ({ frame: f.frame, label: f.label || '' })),
    grid: { bpm: take.bpm || null, sampleRate: sr, downbeat: take.downbeat_frame || 0 },
    cursor: 0,
    selectedFlag: null,
  };

  $('wave-name').textContent = take.label || file.replace(/\.wav$/, '');
  $('wave-bpm').textContent = take.bpm ? `${take.bpm} BPM` : '';

  // --- pieces --------------------------------------------------------------
  const canvas = $('wave-canvas');
  const tiles = new TileCache({ file, totalFrames: total, filePeaks, onChange: () => view.draw() });
  const view = new WaveView({ canvas, tiles, totalFrames: total, sampleRate: sr, getState: () => state, emit });
  const clock = new Clock({
    previewUrl: `/api/download?file=${encodeURIComponent(take.preview_name)}`,
    sampleRate: sr, file,
    onTick: (frame) => { state.cursor = frame; updateReadout(); view.draw(); },
    onError: (m) => toast(m, 'bad'),
  });

  // --- sidecar patches ----------------------------------------------------
  async function patch(body) {
    const res = await fetch(`/api/take?file=${encodeURIComponent(file)}`, {
      method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
    });
    if (!res.ok) {
      const b = await res.json().catch(() => ({}));
      throw new Error(b.error || `status ${res.status}`);
    }
    return res.json();
  }
  let regionTimer = 0;
  function saveRegion() {
    clearTimeout(regionTimer);
    regionTimer = setTimeout(() => {
      patch({ trim: state.region ? { start_frame: state.region.start, end_frame: state.region.end } : null })
        .catch((e) => toast(`Could not save region: ${e.message}`, 'bad'));
    }, 300);
  }
  function saveDownbeat() {
    patch({ downbeat_frame: state.grid.downbeat }).catch((e) => toast(`Could not save downbeat: ${e.message}`, 'bad'));
  }
  function saveFlags() {
    patch({ flags: state.flags }).then((b) => {
      if (b.cue_error) toast(b.cue_error, 'bad');
    }).catch((e) => toast(`Could not save flags: ${e.message}`, 'bad'));
  }

  // --- view events --------------------------------------------------------
  function emit(ev, p) {
    switch (ev) {
      case 'seek': clock.seek(p.frame); state.cursor = p.frame; updateReadout(); view.draw(); break;
      case 'addFlag':
        if (state.flags.some((f) => f.frame === p.frame)) break;
        state.flags.push({ frame: p.frame, label: '' });
        state.flags.sort((a, b) => a.frame - b.frame);
        saveFlags(); view.draw(); break;
      case 'selectFlag': openSheet(p.flag); break;
      case 'regionChange':
        state.region = p.region; updateRegionRow(); view.draw();
        if (p.final) { saveRegion(); if (loopOn) clock.setLoop(state.region); }
        break;
      case 'downbeatChange':
        state.grid.downbeat = p.frame; view.draw();
        if (p.final) saveDownbeat();
        break;
      case 'viewChange': break;
    }
  }

  // --- flag sheet ---------------------------------------------------------
  const sheet = $('flag-sheet');
  function openSheet(flag) {
    state.selectedFlag = flag;
    $('flag-label').value = flag.label;
    sheet.hidden = false;
    $('flag-label').focus();
    view.draw();
  }
  function closeSheet(commit) {
    const f = state.selectedFlag;
    if (!f) return;
    if (commit) {
      const label = $('flag-label').value.trim();
      if (label !== f.label) { f.label = label; saveFlags(); }
    }
    state.selectedFlag = null;
    sheet.hidden = true;
    view.draw();
  }
  $('flag-done').addEventListener('click', () => closeSheet(true));
  $('flag-label').addEventListener('keydown', (e) => {
    if (e.key === 'Enter') closeSheet(true);
    if (e.key === 'Escape') closeSheet(false);
  });
  $('flag-delete').addEventListener('click', () => {
    const f = state.selectedFlag;
    state.flags = state.flags.filter((x) => x.frame !== f.frame);
    state.selectedFlag = null;
    sheet.hidden = true;
    saveFlags(); view.draw();
  });

  // --- transport ----------------------------------------------------------
  let loopOn = false;
  $('play').addEventListener('click', async () => {
    if (clock.playing) { clock.pause(); $('play').textContent = 'Play'; }
    else { await clock.play(); $('play').textContent = 'Pause'; }
  });
  $('loop').addEventListener('click', async () => {
    if (!state.region) { toast('Set a region first'); return; }
    loopOn = !loopOn;
    $('loop').setAttribute('aria-pressed', String(loopOn));
    await clock.setLoop(loopOn ? state.region : null);
    if (loopOn && !clock.playing) { await clock.play(); $('play').textContent = 'Pause'; }
  });

  // --- region row ---------------------------------------------------------
  const nudgeFrames = () => (state.grid.bpm ? Math.round(framesPerBeat(state.grid)) : Math.round(sr * 0.01));
  function setRegion(r, final = true) { emit('regionChange', { region: clampRegion(r, total, minLen), final }); }
  $('region').addEventListener('click', () => {
    if (state.region) { state.region = null; updateRegionRow(); saveRegion(); if (loopOn) { loopOn = false; $('loop').setAttribute('aria-pressed', 'false'); clock.setLoop(null); } view.draw(); return; }
    const half = state.grid.bpm ? Math.round(framesPerBeat(state.grid) * 2) : Math.round(view.view.width * view.view.fpp / 2);
    setRegion({ start: state.cursor - half, end: state.cursor + half });
  });
  const nudge = (edge, sign) => () => {
    if (!state.region) return;
    const r = { ...state.region };
    r[edge] += sign * nudgeFrames();
    if (edge === 'start') r.start = Math.min(r.start, r.end - minLen);
    else r.end = Math.max(r.end, r.start + minLen);
    setRegion(r);
  };
  for (const [id, edge, sign] of [['start-dec', 'start', -1], ['start-inc', 'start', 1], ['end-dec', 'end', -1], ['end-inc', 'end', 1]]) {
    const b = $(id);
    let hold = 0, rep = 0;
    b.addEventListener('click', nudge(edge, sign));
    b.addEventListener('pointerdown', () => { hold = setTimeout(() => { rep = setInterval(nudge(edge, sign), 120); }, 500); });
    for (const ev of ['pointerup', 'pointercancel', 'pointerleave']) b.addEventListener(ev, () => { clearTimeout(hold); clearInterval(rep); });
  }
  function updateRegionRow() {
    const r = state.region;
    $('region').textContent = r ? 'Clear' : 'Region';
    $('export').disabled = !r;
    $('region-start').textContent = r ? fmtTime(r.start, sr) : '—';
    $('region-end').textContent = r ? fmtTime(r.end, sr) : '—';
    for (const id of ['start-dec', 'start-inc', 'end-dec', 'end-inc']) $(id).disabled = !r;
  }

  // --- export -------------------------------------------------------------
  $('export').addEventListener('click', async () => {
    if (!state.region) return;
    const btn = $('export');
    btn.disabled = true;
    try {
      const res = await fetch(`/api/cut?file=${encodeURIComponent(file)}`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ start_frame: state.region.start, end_frame: state.region.end, label: '' }),
      });
      const body = await res.json().catch(() => ({}));
      if (!res.ok) throw new Error(body.error || `status ${res.status}`);
      const t = document.createElement('div');
      t.className = 'toast ok';
      t.innerHTML = `Saved as <a href="/wave.html?file=${encodeURIComponent(body.name)}">${body.name}</a>`;
      $('toasts').appendChild(t);
      setTimeout(() => t.remove(), 8000);
    } catch (e) {
      toast(`Export failed: ${e.message}`, 'bad');
    } finally {
      btn.disabled = !state.region;
    }
  });

  // --- zoom buttons & keyboard -------------------------------------------
  $('zoom-in').addEventListener('click', () => view.zoomTo(view.view.fpp / 2, view.view.width / 2));
  $('zoom-out').addEventListener('click', () => view.zoomTo(view.view.fpp * 2, view.view.width / 2));
  $('zoom-fit').addEventListener('click', () => view.fitAll());
  document.addEventListener('keydown', (e) => {
    if (e.target.tagName === 'INPUT') return;
    switch (e.key) {
      case ' ': e.preventDefault(); $('play').click(); break;
      case 'l': case 'L': $('loop').click(); break;
      case 'f': case 'F': emit('addFlag', { frame: state.cursor }); break;
      case '[': setRegion({ start: state.cursor, end: state.region ? Math.max(state.region.end, state.cursor + minLen) : state.cursor + nudgeFrames() * 4 }); break;
      case ']': if (state.region) setRegion({ start: Math.min(state.region.start, state.cursor - minLen), end: state.cursor }); break;
      case '+': case '=': $('zoom-in').click(); break;
      case '-': $('zoom-out').click(); break;
      case 'ArrowLeft': view.panTo(view.view.start - view.view.width * view.view.fpp * 0.2); break;
      case 'ArrowRight': view.panTo(view.view.start + view.view.width * view.view.fpp * 0.2); break;
    }
  });

  // --- readout ------------------------------------------------------------
  function updateReadout() {
    $('pos-bar').textContent = barBeat(state.cursor, state.grid);
    $('pos-time').textContent = fmtTime(state.cursor, sr);
  }

  updateRegionRow();
  updateReadout();
  view.fitAll();
  window.addEventListener('pagehide', () => { clock.destroy(); tiles.stop(); view.destroy(); });
}

main().catch((e) => fail(e.message || String(e)));
```

- [ ] **Step 3: Styles**

Append to `web/static/styles.css`:

```css
/* ---------- waveform page ---------- */
.wave-page .topbar { display: flex; align-items: center; gap: 10px; }
.wave-page .back { font-size: 28px; line-height: 1; padding: 0 10px; color: var(--ink); text-decoration: none; }
.wave-title { display: flex; flex-direction: column; min-width: 0; }
.wave-name { font-weight: 600; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.wave-bpm { font-size: 12px; color: var(--ink-dim); }
.wave-main { padding: 8px; display: flex; flex-direction: column; gap: 8px; }
.wave-canvas {
  width: 100%; height: 40vh; min-height: 180px;
  border-radius: var(--radius); background: var(--panel);
  touch-action: none; display: block;
}
.wave-error { padding: 24px; color: var(--danger); }
.wave-row { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
.wave-row .readout { margin-left: auto; color: var(--ink-dim); font-size: 13px; }
.wave-row .edge { display: inline-flex; align-items: center; gap: 4px; }
.mono { font-family: var(--mono); }
.nudge {
  appearance: none; min-width: var(--tap); min-height: 40px;
  border: 1px solid var(--line); border-radius: 9px;
  background: var(--panel); color: var(--ink); font-size: 16px; cursor: pointer;
}
.nudge:disabled { opacity: 0.4; }
.icon-btn.primary { background: var(--accent-dk); color: var(--ink); border-color: var(--accent-dk); }
.icon-btn[aria-pressed="true"] { background: var(--accent); color: #000; }
.sheet {
  position: fixed; left: 0; right: 0; bottom: 0;
  padding: 12px calc(12px + env(safe-area-inset-right)) calc(12px + env(safe-area-inset-bottom)) calc(12px + env(safe-area-inset-left));
  background: var(--panel-2); border-top: 1px solid var(--line);
  display: flex; gap: 8px; align-items: center; z-index: 50;
}
.sheet input {
  flex: 1; min-height: 40px; padding: 0 10px;
  border: 1px solid var(--flag); border-radius: 9px;
  background: var(--panel); color: var(--ink); font-family: var(--font); font-size: 15px;
}
@media (min-width: 900px) {
  .wave-canvas { height: 60vh; }
}
```

Check `#toasts` and `.toast` already apply on this page (they are global in `styles.css`; the `#toasts` element is present in `wave.html`).

- [ ] **Step 4: Service worker and list link**

In `web/static/sw.js`: change `CACHE` to `'hindsight-shell-v3'` and add to `SHELL`:

```js
  '/wave.html',
  '/lib/wave/geometry.js',
  '/lib/wave/tiles.js',
  '/lib/wave/view.js',
  '/lib/wave/clock.js',
  '/lib/wave/page.js',
```

In `web/static/lib/takes.js` `createRow` markup, add before the WAV link:

```html
        <a class="icon-btn open">Open</a>
```

and in the row object `openEl: el.querySelector('.open')`, and where `row.dlEl.href` is set:

```js
    row.openEl.href = `/wave.html?file=${encodeURIComponent(t.name)}`;
```

- [ ] **Step 5: Run it and check by hand**

```bash
RING_SECONDS=120 CGO_ENABLED=0 PORT=5099 OUTPUT_DIR=$SCRATCH/jams go run ./cmd/hindsight --demo &
sleep 6; curl -s -X POST 'localhost:5099/api/trigger?seconds=20'
```

Open `http://localhost:5099/` and tap **Open** on the take. Then, on a phone-sized viewport (Chrome devtools device mode, touch emulation on):

1. Waveform fills the canvas; pinch in until beats and bars appear; pan; tap seeks; **Play** plays from the cursor and the cursor moves.
2. Double-tap adds a flag; tap it, name it, Done; reload: name survives; the take list shows the chip.
3. **Region** creates a four-beat region (demo take has 96 BPM); drag each handle; drag the region body; nudge buttons move by a beat; reload: region survives.
4. **Loop region** loops sample-exactly with no click at the seam; toggling off returns to the preview at the same place.
5. **Export** toasts a link; the new take appears in the list with the right length; open it and its waveform matches the region; its sidecar has `source`.
6. Drag the downbeat marker; reload: it persists; bar.beat readout changes accordingly.
7. Kill the server, reload: the page shell loads from the service worker (peaks fetch fails with the "no waveform yet" message; that is expected offline).
8. In devtools, throttle to offline mid-zoom: the coarse waveform stays drawn; restore: tiles refine.

Fix anything that fails, then:

- [ ] **Step 6: Commit**

```bash
node --test web/static/lib/wave/
git add web/static/wave.html web/static/lib/wave/page.js web/static/styles.css web/static/sw.js web/static/lib/takes.js
git commit -m "Waveform page: zoom, flags, one region, loop audition, export as a cut"
```

---

### Task 13: Docs and STATE

**Files:**
- Modify: `README.md` (feature list), `docs/architecture.md` (route/module map), `docs/api.md` (already updated per task; check the route table lists all three), `docs/development.md` (node --test)

- [ ] **Step 1: README and architecture**

Add to the README feature list one line: "**Waveform page** — open any take to zoom, scrub, flag, set a region, loop it, and export the region as a new take with declick fades." In `docs/architecture.md`'s file map add `web/static/lib/wave/` with the five modules and the three endpoints. In `docs/development.md`, after the Go test instructions, add:

```markdown
The waveform page's pure geometry has JavaScript tests under node's built-in
runner — no package.json, no dependencies:

    node --test web/static/lib/wave/
```

- [ ] **Step 2: Commit**

```bash
git add README.md docs/architecture.md docs/development.md docs/api.md
git commit -m "Document the waveform page and its endpoints"
```

- [ ] **Step 3: Update STATE.md** (untracked, gitignored) with: branch, what shipped, what was verified by hand, anything left.

---

## Self-review

**Spec coverage.** §1 range peaks → Tasks 2–3. §2 cut → Tasks 4–5 (naming, fades, streaming, sidecar fields, disk guard, partial-file cleanup, name collision added). §3 slice → Task 6 (16-bit, 60s cap, Content-Length). §4 page → Tasks 8–12 (layout, gestures table, region creation rule, persistence to `trim`, downbeat field, export flow, keyboard). §5 tiles → Tasks 8–9 (ladder, 1024 buckets, margin, 256 cap, LRU, fallback, backoff 500ms→8s, 404 gone). Grid thresholds 8px/4px → Task 8. §6 clock → Task 10 (two engines, one `position()`, refetch without gap, fallback on decode failure). §7 errors → Tasks 3/5/6 server; 9/10/12 client. Testing section → Go tests in 1–7, `node --test` in 8–9 with CI in 8, manual checklist in 12. Out of scope untouched.

**Gaps found and fixed inline:** name collision on two cuts in one second (added to Task 5); `Take` listing needed `downbeat_frame` and `source` for the page (added to Task 7).

**Type consistency.** `ReadFrames(path, from, to, blockFrames, fn) (WAVInfo, error)` used identically in Tasks 2, 4, 6. `PeakData.From` used by tiles.js as `pd.from`. `Cut(dir, CutRequest, now) (string, error)` in 4 and 5. `WriteSlice16(w, path, from, to)`, `SliceBytes(info, from, to)` in 6. `TileCache.columns(view, dpr)` → `{level, cols, channels}` consumed in view.js. `WaveView` emits the six events page.js switches on. `Clock.setLoop` is async and awaited in page.js.
