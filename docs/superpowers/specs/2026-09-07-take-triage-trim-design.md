# Take triage, trim, and batch export — design

**Date:** 2026-09-07 · **Status:** approved, not implemented · **Repo:** `audio-dashcam`

---

## Context

The dashcam captures reliably (see `STATE.md` Part 1), but a take's entire
identity is its filename: `jam_2026-09-07_143022.wav`. You capture the moment
the jam works, and a week later face a wall of identical timestamps.

The workflow is **staging**: the Pi is a holding pen, and keepers get pulled to
a DAW within days. That makes triage speed and export quality the priorities.
Search, tagging, and archival browsing are explicitly not.

Three asks, in the user's terms: judge takes faster, get them off the Pi faster,
and "chop up a sample".

## Goals

1. Give a take an identity — a name and a star — in one thumb tap.
2. Trim one region out of a take and export just that region, losslessly.
3. Export several takes in one action.

All of it **phone-first**: the user is standing at the instrument, on a ~390px
screen, immediately after playing.

## Non-goals

- Multi-region slicing, transient auto-detect, BPM/grid snapping — bento already
  does this properly; duplicating it here would be waste. One region per take.
- Search, tags, collections, folders — wrong for a staging workflow.
- Editing beyond trim: no gain, no fades beyond declick, no EQ.
- Any change to the capture path. Capture is done and measured; leave it alone.

---

## Architecture

Three additions, each small, each reusing code that already exists.

### 1. Sidecar metadata

`jam_<ts>.meta.json`, written beside the take:

```json
{
  "version": 1,
  "label": "the good one",
  "starred": true,
  "trim": { "start_frame": 480000, "end_frame": 998400 }
}
```

- The human-readable name is `label`, not `name`. `Take.Name` is already the
  take's filename and the row key in `takes.js`; one word cannot mean two
  things in the same object.
- Every field except `version` is optional. An **absent file means an untrimmed,
  unnamed take**, so all existing takes remain valid with zero migration.
- `trim` absent or `null` = whole take.
- Bounds are **frames, not seconds** — integers, sample-exact, no float drift,
  and directly comparable to bento's `Cues []uint64`.
- Writes are **atomic** (temp file + `rename`). The UI polls `/api/jams` every
  5s and must never observe a half-written sidecar.
- The sidecar is derived, disposable state. Deleting it loses a name and a trim,
  never audio.

### 2. Trim on export, never on disk

The takes are uncompressed 32-bit PCM, so a trim is a byte-range slice plus a
rewritten header. No ffmpeg, no re-encode, no temp file, no quality loss.

`ReadWAVInfo` already walks the chunk table and `writeWAVHeader` already
synthesizes headers. The only structural change is adding a `DataOffset int64`
field to `WAVInfo` and setting it when the walker reaches the `data` chunk.

Note the existing signature is
`writeWAVHeader(w *bufio.Writer, dataBytes uint32, channels, sampleRate int)` —
the export handler must wrap the `http.ResponseWriter` in a `bufio.Writer` and
flush it, and the `uint32` caps a single take at 4GB (irrelevant at these ring
lengths, but it is why the bound is not `int64`).

Algorithm:

```
info      := ReadWAVInfo(path)
blockAlign := info.Channels * info.BitsPerSample / 8
totalFrames := info.DataBytes / blockAlign

start := clamp(trim.start_frame, 0, totalFrames)
end   := clamp(trim.end_frame,   start, totalFrames)
out   := end - start                       // 400 if out <= 0

writeWAVHeader(w, out*blockAlign, info.Channels, info.SampleRate)
seek(info.DataOffset + start*blockAlign)
stream(out*blockAlign bytes, with declick)
```

**Declick is required.** A raw cut at an arbitrary sample clicks at both edges.
Apply a linear fade over `fadeFrames = min(144, out/2)` — 3ms at 48kHz — to the
leading and trailing frames; raw-copy everything between. This matches bento's
`Declick bool // apply 3 ms boundary fades`, which exists for the same reason.

The output length is known before writing, so set `Content-Length` exactly.

The source WAV is never modified. A bad trim costs nothing and is re-editable
forever.

### 3. Range peaks

Stored peaks are `peakBuckets = 1024` for the whole file regardless of duration
— 29ms per bucket on a 30s take, 117ms on a 120s take. Zooming into them would
stretch ~34 buckets across the screen.

So `/api/peaks` gains optional `from`/`to` frame parameters. The server reads
only that byte range and runs the existing `peakAccumulator` over it, returning
1024 buckets for the **window**. A ±1s window is a 768KB read on the Pi and
yields 5ms/px on a 390px phone.

With `from`/`to` absent, behaviour is unchanged: serve the stored
`.peaks.json`. No regression for the existing takes list.

---

## API surface

| Method | Route | Purpose |
|---|---|---|
| `GET` | `/api/jams` | unchanged route; each `Take` gains `name`, `starred`, `trim` |
| `PATCH` | `/api/take?file=X` | merge `{name?, starred?, trim?}` into the sidecar |
| `GET` | `/api/download?file=X&trim=1` | stream the trimmed region; without `trim=1`, current behaviour |
| `GET` | `/api/peaks?file=X&from=N&to=N` | range peaks; without `from`/`to`, current behaviour |
| `GET` | `/api/export?files=a,b,c&trim=1` | streaming zip of several takes |

`PATCH` merges rather than replaces: sending `{"starred":true}` must not clear a
name. Sending `{"trim":null}` explicitly clears the trim.

All filename parameters go through the existing `safeTakeName` /
`safeMediaPath` guards. No new path-handling code.

The zip is written with `archive/zip` straight to the response, each entry
produced by the same trim writer, so a trimmed take exports trimmed with no
special-casing. Entry names use the take's `name` when set, falling back to the
filename, sanitized: strip `" ? * < > : |` (bento's hardware rejects them) and
never emit `._*` or `.DS_Store`.

---

## UI

### Takes list (phase 1)

Each row gains an inline-editable name and a star toggle. Starred takes sort
first, then by date descending. A row with a trim shows its trimmed length.

### Trim editor (phase 2)

A full-height sheet from the take row, following the existing
`<dialog class="sheet">` pattern in `index.html`. Ordered so the most-used
controls sit lowest, within thumb reach:

| Element | Behaviour |
|---|---|
| Take name | tap to edit inline |
| Overview waveform | whole take from stored peaks; region shaded; two drag handles |
| Detail waveform | ±1s around the **selected** handle, from range peaks. The start handle is selected on open. |
| Readout | `start` · `length` |
| Transport | play region · loop toggle |
| Nudge | `◀ ▶` on the selected handle, 10ms per tap, hold to repeat |
| Actions | Cancel · Save · Download |

The two-waveform stack is the core of the design. Raw drag resolution is 77ms/px
on a 30s take and 308ms on a 120s take — too coarse to place a transient, and
the thumb covers the target. Dragging on the **overview** while watching the
**detail** view solves both: the finger is never over the point being placed.
Nudge closes the last few milliseconds.

Region audition reuses the existing mp3 preview with `currentTime` bounds — no
new data over the wire. The phone never loads the source WAV.

Bento's `RG-01` (exactly one active region) and its loop toggle map directly.

### Tablet

The app should work on a tablet as well as a phone. This is a breakpoint
exercise, not a second design — the layout is already a responsive single
column, so the risk is it stretching into a phone layout with a 900px-wide
button rather than failing outright.

- **< 600px** — current single column, unchanged.
- **>= 600px** — cap the content column and centre it; the Takes list may go
  two-up; the trim editor places the overview and detail waveforms side by side
  rather than stacked, since both fit.

Two things stay constant regardless of width: touch targets remain >= 44px (a
tablet is still touch), and the detail waveform is still required. Extra width
helps drag resolution but does not solve it — a 30s take is 77ms/px at 390px and
still 29ms/px at 1024px, which is far short of placing a transient. Nudge and
the detail view carry the precision at every size.

The waveform canvas already resizes via `ResizeObserver`, so no new mechanism is
needed — only breakpoints and a max-width.

### Multi-select (phase 3)

A **select-mode toggle** in the Takes header puts the list into selection; a bar
shows "N selected · Export". One request, one zip.

Deliberately not long-press: on mobile it collides with the browser's own text
selection and context menu, and it is undiscoverable.

---

## Error handling

| Case | Behaviour |
|---|---|
| Sidecar missing | Treat as defaults. Not an error. |
| Sidecar corrupt | Log, treat as defaults, do not delete. Audio is unaffected. |
| `end_frame <= start_frame` | `400`, message names the field. |
| Bounds beyond the file | Clamp silently to the file. A take re-recorded shorter must not 500. |
| Region shorter than 6ms | Clamp `fadeFrames` to `out/2`; do not refuse. |
| Trim requested, no sidecar | Serve the whole file. `trim=1` is a request, not an assertion. |
| Zip entry fails mid-stream | Headers are already sent; log and abort that entry. Do not attempt a `500`. |
| Disk full on `PATCH` | `507`, consistent with the existing capture guard. |

---

## Testing

**Unit**
- Frame→byte math across channel counts and bit depths, including odd frame counts.
- Header correctness: a trimmed stream parses back through `ReadWAVInfo` with
  the expected duration, channels, and rate.
- Fade envelope: first and last sample near zero, midpoint untouched, and the
  short-region clamp.
- Clamping: negative, zero-length, past-EOF, and reversed bounds.
- Sidecar round-trip; absent-file defaults; `PATCH` merge semantics including
  explicit `null` trim; atomic write leaves no partial file.
- Range peaks: bucket count, window bounds, and `from`/`to` absent falling back
  to stored peaks.

**Integration**
- Trimmed download is a valid WAV whose duration matches the requested region
  within one frame — verified with `ffprobe`.
- Zip contains the expected entries, each independently valid.

**Manual**
- The editor at 390px: handles reachable, detail view tracks the drag, no
  horizontal overflow.
- The editor at 768px and 1024px: no stretched controls, side-by-side waveforms,
  touch targets still >= 44px.
- Confirm a trimmed export opens cleanly in the DAW with no click at either edge.

---

## Phasing

**Phase 1 — identity.** Sidecar, inline rename, star toggle, starred-first
ordering. No new audio code. Independently shippable and the biggest usability
win per line of code.

**Phase 2 — trim.** `DataOffset`, the trim export handler with declick, range
peaks, the editor sheet.

**Phase 3 — egress.** Multi-select and streaming zip export.

---

## Future, explicitly out of scope

Recorded so the decisions above stay legible; none of it is being built now.

- **RIFF `cue ` chunk on export.** Because trim bounds are already frames, a
  `cue ` chunk is a small addition, and the 1010music Bento hardware reads cue
  points directly as slice markers. This is the bridge to bento.
- **Convergence with bento** (`~/projects/bento`) into one home-recording
  pipeline: `audio-dashcam` (capture) → `bento` (chop, convert, card layout) →
  Bento hardware / MPC. Bento already imports Akai `.xpm` programs and converts
  their slice points to cue markers, so it is already the MPC bridge; the
  dashcam is the missing capture front-end. The shared currency across all three
  is sample-offset frames plus RIFF cue chunks — which is why this design uses
  frames.
- **Tailscale for HTTPS.** Now the chosen approach (`STATE.md` Part 3), which
  replaced routing through the k3s cluster. `tailscale serve` yields a trusted
  cert on `*.ts.net` with no port-forward, no cert-manager and no DNS01,
  unlocking the Android PWA install. Not a dependency of this design.
- **Multi-region slice and transient detection** — bento's territory.
