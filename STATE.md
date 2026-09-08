# Audio Dashcam — session state

**Last updated:** 2026-09-08 · **Branch:** `master` (phase 1 merged)
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

Take triage — **phase 1 of** the triage/trim/export feature set — is now
**shipped and running on the Pi**: per-take sidecar, star toggle, inline
rename, starred-first ordering. Phases 2 (trim) and 3 (egress) are specced but
not planned. Spec:
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

**Executing now**, subagent-driven: a fresh implementer per task, then a spec
compliance review and a code quality review before the next task starts.

Work is on branch **`feat/take-identity`**, not master.

| Task | Status |
|---|---|
| 1. Sidecar type + atomic IO | **DONE** — `c355b65`, `6ddfef6`, `6d4e298`, `92ad231`. Both reviews passed, 10 tests. |
| 2. Merge sidecar into ListTakes | **DONE** — `5bbe2cb`. Review clean, no issues. |
| 3. Starred-first ordering | **DONE** — `195461a`, plus a doc-comment fix landing |
| 4. RemoveTake cleans the sidecar | **DONE** — `0255de7` |
| 5. PATCH /api/take | **DONE** — `d0d506f`, `3449c3a`. Security review clean; its 8 correctness fixes landed. |
| 6. Star toggle (JS) | **DONE** — `dd7d6e2` |
| 7. Inline rename (JS) | **DONE** — `23ed502` |
| 8. Styling | **DONE** — `8d99246`; spec field name `5de2274` |
| 8b. Deploy + verification | **DONE** — deployed, verified against the running Pi |

### Also shipped this session, separate from phase 1

**Topbar overflow fix — on `master`, pushed, not yet deployed.** Reported from the
running app: with the interface powered off the status read
`open "EP-136: USB Audio (hw:2,0)": Illegal combination of I/O devices` and on a
phone it painted over the logo and title. Two compounding causes — the bar
rendered `s.last_error` verbatim, and `.health` set `white-space: nowrap` without
`min-width: 0`, so as a flex item it defaulted to `min-width: auto` and refused
to shrink, while `.brand b` had no `text-overflow` and overflowed rather than
truncating. The bar now shows a short status with the full text in a toast (once
per distinct error, since status polls every 2s) and in the title. CSS fixed
independently so it stays safe if a long string ever returns. Verified at 390px
by forcing the original error back in: no intersection, no horizontal scroll.

**Deployed.** This shipped along with phase 1; the bar now reads "recording"
with the interface connected and `last_error` empty.

### Seven defects found so far — all in the plan, none in the implementations

1. **Version normalization defeated the version field** (Task 1) — see below.
2. **The chmod fix had no regression coverage** (Task 1) — a reviewer proved it by
   deleting the `os.Chmod` call and watching all nine tests stay green. Test added.
3. **`ListTakes`' doc comment went stale** (Task 3) — the plan's prescriptive diff
   changed the sort without updating the comment above it, leaving
   "returns takes newest first" directly over a starred-first comparator. The
   implementer flagged it rather than guessing; fixed, and the plan now carries an
   explicit step for it.
4. **Label truncation splits UTF-8 runes** (Task 5) — `label[:maxLabelLen]` slices
   bytes. A mixed ASCII/multi-byte label cuts mid-rune, stores mojibake, and the
   U+FFFD replacement pushes it to 122 bytes — past the cap it was enforcing.
   Fixed by capping runes.
5. **The PATCH response drops the fields it just cleared** (Task 5) — it returned
   `audio.Meta`, whose `omitempty` omits `label`/`starred` when empty, so a client
   merging the response cannot clear its local state. Replaced with an explicit
   response struct.
6. **A prescribed regression test could not fail** (Task 5) — the security review
   supplied a reproducer for defect 4 (`119×"x" + "あ" + "yy"`, asserting valid
   UTF-8 and a 120-rune result). It passes under the *buggy* code too: the byte
   cut orphans one byte of the 3-byte rune, `json.Marshal` replaces that byte
   with exactly one U+FFFD, and the result is valid UTF-8 at exactly 120 runes
   — the same count the fix produces. The implementer caught it by reverting the
   cap and watching the new test stay green, then substituted
   `60×"x" + 100×"あ"`, which discriminates (80 runes buggy, 120 fixed).

7. **A poll mid-rename saved a name the user never confirmed** (Task 7) —
   `render()` reorders rows with `insertBefore`, and moving a node blurs the
   input inside it. The blur handler commits, so a list that reordered while
   you were typing silently wrote a half-finished label. Reproduced by starring
   the row being edited from a second client: the server ended up with
   `"half-typed"`. Holding the *text* while editing (which the plan did) was not
   enough — the node still moved. A row mid-edit is now left in place and the
   move deferred to `endEdit`. Note the plan already **accepted** a two-tab race
   here, but its cost was a lost star and one more tap; writing an unconfirmed
   name is a worse outcome than the one that was accepted, which is why this was
   fixed rather than noted. A new capture does **not** trigger it — inserting at
   the front shifts every existing row's index by one, so none of them move; it
   takes a delete or a star from another tab.

Defects 4-7 were found by reviewers and implementers *executing* the code
adversarially, which is the thing that cannot be caught by re-reading a plan.
Defect 6 is the sharpest of them: a test that cannot fail is worse than no test,
because it reads as coverage. **Verify a new regression test actually fails
against the unfixed code** — every one of these came from someone pushing back,
not from re-reading. Worth keeping the review layer for the remaining tasks.

### Review findings on Task 1 — both were flaws in the plan, not the code

1. **Version normalization defeated the version field.** `ReadMeta` did
   `got.Version = MetaVersion` unconditionally, so a future v2 sidecar would be
   silently downgraded and any fields v1 does not know would be dropped on the
   read-modify-write the PATCH handler performs. Fix: normalize only the zero
   case, and have `WriteMeta` refuse to rewrite a sidecar newer than it can
   represent. The plan has been corrected to match.
2. **`MetaPath` was exported for no caller.** Unexported to `metaPath`, matching
   its siblings `previewPath`/`peaksPath` in `save.go`. Plan call sites updated.

Also applied: sidecar file mode `0644` to match the other sidecars (`os.CreateTemp`
lands at `0600`), tests for the version cases and the update-in-place path, and a
doc comment saying why `ReadMeta` is deliberately silent (`ListTakes` calls it
per-take on a 5-second poll, so logging would be thousands of lines a day).

### Task 5 security review — clean, and worth not re-litigating

~40 adversarial probes: URL-encoded and double-encoded traversal, absolute paths,
Windows separators, null bytes, unicode normalization, symlinked sidecars and
WAVs, CSRF, method confusion, oversized and deeply-nested bodies, 50 concurrent
writers. **None reachable.** Two protections are correct by construction rather
than luck: `safeTakeName` rejects via `Base(raw) == raw` instead of normalizing,
and `WriteMeta`'s temp-file-plus-rename defeats a symlink-swap that a plain
`os.WriteFile` would lose to.

Two verdicts to carry forward:
- **Stored XSS is inert** because Go's default `SetEscapeHTML(true)` plus an
  `application/json` content type means browsers do not sniff it. That protection
  is a default, not a chosen control — if `writeJSON` is ever swapped for an
  encoder with `SetEscapeHTML(false)` it silently disappears. The renderer must
  keep labels on `textContent` (`takes.js` already does this everywhere).
- **An unbounded `end_frame` is deliberately NOT validated here.** The spec
  assigns clamping to the export path because a WAV can be shorter than when the
  trim was set. Validating at write time would be redundant and still wrong later.

No auth or rate limiting on this endpoint — but `handleDelete` can already destroy
takes, so this does not change the threat model. Auth belongs with the Tailscale
work, not here.

### Phase 1 verified on the running Pi

Deployed from `master` at `9b37c06`. Service active, `capture_healthy: true`,
`last_error: ""`, ring 120s, 8ch @ 48k. `go test ./...` green on the Pi.

Checked against the real backend (Playwright at 390px, not the stub):

- The one pre-existing take — recorded before any of this — lists with
  `label: ""`, `starred: false` and **no sidecar on disk**. Zero migration, as
  designed.
- Starring it created `jam_<ts>.meta.json` at mode **0644** holding
  `{"version":1,"starred":true}`. That is the Task 1 chmod fix confirmed in
  production, and `omitempty` correctly leaving `label` out.
- Renaming wrote `"label":"Kitchen soundcheck"`; it survived a reload.
- Clearing both returned `{"label":"","starred":false,"trim":null}` — the
  Task 5 defect-5 fix (explicit response struct) working on real hardware,
  since `omitempty` would have dropped exactly those two fields.
- The take was restored to its original unlabelled, unstarred state.

**Still unverified on hardware:** that deleting a take removes its sidecar.
Not exercised because the only take on the device is real and deleting it to
prove a point is a bad trade; it has unit coverage from Task 4 (`0255de7`).

Note the owner was clicking in the UI at the same time as this pass — the
accepted read-modify-write race, in the wild, converging harmlessly.

### Where the code stands

**26 tests passing** — 15 in `audio`, 11 in `api` (that package had none before
Task 5). `go build ./...`, `go vet ./...`, `gofmt -l .` clean.

**The whole backend is done.** The sidecar format and its atomic IO, the merge
into `ListTakes`, starred-first ordering, sidecar cleanup on delete, and
`PATCH /api/take`. Nothing is wired to the UI yet — the frontend still ignores
the new `label`/`starred` JSON fields, verified deliberately in review.

**Tasks 6-8 are frontend, and changed character.** There are no JS tests and no
harness, so the plan verified Tasks 6 and 7 only through the manual checks in
Task 8 — on hardware, at the end, after three commits had landed.

That was too late to catch anything, so these were instead verified against a
**local stub of `/api/jams` and `/api/take`** (a ~70-line Python server, in the
session scratchpad, not committed) driven through Playwright. It is what caught
defect 7, which no amount of re-reading would have found. Worth rebuilding if
Phase 2's trim editor needs the same treatment; the stub only has to serve
`static/` and fake three endpoints. It does not implement `/api/live`, so the
top bar reads "server unreachable" and the capture button reads
"Full undefineds" — both are stub gaps, not regressions.

**Verified via the stub:** star toggles and persists; starred sorts first;
unstar returns to date order; Enter commits a rename; Escape discards; an
unchanged name writes nothing; clearing falls back to the dimmed timestamp; a
507 on the PATCH toasts "Could not star: disk full"; a 500 on the *list* leaves
no toast while the write still persists; and at 390px there is no horizontal
scroll, the star tap target is 48px, and a long label ellipsises without
colliding with the duration.

### Dispatch shape

Tasks 3 and 4 are being dispatched as one job: both are small pure additions to
existing functions in the same file (`save.go` — a sort comparator and one
`os.Remove`), against tests already written out in the plan. Their separate
commits are preserved. Task 2 got its own dispatch because it changes the `Take`
struct, which is the JSON contract the frontend consumes.

Heavyweight (Opus) code-quality review is reserved for changes that define a
contract. Task 1 earned it and it paid for itself; a sort comparator does not.

### Carried forward into Task 5

`PATCH /api/take` is a read-modify-write, so two browser tabs — one starring
while the other renames — lose one change. `WriteMeta` itself is safe (unique
temp name, atomic rename, last write wins); the race is at the handler. Recorded
in the plan as **deliberately accepted**: single-user device on a LAN, and the
cost is one more tap. The fix, if it ever matters, is a package-level mutex
around the handler's read-modify-write, not a change to `meta.go`.

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
- [x] **Execute the Phase 1 plan** (sidecar, rename, star, starred-first) — all
      8 tasks committed on `feat/take-identity`. **Not yet deployed or verified
      on hardware**; see the deploy item below.
- [x] **Deploy phase 1 and verify on the Pi** — done; see "Phase 1 verified on
      the running Pi" above. Carried the topbar overflow fix out with it.
- [x] Merge `feat/take-identity` to `master` and push — `9b37c06`.
- [ ] Confirm on an actual phone. The 390px pass was a desktop browser at phone
      width, which proves layout and behaviour but not tap feel or Android PWA
      install.
- [ ] `superpowers:writing-plans` for Phase 2 (trim), then Phase 3 (egress).
- [ ] Tablet breakpoints (>= 600px), deferred to land with the Phase 2 trim
      editor — the screen that actually benefits from the width. Cap and centre
      the content column, Takes list two-up, waveforms side by side. Extra width
      helps drag resolution but does not replace the detail view: a 30s take is
      77ms/px at 390px and still 29ms/px at 1024px.

### Known gotchas for whoever picks this up
- `audio/capture.go` is cgo/PortAudio. **Resolved on this Mac** with
  `brew install portaudio`, so `go build ./...` and `go test ./...` now work
  locally and the TDD loop does not need the Pi. On a machine without it, run
  tests on the Pi instead:
  `ssh "$DASHCAM_HOST" 'cd ~/audio-dashcam/v2-go && go test ./...'`.
- The repo had **no Go tests at all** before this phase; Task 1 writes the first.
  A green `go test ./...` that reports `[no test files]` is the old baseline, not
  a passing suite.
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
