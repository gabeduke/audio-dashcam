# Flags Design

**Date:** 2026-09-09
**Status:** awaiting owner review

## Goal

Mark a moment of interest — while jamming, before deciding to save, and
afterwards on a saved take — and have that mark survive into the WAV as a RIFF
cue point, so it arrives in bento, an MPC or a DAW as a real slice marker.

The differentiated half is the live one. A DAW cannot mark a moment it is not
recording, and a phone app cannot either. Hindsight already holds fifteen
minutes of hindsight in RAM; flagging it costs one button.

This is the first item in the owner's re-ranked backlog, and the only one of
the three with no blocker: cue points are metadata and bit-depth independent,
so the 32-bit gap that stops `ApplyBoundaryFades` and `NormalizePeak` does not
touch flags.

## What the owner decided

| Question | Decision |
|---|---|
| Scope of v1 | **Both** live-ring flags and saved-take flag editing |
| Live gesture | **A "Mark" button meaning "now"** — not ribbon-tapping |
| Flags vs. capture window | **Visible on the ribbon, passive** |
| Where cue code comes from | **Copy into this repo** — no dependency on bento |
| When cues hit the WAV | **On disk, at save and on every flag edit** |

## Facts this design rests on

Read out of the code on 2026-09-09. These are the load-bearing ones; each was
checked rather than assumed.

- **`Ring.totalFrames` is a valid absolute clock.** `NewRing` is called exactly
  once, in `NewCapture` (`capture.go:51`). `supervise()` reopens the *stream*,
  never the ring. Absolute frame numbers therefore survive USB unplugs for the
  life of the process, which is what makes a live flag a single integer.
- **That clock stalls during a dropout.** Frames advance only when audio
  arrives, so flags stay pinned to the *audio*, not to wall clock. This is the
  wanted behaviour, and it is why "Mark = now" avoids an age→frame conversion
  that would be ambiguous exactly when audio was missing.
- **`Save()` has a latent race for this feature.** It calls `Snapshot(frames)`
  without learning where the window ended (`save.go:100`), so a flag arriving
  between the snapshot and a separate position read would map to the wrong
  sample.
- **Takes already render a waveform.** `lib/takes.js:323` lazily mounts a
  WaveSurfer per row from `peaks.json` when the row scrolls into view. Saved-
  take flag editing needs no waveform page first.
- **Flags are points, not regions.** They render as positioned DOM ticks, so
  the WaveSurfer Regions plugin — vendored core is 28KB, Regions is *not*
  vendored — is not needed. `sw.js`'s `SHELL` list and cache version stay
  untouched, sidestepping the atomic-precache failure that would otherwise kill
  offline silently.
- **No orphan risk.** Flags live in `.meta.json`, an artifact `RemoveTake`
  (`save.go:318`) already deletes. No new artifact type is introduced.
- **`WriteWAV` writes a canonical 44-byte header** (`wavHeaderBytes = 44`,
  `wav.go:13`) — RIFF, `fmt `, `data`, with nothing after the data chunk. Every
  take this app owns has that shape, which is what makes the O(1) cue append
  below always apply.

## What was rejected, and why

**Depending on bento's `wav` package.** STATE.md carried this as "the single
highest-leverage refactor available to either project". It is off the table for
now, for two independent reasons found this session:

- **bento is private; Hindsight is public.** The repo is
  `gabeduke/bento-librarian`, `isPrivate: true`. A public module requiring a
  private one breaks `go get` for anyone else and forces a credential into the
  release CI, which builds in a clean `debian:bookworm` container.
- **Its module path does not match its remote.** `go.mod` declares
  `module github.com/gabeduke/bento` while the repo is `bento-librarian`, so
  `go get github.com/gabeduke/bento` does not resolve at all.

`internal/wav` is pure stdlib — zero third-party imports — so none of bento's
Wails or beep dependencies would have come along. The obstacle is packaging and
visibility, not coupling. Real sharing means extracting a **new public module**
that both projects depend on. That is worth doing when the waveform page needs
`Peaks` and trim needs `ApplyBoundaryFades`; it is not worth doing for ~150
lines of cue handling.

**Porting bento's `WriteCues` verbatim.** It calls `os.ReadFile(path)`, loading
the entire file into memory, then builds a second buffer of comparable size
before writing atomically. Against a 330 MB take that is roughly 700 MB of
resident memory, and against a 1.4 GB all-channels take roughly 2.8 GB — on a
Pi already holding a ~1.4 GB ring. Its algorithm is correct and its chunk
handling is worth copying; its I/O strategy is not.

**Ribbon-tapping to place a flag.** Deferred, not dismissed. It needs the
age→frame conversion described above, and aiming on a log axis is coarse at the
old end — a pixel is minutes wide near 15m. It pairs naturally with "tap the
ribbon to set the capture length" and should be specced with it.

**Flags held in browser state and posted with the Capture request.** Needs no
server changes, but marks become per-device and vanish on reload. For an app
whose point is being reachable from any browser on the tailnet, a mark made on
the phone must be visible on the tablet.

**Write-only flags, invisible until saved.** Cheapest, but the Mark button
would give no confirmation it registered and no way to choose a window covering
your marks.

**Cues written only at download time.** This is the trim spec's philosophy
("applies at download time, never on disk") and is safest for the audio. It
loses the case that matters most: a file copied by rsync, or read straight off
the card, carries no cues. The owner's stated goal is files already waiting when
the DAW opens.

## Architecture

### Data model

```go
// Flag marks a moment of interest, in frames from the take's first frame.
type Flag struct {
    Frame int64  `json:"frame"`
    Label string `json:"label,omitempty"`
}
```

`Meta` gains a matching field, sorted ascending and deduplicated on write:

```go
Flags []Flag `json:"flags,omitempty"`
```

A struct rather than a bare `[]int64`: v1 never sets `Label`, but shipping an
array of integers would make adding labels later a breaking shape change that
older readers could not tolerate, forcing a `MetaVersion` bump. As a struct the
field stays additive, exactly like `Trim`. **`MetaVersion` stays 1**, following
the BPM precedent.

Frames rather than seconds, for the same reasons `Trim` uses them: sample-exact,
integer, and already the currency RIFF cue points speak.

### Ring changes

Two additions to `internal/audio/ring.go`:

```go
func (r *Ring) TotalFrames() uint64
func (r *Ring) SnapshotAt(frames int) (data []int32, got int, endFrame uint64)
```

`SnapshotAt` is the existing `Snapshot` returning, under the same mutex
acquisition, the absolute frame the window ends at. `Snapshot` stays as a thin
wrapper so existing callers and tests are untouched. This closes the race noted
above: the copy and its position are read atomically or they are meaningless.

### Live flag store

A new `FlagStore` in `internal/audio`, owned by `Capture` and created in
`NewCapture` alongside the ring:

- `Mark(now uint64) uint64` records the current absolute frame and returns it.
- `Active(now uint64, ringFrames uint64) []uint64` returns live flags newest
  last, pruning anything older than the ring as it goes.
- `Remove(frame uint64)` and `Clear()` back the undo path.
- Capacity is capped at **256** flags; the oldest is dropped on overflow, so a
  stuck button cannot grow it without bound.

A save does **not** consume flags. They leave only by ageing out with the
audio, so two overlapping captures both inherit the marks that fall inside
them, and a mistimed capture does not silently destroy a mark.

### Save-time translation

`Save()` switches to `SnapshotAt`. The captured window starts at
`endFrame - gotFrames`; every live flag `F` in `[start, endFrame)` becomes a
take-relative `F - start`. Flags outside the window are simply not in this take
and stay in the store.

The resulting `[]Flag` is written into the take's `Meta` in the same write that
already records BPM, and then into the WAV as cue points.

### Cue chunk I/O

New file `internal/audio/cue.go`, next to the existing `wav.go`. **This is a
deviation from the design presented in chat**, which said `internal/wav`: the
existing WAV code lives in `internal/audio/wav.go`, and a new package for one
more file would split WAV handling across two places for no gain.

`buildCueChunk` and the chunk-walking logic are ported from bento — including
its tolerance for writers that fill only `dwPosition`, and its stripping of any
`LIST`/`adtl` label chunk — and both `dwPosition` and `dwSampleOffset` are
filled so bento's own `ReadCues` reads our files.

```go
func ReadCues(path string) ([]uint64, error)
func WriteCues(path string, offsets []uint64) error
```

`WriteCues` diverges from bento in its I/O. It locates the `data` chunk with
the existing chunk walk, computes `dataEnd`, and requires the file to have the
shape `header + data + optional trailing cue chunk` — the shape `WriteWAV`
produces. Anything else returns an error rather than attempting a rewrite;
there is no ingest path in this app, so no take has any other layout.

The write is then **O(1) in the size of the take** — a few KB regardless of
whether the file is 330 MB or 1.4 GB — and ordered so that no crash can leave
an invalid file:

1. Patch the RIFF size at offset 4 down to `dataEnd - 8`. `fsync`. The file now
   reads as a valid WAV with no cues; anything past `dataEnd` is outside the
   RIFF extent and ignored by every reader.
2. Write the new cue chunk at `dataEnd`. `fsync`.
3. Patch the RIFF size up to `dataEnd + len(chunk) - 8`. `fsync`.
4. Truncate to `dataEnd + len(chunk)` if the file is longer than that — which
   happens when the previous cue chunk held more flags than the new one.

A crash after step 1 or 2 leaves a valid cue-less take. A crash after step 3
leaves a valid flagged take. **The audio bytes are never touched at any point**
— only the 4-byte size field and the region past the data chunk. A 256-flag
chunk is 6,156 bytes.

Offsets are validated against the take's frame count and rejected if out of
range, matching bento.

### API

| Endpoint | Change |
|---|---|
| `POST /api/flag` | Place a live mark at now. Returns `{frame, age_seconds}` so the ribbon can draw it without waiting for the next poll. `409` if capture is unhealthy — there is no audio to mark. |
| `DELETE /api/flag` | `?frame=N` removes one, `?all=1` clears. Backs undo of a mistap. |
| `GET /api/envelope` | Response gains `flags: [ages]`, in seconds-ago, matching the axis the ribbon already thinks in. The ribbon polls this already, so no new poll is added. |
| `PATCH /api/take` | Gains `flags`, a full replacement of the take's array. Rewrites the sidecar and the WAV's cue chunk. Capped at 512 per take. |

### UI

- **Mark button** in the monitor column beside the tier buttons, not in a new
  row. Phone landscape has ~5px of vertical margin — Capture's bottom edge sits
  at 385px in a 390px viewport — so this layout must be re-measured on the
  phone before merge, not after.
- **Ribbon ticks** drawn from `flags` in the envelope response, reusing the
  ribbon's existing log-axis maths on the client.
- **Take ticks** absolutely positioned over each row's WaveSurfer container.
  Click the waveform to add a flag, click a tick to remove it. No plugin.

## Failure behaviour

- **Capture unhealthy when Mark is pressed:** `409`, and the button shows the
  rejection. Marking silence that does not exist would produce a flag pointing
  at nothing.
- **Cue write fails after the sidecar is written:** the take keeps its flags in
  `.meta.json` and the UI still shows them; the WAV simply lacks cues. The
  error is logged and surfaced on the PATCH response. Metadata is the source of
  truth; the cue chunk is a derived export, and losing it must never obscure the
  audio.
- **A take whose WAV is not the expected shape:** `WriteCues` errors, the
  sidecar still updates, and the take remains fully usable.
- **Flags that age out before a capture:** silently dropped. They marked audio
  that no longer exists.
- **A sidecar written by a newer build:** unchanged — `WriteMeta` already
  refuses rather than dropping fields it cannot represent.

## Verification

- `SnapshotAt` returns a window and an end position that agree, under
  concurrent writes, with `-race`.
- `Snapshot` still behaves identically for existing callers.
- `FlagStore`: marks age out at exactly the ring boundary; the 256 cap drops
  oldest-first; remove and clear behave.
- Translation: a flag inside the window, one outside it, one exactly at
  `start`, one exactly at `endFrame-1`, and an empty ring.
- Flags survive a save and appear in the sidecar in ascending order.
- `WriteCues` → `ReadCues` round-trips, including the empty case.
- **Cross-tool golden file:** a take flagged by this code is read correctly by
  bento's own `ReadCues`. This is the test that proves the hand-off, and it is
  the reason both `dwPosition` and `dwSampleOffset` are filled.
- Crash safety: truncating the file after each of the four write steps leaves a
  file that still parses as a valid WAV.
- A pre-flags sidecar still reads clean, and a take with no flags gets no `cue `
  chunk written at all.
- **On hardware, with the EP-136 attached:** mark three moments while playing,
  capture, and confirm the ticks land on the transients they were placed on.
  This is the only check that proves the frame arithmetic end to end, and it
  cannot be done until the interface is plugged back in.

## Out of scope

- **Labels on flags.** The field exists so adding them later needs no
  migration; nothing writes it.
- **Ribbon-tap placement**, and the "capture since first flag" action.
- **Region selection**, trim, splicing. Flags are points.
- **A dedicated waveform page.** Flag editing rides the existing take rows.
- **Ingesting foreign WAVs**, and therefore the streaming rewrite path a
  non-canonical chunk layout would need.
- **Extracting a shared public wav module.** Revisit when the waveform page
  needs `Peaks` and trim needs `ApplyBoundaryFades` — that is when it pays for
  itself.
