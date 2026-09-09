# Development

## The demo is the fast path

```bash
CGO_ENABLED=0 go run ./cmd/hindsight --demo
```

That runs the whole application — ring, levels, envelope, saving, previews, the
UI — against a synthetic 96 BPM loop, with no hardware and no PortAudio.

`CGO_ENABLED=0` is not optional here. Go enables cgo by default wherever a C
compiler exists, and the PortAudio binding's only directive is
`#cgo pkg-config: portaudio-2.0`, so a default build on a machine without the C
library fails with `Package 'portaudio-2.0' not found` before anything runs.
The build tags in `internal/audio` mean the flag costs nothing: `CGO_ENABLED=0`
selects `source_nocgo.go`, and the demo source never needed the device path.

Anything that is not specifically about the audio device can be developed and
tested this way, and CI checks that the demo still boots without cgo on every
run.

**Port 5000 is occupied by ControlCenter on macOS** (AirPlay Receiver). Use
`PORT=` for anything you run locally:

```bash
RING_SECONDS=120 OUTPUT_DIR=/tmp/hindsight PORT=5173 \
  CGO_ENABLED=0 go run ./cmd/hindsight --demo
```

`--version` prints the build version and exits. It reads `dev` unless the
binary was stamped by the release workflow.

## Building against real hardware

PortAudio is cgo, so the C library and headers have to be present:

```bash
brew install portaudio            # macOS
sudo apt install portaudio19-dev  # Debian / Raspberry Pi OS
go build ./cmd/hindsight
```

A macOS build compiles and runs, and will try to open whatever PortAudio
enumerates there. Useful for exercising the device path; it is not the target,
and no macOS interface has been tested against it.

## Testing

```bash
gofmt -l .
go vet ./...
go test -race ./...
./scripts/test-release-scripts.sh
```

`test-release-scripts.sh` covers `next-tag.sh` and `changelog-entry.sh` — the
two shell scripts the release workflow depends on, which otherwise would only
ever be exercised by a real release.

**There are no JS tests.** The UI is checked by hand and, when something needs
it, by a throwaway Playwright script in a scratch directory. Those are not
committed; there is no runner to add one to.

## Deploying to a Pi

`deploy.sh` rsyncs the tree, builds on the Pi (PortAudio is cgo, so it is built
where it runs) and restarts the service. It needs an **untracked**
`deploy.local.env` next to it:

```bash
# deploy.local.env — not tracked, never commit it
HINDSIGHT_HOST=pi@<pi-host>
```

```bash
./deploy.sh              # sync, rebuild, restart
./deploy.sh --static     # sync web/static only
```

`--static` takes about a second: no rebuild, no restart. The UI is served with
`http.FileServer` straight from disk on each request, so an HTML, CSS or JS
change is live on the next reload. Use the full deploy only when Go code
changed.

If an ssh agent is holding your key hostage — 1Password installs a global
`IdentityAgent` in `~/.ssh/config`, and signing fails whenever the vault is
locked — set `HINDSIGHT_NO_AGENT=1` in `deploy.local.env` to bypass it with a
throwaway ssh config. `HINDSIGHT_SSH_KEY` picks the key.

The older `DASHCAM_*` spellings of these variables still work, so an existing
`deploy.local.env` keeps working after the rename.

## Releases

Merging to `master` runs the test suite, then builds and publishes:

- a tag `vYYYY.MM.DD.N`, where `N` increments per release that UTC day. It is
  the highest existing suffix plus one, not a count — a deleted tag leaves a
  gap rather than causing a collision
- `hindsight_<tag>_linux_arm64.tar.gz` and `SHA256SUMS`
- a `CHANGELOG.md` entry, committed back to `master` with `[skip ci]`

The binary is built inside a `debian:bookworm` container on an arm64 runner.
Raspberry Pi OS Bookworm ships glibc 2.36 and Ubuntu 24.04 links 2.39, so a
binary built on the bare runner would not start on the Pi.

Nothing about this can be rehearsed before it is on `master` — the first merge
is also the workflow's first run.

## The Python probes

Diagnostics, not part of the runtime. They are in `scripts/` in the repo and
are deliberately not shipped in the release tarball.

| Script | Question it answers |
|---|---|
| `channel-probe.py` | Which USB channel pair carries the master? Holds peak dBFS per channel over a run and prints a verdict per pair |
| `midi-probe.py` | What does the interface actually send over MIDI, and when? Walks three phases and prints a verdict per question |
| `take-envelope.py` | What does a take look like? Reduces a WAV to a base64 amplitude envelope, so a 346 MB take can be judged without copying it off the Pi |

All three are standard library only — no pip, no virtualenv. What they reach
for differs:

- `channel-probe.py` talks to the HTTP API, so it runs from anywhere that can
  reach the Pi.
- `take-envelope.py` reads a WAV off disk directly and touches no API at all,
  so it runs wherever the file is.
- `midi-probe.py` shells out to `amidi`, which comes from **`alsa-utils`**
  (`sudo apt install alsa-utils`); without it the script exits with
  `amidi not found. Install alsa-utils.` It has to run on the Pi, with the
  interface plugged in.

`midi-probe.py` and `take-envelope.py` are also where several of the MIDI and
envelope decisions are argued out, in their docstrings. Worth reading before
changing either subsystem.

## Screenshots

`docs/images/{desktop,tablet,phone}.png` are captured against the demo, never
against real hardware — this repo is public, and a screenshot of the real rig
would put its hostname in the URL bar.

Playwright is not a dependency of this repo — there is no `package.json`. The
script imports it, so it has to be installed somewhere Node's ESM resolver will
find from `scripts/`, which in practice means the repo root:

```bash
npm install --no-save playwright@1.49.0     # creates ./node_modules (gitignored)
npx playwright install chromium

RING_SECONDS=120 OUTPUT_DIR=/tmp/hindsight-shots PORT=15173 \
  go run ./cmd/hindsight --demo &
HINDSIGHT_URL=http://127.0.0.1:15173 node scripts/screenshots.mjs
```

Delete `node_modules/` afterwards; nothing else needs it.

It fills the ring for 45 seconds so the ribbon has structure to show, takes one
capture so the library is not empty, waits for the waveform to actually mount,
and then shoots three viewports at 2× scale. The demo signal is derived from a
sample counter rather than the clock, so the waveform is identical run to run
and a re-shoot produces a comparable image.

## Conventions

**No tracked file carries home network details.** No tailnet name, no MagicDNS
hostname, no LAN or CGNAT address, in code, comments, docs or commit messages.
This repository's history was squashed once to remove exactly that. Use
`$HINDSIGHT_HOST`, `<pi-host>`, `<pi-host>.<tailnet>.ts.net`. Personal
configuration lives in `deploy.local.env`, which is untracked.

**Comments say why, not what.** The expensive findings in this codebase — the
busy-poll, the 7.1 fold, the ALSA backlog, the pre-fader tap — are recorded
next to the code that works around them, because the code alone looks
arbitrary. Keep doing that; it is most of what these docs are made of.
