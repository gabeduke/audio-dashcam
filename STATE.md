# Audio Dashcam — session state

**Last updated:** 2026-09-07 · **Branch:** master
**Repo:** https://github.com/gabeduke/audio-dashcam (public)

An always-listening audio buffer for a Raspberry Pi. It continuously captures
the stereo master from a USB interface into a memory ring; pressing Capture in
the web UI writes the last N seconds to disk. Nothing touches disk until asked.

---

## TL;DR

The Go rework is **finished, deployed and verified**, with one unverified
assumption (below). The Python original has been removed.

Now published as a public repo. Pre-publication history was **squashed to a
single initial commit** because earlier commits carried home network details
(public IP, service domains) that scrubbing the working tree does not remove
from history. The old commits survive only in this machine's reflog — the
Python original and the draft k3s ingress are recoverable from there for now,
but not from the remote.

The next feature set — take triage, one-region trim, batch export — is
**designed and specced**, not implemented. Spec:
`docs/superpowers/specs/2026-09-07-take-triage-trim-design.md`.

HTTPS will come from **Tailscale**, not the k3s cluster. That decision is new
and it retires the previous ingress plan.

---

## Part 1 — The Go rework: DONE

### The seven problems and what was wrong

| # | Symptom | Root cause | Status |
|---|---|---|---|
| 1 | Not mobile friendly, not installable | Zero media queries; 4-col table + inline `<audio>` overflowed at 375px; no manifest; render-blocking Google Fonts `@import` on a LAN device | fixed |
| 2 | 10-min buffer too long | Hardcoded `BufferMinutes = 10` → 921 MB heap at boot, 921 MB WAV per capture | fixed |
| 3a | Preview cut out | `tbody.innerHTML = ''` every 5s destroyed the playing `<audio>` element | fixed |
| 3b | Preview sounded wrong | `ffmpeg -ac 2` on 8ch assumes 7.1 → ch3 folded to mono centre, **ch4 discarded as LFE** | fixed |
| 4 | Waveform useless | Server sent one RMS scalar/50ms; client scaled it linearly so it was flat or pegged; canvas sized once at parse time; a `fillRect` wash painted over it | fixed |
| 5 | *(not reported)* Pi ran hot | `LowLatencyParameters` asks a USB device for an impossible deadline → PortAudio busy-polls | fixed |
| 6 | *(not reported)* Silent capture death | Nothing cleared `isRecording`, so a dead stream never restarted while `/api/status` reported "Active" forever. Ring was also read/written unsynchronised | fixed |
| 7 | *(not reported)* No disk guard | Python had it, the Go port dropped it, the README still claimed it | fixed |

### Measured before → after, on the running Pi

| | before | after |
|---|---|---|
| CPU (idle capture) | **84%** of a core | **1.1%** |
| loadavg | 1.01 | 0.01 |
| RAM (RSS) | 164 MB (of a 921 MB ring) | 192 MB (of a 176 MB ring) |
| Capture size | 921 MB (10 min only) | **11 MB** (30 s), in 771 ms |
| Preview size | 14.4 MB | **480 KB** |
| Preview level vs source | **-30.2 dB** (source -18.7) | **-19.2 dB** |
| xruns | n/a | 0 |

**These are Go-vs-Go**, before and after the rework — not Python-vs-Go. The CPU
win was the `LowLatencyParameters` busy-poll, a bug that could have existed in
either language. The case for Go over Python rests instead on: 17 focused files
versus one 387-line script with HTML in a `render_template_string` blob; an
audio callback that structurally cannot block the device (Python's did numpy
conversion inline under the GIL); no `b''.join()` doubling memory on save; and a
single static binary instead of venv + `python3-pyaudio` + `portaudio19-dev`.

### Structure

- Config is env-driven via `~/audio-dashcam/dashcam.env` (see `deploy/dashcam.env.example`)
- `deploy.sh` rsyncs, builds on the Pi (PortAudio is cgo, so it must build there),
  and restarts. Host comes from an untracked `deploy.local.env`.
- nginx on the Pi is patched for WebSocket upgrade (backup in `/etc/nginx/backups/`)

---

## Part 2 — Next feature set: DESIGNED, not implemented

**Spec:** `docs/superpowers/specs/2026-09-07-take-triage-trim-design.md`.
**Phase 1 plan:** `docs/superpowers/plans/2026-09-07-take-identity-phase1.md`
(8 TDD tasks, ready to execute). Phases 2 and 3 are specced but not yet planned.

**Awaiting a decision:** execute the plan subagent-driven (a fresh subagent per
task, review between) or inline in-session. Nothing is blocked otherwise.

### Requirements settled

| Question | Answer |
|---|---|
| Take lifecycle | **Staging** — the Pi is a holding pen; keepers get pulled to a DAW within days. Triage speed + export quality matter; search/archive do not. |
| Friction | **Both** triage and egress, plus a new ask: **"chop up a sample"**. |
| Chop scope | **Trim — one region per take.** Not multi-slice, not auto-detect. |
| Device | **Phone, right after playing.** Touch-first at 390px. Tablet supported via breakpoints (>= 600px), not a separate design. |

### The design in one paragraph

A `jam_<ts>.meta.json` sidecar per take holds `{name, starred,
trim:{start_frame, end_frame}}`. Absent file = untrimmed unnamed take, so zero
migration. Trim applies **at download time**, never on disk: takes are
uncompressed PCM, so a trim is a byte-range slice plus a rewritten header — no
ffmpeg, no re-encode, no temp file, no disk cost, re-editable forever. Batch
export is a streaming zip through the same writer.

### Phases

1. **Identity** — sidecar, inline rename, star toggle, starred-first ordering.
   No new audio code. Biggest usability win per line. **Ship this first.**
2. **Trim** — `DataOffset` on `WAVInfo`, export handler with declick, range
   peaks endpoint, editor sheet.
3. **Egress** — multi-select, streaming zip.

### Two findings that shaped it

1. **Declick is required.** A raw byte-slice clicks at both cut edges. Fix is a
   3 ms linear fade (144 frames @ 48k) on leading and trailing frames, raw copy
   between. Found in the bento project, which already carries `Declick` for the
   same reason.
2. **Stored peaks cannot be zoomed.** `peakBuckets = 1024` is fixed regardless
   of duration — 29 ms/bucket at 30 s, 117 ms at 120 s. Raw drag on a 390px
   phone is 77–308 ms/px, far too coarse to place a transient. Hence a
   `/api/peaks?from=&to=` range endpoint feeding a detail waveform under the
   overview, giving 5 ms/px.

---

## Part 3 — HTTPS via Tailscale (decided, not built)

**Decision:** use Tailscale, not the k3s cluster, to put the dashcam on HTTPS.

`tailscale serve` yields a trusted cert on a `*.ts.net` name with no
port-forward, no cert-manager, and no DNS01. This unlocks the Android PWA
install (the service worker already guards on `isSecureContext`, so no code
change is needed) and removes any dependency on the cluster.

Why this replaced the earlier plan: **the Pi is not in the cluster.** The
previous approach routed the ingress controller to an off-cluster host through a
selector-less Service plus a hand-maintained EndpointSlice — machinery whose
whole purpose was working around the target not being in Kubernetes. The cluster
has also been unreliable enough to require regular attention. Tailscale gets the
same result with none of it.

The draft k8s ingress manifest has been deleted; it is in git history if needed.

**Open:** confirm the Tailscale free tier limits (100 devices / 3 users at time
of writing) against the actual tailnet — unverified, the CLI is not installed on
the dev Mac.

---

## Part 4 — Related projects

**`bento`** — a working Wails (Go + web) app that chops samples and lays out
cards for a 1010music Bento hardware sampler. It already imports Akai MPC `.xpm`
programs and converts their slice points to RIFF cue markers, so it is already
the MPC bridge.

The goal is one pipeline:

```
audio-dashcam  ->  bento  ->  Bento hardware / MPC
   (capture)      (chop, convert, card layout)
```

The dashcam is the missing capture front-end. The shared currency is
**sample-offset frames + RIFF `cue ` chunks**, which is why this design stores
trim bounds as frames. Writing a `cue ` chunk on export is the natural bridge
and a small later addition. Bento's hardware constraints (≤2 channels, `.wav`,
≤2 GB, no `._*`/`.DS_Store`) are already satisfied by the default 2-channel save.

**Homelab cluster** — the k3s cert outage and related breakage are tracked in
the separate homelab repo (`STATE.md` and `docs/tls-http01-outage.md` there),
handled in its own sessions. Nothing there blocks this project any more, now
that HTTPS is going via Tailscale.

---

## Remaining

### Verification — the one open item
- [ ] **Confirm a real signal end-to-end.** The interface was silent all session
      (-76 dBFS noise floor), so the visualizer's maths and rendering are proven
      but no real audio has ever flowed through them. `SAVE_CHANNELS=3,4` is
      inferred from an earlier capture, not live-confirmed. Play something, open
      the UI, expand **Input channels**, and check that channels **3 & 4** are the
      pair that moves. If not, change `SAVE_CHANNELS` in `dashcam.env` and restart.
- [ ] Capture a real jam and confirm the preview sounds like the room.

### Build
- [ ] **Execute the Phase 1 plan** (sidecar, rename, star, starred-first).
- [ ] `superpowers:writing-plans` for Phase 2 (trim), then Phase 3 (egress).
- [ ] Tablet breakpoints (>= 600px), deferred to land with the Phase 2 trim
      editor — the screen that actually benefits from the width. Cap and centre
      the content column, Takes list two-up, waveforms side by side. Extra width
      helps drag resolution but does not replace the detail view: a 30s take is
      77ms/px at 390px and still 29ms/px at 1024px.

### Known gotchas for whoever picks this up
- `audio/capture.go` is cgo/PortAudio, so `go test ./audio/...` may not build on
  a Mac. Run tests on the Pi: `ssh "$DASHCAM_HOST" 'cd ~/audio-dashcam/v2-go && go test ./...'`.
- There are no JS tests and no harness; frontend changes are verified manually.
- The spec says the sidecar's human name is `name`; the plan corrects it to
  `label` because `Take.Name` is the filename and the row key in `takes.js`.
  The plan's last step updates the spec.

### Infrastructure
- [ ] Set up `tailscale serve` on the Pi; confirm the PWA installs on Android.
- [ ] Verify tailnet device count against the free-tier limit.

### Tuning, when there is real usage to judge by
- [ ] Decide whether `RING_SECONDS=120` is right. Longer costs RAM linearly
      (~1.5 MB/s): 300 s ≈ 440 MB, 600 s ≈ 879 MB.
- [ ] Consider setting `MAX_SAVES` so the card cannot fill silently.

---

## Handy commands

```bash
# deploy (rsync + build on the Pi + restart); needs deploy.local.env
./deploy.sh

# tune without rebuilding
ssh "$DASHCAM_HOST" 'vi ~/audio-dashcam/dashcam.env && systemctl --user restart audio-dashcam'

# health
curl -s http://<pi>/api/status | python3 -m json.tool
```
