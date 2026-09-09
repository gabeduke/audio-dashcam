# Installing on a Raspberry Pi

> **Status.** `install.sh` is written and its guard clauses are rehearsed, but
> it has not yet been run end to end on a real Pi. The apt install, the unit
> install, lingering and the `audio-dashcam` migration are all unproven. Read
> what each step does before running it, and expect to fix something.
>
> The release workflow is in the same position: it only runs on a merge to
> `master`, so the first merge is also its first run. Until one has completed
> there is nothing behind the download URLs below — build from source in the
> meantime, per [development.md](development.md).

## Prerequisites

- **A Raspberry Pi 5.** 8 GB if you want the default 900-second ring — the
  buffer is uncompressed and lives in RAM. "`RING_SECONDS` and RAM" in
  [configuration.md](configuration.md) has the arithmetic.
- **64-bit Raspberry Pi OS Bookworm.** The release binary is arm64 and is built
  inside `debian:bookworm`, against glibc 2.36. A newer Pi OS is fine; an
  older one is not.
- **An SSH login as the user that will run it — not `sudo`.** Hindsight
  installs as a systemd *user* service. `sudo ./install.sh` gives you a service
  under root, in root's home, and the installer refuses it: without a systemd
  user session it stops with an error rather than installing something broken.
- `sudo` available for two things only: installing packages, and enabling
  lingering.

## Install from a release

Every merge to `master` publishes `hindsight_<version>_linux_arm64.tar.gz` and
a `SHA256SUMS` alongside it.

```bash
cd ~
curl -fsSLO https://github.com/gabeduke/hindsight/releases/latest/download/SHA256SUMS
tarball=$(awk '{print $2}' SHA256SUMS)
curl -fsSLO "https://github.com/gabeduke/hindsight/releases/latest/download/$tarball"
sha256sum -c SHA256SUMS
tar xzf "$tarball"
cd "${tarball%.tar.gz}"
./install.sh
```

`sha256sum -c` must print `OK`. If it does not, stop.

The archive contains `bin/hindsight`, `web/static`, `deploy/`, `install.sh`,
and copies of the README, CHANGELOG and LICENSE. It does **not** contain
`scripts/` — the Python probes below come from a git checkout. `docs/` is not
in it either, so the relative links in the archived README (including the one
to this page) resolve only against the repository on GitHub, not against the
directory you just unpacked.

### What the installer does

1. Refuses to continue unless this is Linux on `aarch64`, with `systemctl`,
   `curl`, and a live systemd user session.
2. `sudo apt-get install libportaudio2 libasound2 ffmpeg`. The binary is
   prebuilt, so the `-dev` package is not needed. `ffmpeg` renders the mp3
   previews the UI streams.
3. Runs `bin/hindsight --version` once. If the release binary cannot load on
   this machine — a glibc mismatch is the way that happens — it says so here,
   before anything has been changed, rather than as an unexplained "service
   did not come up" at the end.
4. Offers to migrate an existing `audio-dashcam` install, if it finds one.
5. Copies the binary to `~/hindsight/bin` and the UI to `~/hindsight/web`.
   The UI is staged and swapped, so a failed copy never leaves you with no
   `web/static`.
6. Writes `~/hindsight/hindsight.env` from the example — **and leaves an
   existing one alone**, so re-running it is how you upgrade.
7. Installs and restarts `~/.config/systemd/user/hindsight.service`.
8. `sudo loginctl enable-linger` so the service survives logout. Without this
   it dies the moment you close the SSH session. If that fails it warns and
   keeps going rather than failing the install.
9. Polls `http://127.0.0.1:5000/api/status` for about 15 seconds and reports
   whether capture came up healthy. If it never answers, it prints the last 30
   journal lines and exits non-zero.

That last port is **hardcoded**. If `PORT` is set in `hindsight.env`, the poll
asks 5000, gets nothing, and reports "service did not come up" for a service
that is running perfectly on your port. That happens on *every* run of the
installer, upgrades included — the poll never reads your `hindsight.env`. Check
`systemctl --user status hindsight.service`, or curl your own port, before
believing it.

### Where things end up

```
~/hindsight/bin/hindsight                     the binary
~/hindsight/web/static/                       the UI
~/hindsight/jam_saves/                        takes and their sidecars
~/hindsight/hindsight.env                     your configuration
~/.config/systemd/user/hindsight.service      the unit
```

`jam_saves` keeps its name from before the rename; so does `/api/jams`.
Renaming the endpoint would break installed PWAs, and the directory follows it.

## Plugging in the interface

With the interface powered and connected:

```bash
lsusb                    # is it on the bus at all
arecord -l               # does ALSA see a capture device
cat /proc/asound/cards   # the same entry Hindsight's MIDI discovery scans
```

`arecord` comes from `alsa-utils`, which the installer does **not** pull in —
Hindsight itself does not need it. `sudo apt install alsa-utils` if the command
is missing. (`lsusb` is from `usbutils`, likewise.)

`DEVICE_MATCH` is a substring match against the **PortAudio** device name, and
`/proc/asound/cards` is what MIDI discovery searches. The two usually agree on
the model string; the bracketed short id in `/proc/asound/cards` is truncated
to 15 characters by ALSA and often is not the model name at all, which is why
discovery searches the whole entry.

If capture does not come up, the log will say why:

```bash
systemctl --user status hindsight.service
```

Hindsight retries on its own with a growing backoff, so plugging the interface
in later is enough — there is nothing to restart.

## Finding the right `SAVE_CHANNELS`

An interface can present eight inputs while the mix you want is on exactly one
pair, and the wrong pair does not sound broken. It sounds like a take with the
mixer missing. Read "`SAVE_CHANNELS` and the pre-fader trap" in
[configuration.md](configuration.md) before you decide.

**From the UI.** Open Hindsight, expand **Input channels**, and play something.
The pair that moves with the master fader is the one you want. Move the fader
up and down: a pair that does not respond is a pre-fader tap, not the main mix.

**From the probe.** `scripts/channel-probe.py` measures it. It holds peaks
across the whole run, so quiet moments in real material do not read as a dead
channel. It only needs the HTTP API, so run it from anywhere:

```bash
python3 scripts/channel-probe.py <pi-host>:5000 --seconds 20
```

It prints a peak-dBFS bar per channel and a verdict per pair. Run it twice with
the source moved to a different physical input — the pair active in *both* runs
is the main mix.

Then set it and restart:

```bash
$EDITOR ~/hindsight/hindsight.env      # SAVE_CHANNELS=1,2
systemctl --user restart hindsight.service
```

## Operating it

```bash
systemctl --user status hindsight.service
systemctl --user restart hindsight.service
systemctl --user stop hindsight.service
```

The log is noisy on start-up: ALSA and JACK both print probe failures that mean
nothing here. Filter them out.

```bash
journalctl --user -u hindsight.service -f \
  | grep -viE "ALSA lib|jack server|JackShm|Cannot connect to server"
```

What you want to see:

```
[*] hindsight v2026.09.09.1 — device="EP-136" channels=8 ... ring=900s
[*] capture live on "EP-136 K.O. Sidekick" — ring 900s, 8 ch @ 48000 Hz
[*] listening on :5000
```

Then reach it at `http://<pi-host>:5000`.

## Optional: nginx on port 80

Useful so the address has no port in it, and required if you want
`tailscale serve` in front of it.

`/api/live` is a WebSocket, so the upgrade headers must be proxied. Without
them the page loads and the meters never move.

Write this as `/etc/nginx/sites-available/hindsight`, then symlink it into
`sites-enabled` **and remove the stock `default` site**. Debian ships that file
already claiming `default_server` on port 80, and two `default_server`
directives on the same address make `nginx -t` fail:

```bash
sudo rm /etc/nginx/sites-enabled/default
sudo ln -s /etc/nginx/sites-available/hindsight /etc/nginx/sites-enabled/
```

```nginx
map $http_upgrade $connection_upgrade {
    default upgrade;
    ''      close;
}

server {
    listen 80 default_server;

    # A full-ring download is hundreds of megabytes; streaming it straight
    # through avoids nginx spooling the whole thing to disk first.
    proxy_buffering off;

    # Nothing here uploads, so this only stops a stray 413 on a large PATCH.
    client_max_body_size 0;

    location / {
        proxy_pass http://127.0.0.1:5000;
        proxy_http_version 1.1;
        proxy_set_header Upgrade    $http_upgrade;
        proxy_set_header Connection $connection_upgrade;
        proxy_set_header Host       $host;
        proxy_read_timeout 3600s;
    }
}
```

`sudo nginx -t && sudo systemctl reload nginx`. Run `nginx -t` before the
reload — it is what catches both the `default_server` collision and a missing
`map` block.

## Optional: HTTPS with `tailscale serve`

Android will not offer a real PWA install over plain HTTP, and service workers
do not register in an insecure context at all. `tailscale serve` provisions and
renews a certificate for `<pi-host>.<tailnet>.ts.net` with no port forward and
no DNS challenge.

```bash
sudo tailscale serve --bg 80
tailscale serve status
```

That proxies TLS to the nginx above, which keeps the tested WebSocket path
untouched. Plain HTTP on the LAN keeps working exactly as before.

**Do not use `tailscale funnel`.** `serve` publishes to your tailnet; `funnel`
publishes to the public internet. This API has no authentication and no rate
limiting: `DELETE /api/delete` will destroy takes and `POST /api/trigger` will
write hundreds of megabytes for anyone who can reach it.

HTTPS also switches on the screen wake lock, which keeps a docked, charging
tablet lit while the app is visible.

## Migrating from `audio-dashcam`

If the installer finds an `audio-dashcam.service` user unit it offers to take
over, and this is what it does:

- Stops and disables `audio-dashcam.service`. The old service holds the USB
  audio interface, and two processes fighting over it means Hindsight crash-
  loops. If you decline the migration while it is still running, the installer
  refuses to continue rather than installing something that cannot start.
- Copies takes from `~/audio-dashcam/jam_saves` into `~/hindsight/jam_saves`,
  file by file, skipping any name that already exists and preserving mtimes —
  the take list derives each take's date from the file's mtime.
- **Leaves your originals in place.** Nothing is deleted. Delete
  `~/audio-dashcam` yourself once you are satisfied.

Your old `dashcam.env` is not migrated; the variable names are unchanged, so
copy across whatever you had customised.

## Deploying from a checkout instead

If you are working on the code rather than installing a release, `./deploy.sh`
rsyncs the tree, builds on the Pi and restarts the service. See "Deploying to a
Pi" in [development.md](development.md).
