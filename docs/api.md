# HTTP API

Thirteen routes, registered in `internal/api/api.go` (`SetupRoutes`). Everything
else the server answers is the static UI under `web/static`.

There is **no authentication and no rate limiting**. `DELETE /api/delete`
destroys takes and `POST /api/trigger` writes hundreds of megabytes, for anyone
who can reach the port. Keep it on a LAN or a tailnet; do not expose it to the
internet.

| Endpoint | Purpose |
|---|---|
| `GET /api/status` | Health, ring fill, per-channel dB, disk, live tempo, version |
| `GET /api/live` | WebSocket: min/max peak bins (~100/s) plus peak-hold |
| `GET /api/envelope` | The buffer ribbon's amplitude envelope over the whole ring |
| `POST /api/trigger?seconds=N` | Save the last N seconds; `0` is the whole ring |
| `GET /api/jams` | Takes, starred first then newest first. Sends an ETag |
| `PATCH /api/take?file=` | Edit a take's label, star, trim, BPM and flags |
| `POST /api/flag` | Mark a moment of interest at the ring's newest frame |
| `DELETE /api/flag` | Remove one live mark (`?frame=`), or every one (`?all=1`) |
| `GET /api/peaks?file=` | Precomputed waveform, so phones do not download audio to draw one |
| `GET /api/download?file=[&dl=1]` | Stream inline, or force a download |
| `DELETE /api/delete?file=` | Remove a take and its sidecars |
| `POST /api/cut?file=` | Export a region of a take as a new take, with 3ms declick fades |
| `GET /api/slice?file=&from=&to=` | A region as a 16-bit WAV with the same fades a cut gets, for auditioning |

`GET` routes also accept `HEAD`, except `/api/live`, which is a WebSocket
upgrade.

---

## `GET /api/status`

The poll everything else hangs off. The UI reads it every two seconds.

```json
{
  "version": "dev",
  "is_recording": true,
  "capture_healthy": true,
  "last_error": "",
  "device": "Demo signal generator (synthetic, 96 BPM)",
  "xruns": 0,
  "ring_seconds": 60,
  "buffered_seconds": 4.82,
  "sample_rate": 48000,
  "channels": 8,
  "save_channels": [1, 2],
  "channel_rms": [-15.54, -15.54, -46.0, -46.0, -46.0, -46.0, -46.0, -46.0],
  "floor_db": -60,
  "last_saved": "",
  "saving": false,
  "disk_free_gb": 262.28,
  "disk_percent": 71.69,
  "min_free_gb": 1,
  "midi_connected": true,
  "midi_bpm": 96
}
```

- `save_channels` is **1-indexed**, matching how the hardware labels its inputs
  and how `SAVE_CHANNELS` is written.
- `channel_rms` is dBFS per channel, from the newest 10 ms bin, clamped at
  `floor_db`.
- `midi_bpm` is `null` whenever there is no defensible reading over the last
  eight seconds — which is also what a connected interface with clock-send
  switched off looks like. `midi_connected` and a null `midi_bpm` together are
  a normal state, not an error.
- `capture_healthy` is what the installer greps for to decide whether the
  service actually came up recording.
- `version` is stamped at build time; a development build reports `dev`.

## `GET /api/live`

WebSocket. The server pushes a frame whenever level bins have accumulated —
each bin is 10 ms wide, so roughly 100 bins a second, batched per frame.

Every array is per channel, so at `CHANNELS=8` each has eight entries:

```json
{
  "bins": [{ "min": [-0.31, -0.29, ...], "max": [0.30, 0.28, ...], "rms": [-15.5, -15.5, ...] }],
  "peak": [-12.4, -12.6, -60, -60, -60, -60, -60, -60],
  "clip": [false, false, false, false, false, false, false, false]
}
```

`min`/`max` are the true sample extremes per channel in −1..1, which is what
lets the client draw a real waveform envelope rather than mirroring a single
RMS scalar. `peak` is a decaying peak-hold in dBFS; `clip` says a sample hit
full scale during the frame.

The server pings every 25 s and expects a pong within 60 s. It reads from the
socket but ignores the contents; reading is what surfaces close frames. Origin
is not checked — the app is reached by hostname, IP and `.local` alias, so
origin pinning would only break access.

## `GET /api/envelope`

What the buffer ribbon draws: a dB-coded amplitude envelope of the entire ring,
not just what the browser has been open for.

| Query | Default | Notes |
|---|---|---|
| `buckets` | `400` | Clamped to 1..600 |
| `spans` | *(none)* | Comma-separated capture lengths in seconds; `0` means the whole ring, matching `/api/trigger` |

```json
{
  "ring_seconds": 60,
  "buffered_seconds": 4.99,
  "edge_seconds": 10,
  "buckets": "AAAAAAAAAOA=",
  "signal_seconds": [4.99, 4.99],
  "flags": [{ "age_seconds": 1.5, "frame": 24000 }]
}
```

`buckets` is base64: one byte per bucket, holding peak magnitude across the
save channels, dB-coded over −60..0 dBFS as `round((db + 60) × 255 / 60)`. A
consumer divides by 255 and has exactly the value the live meters would draw.
Peak, not mean — a peak survives downsampling.

`signal_seconds` is parallel to the `spans` you asked for: how many seconds of
each window were above the signal gate. That is what lets the ribbon tell you
whether a 7-minute capture would actually contain anything.

`edge_seconds` is the age at the ribbon's right edge and the newest age its
logarithmic axis can address.

`flags` is every live mark still in the ring, oldest first. `age_seconds`
positions the tick on the ribbon's log axis, in the same currency the rest of
the envelope speaks; `frame` is the mark's absolute ring frame, which the
client sends back to `DELETE /api/flag?frame=` to undo a mistap.

Returns 503 if the envelope is unavailable, 400 if `buckets` or `spans` will
not parse. `Cache-Control: no-store`.

## `POST /api/trigger?seconds=N`

Copies the last `N` seconds out of the ring and writes a WAV. `seconds=0`, or
omitting it, means the whole ring.

The response is not sent until the WAV and its `.peaks.json` are on disk, and
the BPM stamped. Only the `_preview.mp3` is backgrounded, so a take appears in
the list with `has_peaks` already true and `has_preview` false for the length of
one ffmpeg encode — which is what the UI's "waveform pending…" row is waiting
on.

```json
{ "status": "saved", "name": "jam_2026-09-09_145852.wav", "seconds": 10, "buffered": 22.8 }
```

| Status | When |
|---|---|
| 400 | `seconds` is not a non-negative number |
| 409 | Nothing is buffered yet |
| 507 | Free space is below `MIN_FREE_GB` |
| 500 | The write itself failed |

If a MIDI clock is present, the take's tempo is stamped into its sidecar after
the WAV is safely on disk. A MIDI failure of any kind — no device, a parse
error, a panic — produces a take with no BPM and nothing else.

## `GET /api/jams`

Every take in `OUTPUT_DIR`, **starred first, then newest first**. Starring is
how a take stays in reach once newer ones have pushed it down.

```json
[{
  "name": "jam_2026-09-09_145852.wav",
  "size_mb": 3.66,
  "duration_seconds": 10,
  "created": "2026-09-09T14:58:52-04:00",
  "channels": 2,
  "sample_rate": 48000,
  "has_preview": true,
  "has_peaks": true,
  "preview_name": "jam_2026-09-09_145852_preview.mp3",
  "label": "",
  "starred": false,
  "bpm": 96,
  "flags": [{ "frame": 100 }, { "frame": 900 }],
  "downbeat_frame": null,
  "source": { "name": "jam_src.wav", "start_frame": 1000, "end_frame": 9000 }
}]
```

`source` is present only on a take that was cut from another (see
`POST /api/cut` below); it is absent for a take saved from the ring.

Duration and layout come from each file's own header, so takes recorded under
an older channel configuration still report correctly.

The response carries an `ETag`; send it back as `If-None-Match` and an
unchanged list answers 304. That is what keeps the 5-second poll from
re-rendering the list and interrupting a playing preview.

## `PATCH /api/take?file=`

Merges fields into a take's `.meta.json` sidecar. It is a merge, not a replace:
an absent field is left alone, so starring a take cannot silently clear its
label.

```bash
curl -X PATCH 'http://127.0.0.1:5000/api/take?file=jam_2026-09-09_145852.wav' \
  -H 'Content-Type: application/json' \
  -d '{"label":"warm-up","bpm":128,"starred":true}'
```

| Field | Type | Notes |
|---|---|---|
| `label` | string | Control and Unicode format characters stripped, trimmed, capped at 120 runes |
| `starred` | bool | |
| `trim` | `{start_frame, end_frame}` or `null` | `start_frame` must be `>= 0` **and** `end_frame` must exceed `start_frame`; `null` clears |
| `bpm` | number or `null` | 20–400, rounded to two decimals; rejects NaN and ±Inf; `null` clears |
| `flags` | `[{frame, label}]` or `null` | A full replacement of the take's flags. Capped at 512; `frame` must be `>= 0` and less than the take's frame count; `null` clears. `label` is sanitized like the take label (control characters stripped, trimmed, 120 runes) |
| `downbeat_frame` | integer or `null` | Where bar 1 falls, for the waveform page's grid. `>= 0` and less than the take's frame count; `null` clears |

The response is the merged result:

```json
{ "label": "warm-up", "starred": true, "trim": null, "bpm": 128, "flags": [], "downbeat_frame": null }
```

If `flags` changed, the sidecar write is also mirrored into the WAV as RIFF
`cue ` points, with labelled flags also written as `labl` records in a `LIST`/`adtl` chunk so DAWs show the name beside the marker. That second write can fail on its own — a take whose layout
`WriteCues` does not recognise, say — without the sidecar edit failing with
it: the response carries a non-empty `cue_error` when it does, and the status
stays 200, because the sidecar (the source of truth) already saved.

| Status | When |
|---|---|
| 400 | Missing or bad `file`, malformed JSON, trailing content, or a field out of range |
| 404 | No such take |
| 409 | The sidecar was written by a **newer build** than this one. Rewriting it would drop fields this build does not know about |
| 500 | The sidecar write failed for any other reason. The detail is logged, not returned — the real error names absolute paths and the temp-file scheme |
| 507 | Disk full |

The tempo range is deliberately far wider than any interface will produce,
because the stamped BPM is a device's guess rather than ground truth — see the
MIDI section of [architecture.md](architecture.md). The field exists to be
overridden, including for takes whose clock reading was confidently wrong.

## `POST /api/flag`

Marks a moment of interest at the newest frame the ring holds — the live
counterpart to a take's `flags`. It does not require a healthy capture:
`capture_healthy: false` means no new audio is arriving, not that there is
none, and marking a moment in audio you can still capture is the point of the
feature.

```json
{ "frame": 43199, "age_seconds": 0 }
```

`frame` is the mark's absolute ring frame — the same value `/api/envelope`
later reports it at — so the caller can draw the tick immediately rather than
waiting for the next poll.

| Status | When |
|---|---|
| 409 | The ring is empty. This is the only case with nothing to mark |
| 503 | Capture is not attached to this build at all |

Marking the same frame twice collapses to one flag, which is what happens on
repeated presses while capture is dead and the ring's frame counter has
stopped advancing.

## `DELETE /api/flag`

Removes live marks. Backs undo of a mistap.

| Query | Notes |
|---|---|
| `frame` | Removes the one mark at this absolute frame |
| `all` | Clears every live mark. Only the literal values `1` or `true` trigger it — `?all=0` and `?all=false` are left alone, not read as truthy |

```json
{ "status": "removed" }
```

| Status | When |
|---|---|
| 400 | Neither `frame` nor `all` given, or `frame` does not parse |
| 404 | No mark at that `frame` |
| 503 | Capture is not attached to this build at all |

## `GET /api/peaks?file=`

The `.peaks.json` written alongside the take: a precomputed min/max waveform, so
a phone can draw a take without downloading the audio.

```json
{ "version": 1, "channels": 2, "sample_rate": 48000, "duration": 10, "buckets": 1026, "data": [[...]] }
```

400 if `file` is missing or is not a bare `.wav` name, 404 if peaks have not
been generated. Served immutable — a take's peaks never change.

With `from`, `to` (frames, `0 <= from < to <= frames`) and `buckets`
(`1..4096`) given together, the peaks are computed on demand over exactly
that range instead of served from the file. The response has the same shape
plus `"from"`, and `duration` describes the range. All three or none: a
partial set is 400. The waveform page uses this for every zoomed view.

## `GET /api/download?file=[&dl=1]`

Serves the take or its mp3 preview, with range requests. Only `.wav` and `.mp3`
names in `OUTPUT_DIR` are addressable; anything with a path in it is rejected.

Inline by default so `<audio>` can stream it. `dl` adds a
`Content-Disposition: attachment` header instead — **any non-empty value**, so
`dl=0` and `dl=false` force the download just as `dl=1` does. 400 if `file` is
missing or rejected, 404 if it is not there.

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

## `GET /api/slice?file=&from=&to=`

Streams frames `[from, to)` as a complete **16-bit** PCM WAV with the same
3ms fades `POST /api/cut` applies, so what the waveform page loops is exactly
what a cut will produce. 16-bit because browsers cannot reliably decode
32-bit integer WAV. Capped at 60 seconds. `Content-Length` is exact.

| Status | When |
|---|---|
| 400 | Bad `file`, non-integer or inverted frames, past the end, or over 60s |
| 404 | No such take |

## `DELETE /api/delete?file=`

Removes the take and every sidecar: `_preview.mp3`, `.peaks.json`,
`.meta.json`. Takes the `.wav` name.

```json
{ "status": "deleted", "name": "jam_2026-09-09_145852.wav" }
```

Succeeds whether or not the files were there. 400 if `file` is missing, has a
path in it, or has an extension other than `.wav`.

---

## Names that are kept on purpose

`jam_saves` and `GET /api/jams` predate the rename to Hindsight. They keep
their names deliberately: the endpoint is baked into installed PWAs and cached
service workers, and renaming it would break them for no gain.
