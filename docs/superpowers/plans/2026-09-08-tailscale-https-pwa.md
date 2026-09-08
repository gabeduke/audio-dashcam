# Tailscale HTTPS and Android PWA Install Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Put the dashcam on a trusted HTTPS name via Tailscale so the service worker registers and the app installs as a real PWA on Android, without a port-forward, cert-manager or DNS01 challenge.

**Architecture:** Install Tailscale on the Pi and join the existing tailnet. `tailscale serve` terminates TLS on `<pi-host>.<tailnet>.ts.net` using an automatically provisioned cert and reverse-proxies to the nginx already listening on port 80, which keeps the tested WebSocket upgrade path untouched. LAN access over plain HTTP keeps working exactly as it does today.

**Tech Stack:** Tailscale (tailnet `<tailnet>.ts.net`), nginx, Debian 13 trixie arm64, Web App Manifest, service worker.

---

## Facts established before planning

Checked on 2026-09-08 from the dev Mac and the Pi:

| | |
|---|---|
| Tailnet | `<tailnet>.ts.net` |
| Devices | **9 of 100** on the free tier |
| Users | **3 of 3** — `gabeduke@`, `zepterfd@`, `john.k.duke@` |
| Dev Mac | `<dev-mac>.<tailnet>.ts.net`, Tailscale 1.78.1 (1.102.3 available) |
| Android target | `<android-phone>`, already on the tailnet under `gabeduke@` |
| Pi | hostname `<pi-host>`, Debian 13 trixie, aarch64, **Tailscale not installed** |
| Pi web stack | nginx on `:80` → `127.0.0.1:5000`, WebSocket upgrade map present, `nginx -t` passes |
| Pi sudo | **passwordless sudo works** (`sudo -n nginx -t` succeeded) |

**The free tier is not a blocker.** Adding the Pi is device #10 and adds no
user, because it joins under `gabeduke@`. Note that the *user* count is already
at its cap: this tailnet can add machines but not people without a paid plan.

**The earlier note that the Tailscale CLI was missing from the dev Mac was
wrong** — it is at `/Applications/Tailscale.app/Contents/MacOS/Tailscale`.

### Why `serve` and not `funnel`

`tailscale serve` publishes to the tailnet only. `tailscale funnel` publishes to
the public internet. The dashcam's API has **no authentication or rate limiting
at all** — `handleDelete` will destroy takes and `handleTrigger` will write
multi-hundred-megabyte files for anyone who can reach it. Funnel is therefore
out of the question here. Do not use it, and do not suggest it as a fallback if
serve gives trouble.

---

## File Structure

| File | Responsibility |
|---|---|
| `v2-go/static/manifest.json` (modify) | Add an explicit `id` and `screenshots` so Chrome shows the rich install dialog rather than the minimal infobar. |
| `v2-go/static/index.html` (modify) | Correct the comment that claims this device has no TLS. |
| `v2-go/static/icons/screenshot-narrow.png` (create) | 390×844 phone screenshot for the install dialog. |
| `v2-go/static/icons/screenshot-wide.png` (create) | 1024×768 landscape screenshot for the install dialog. |
| `STATE.md` (modify) | Record the resulting URL and the admin-console steps taken. |

No Go changes. `app.js` already guards service-worker registration on
`window.isSecureContext`, and `live.js` already picks `wss:` when
`location.protocol === 'https:'`, so the client needs no code change to take
advantage of TLS.

---

### Task 1: Install Tailscale on the Pi and join the tailnet

**Files:** none — Pi configuration.

**This task needs the owner.** `tailscale up` prints a URL that a human must
open and approve in a browser. Do not attempt to work around this.

- [ ] **Step 1: Install**

```bash
ssh "$DASHCAM_HOST" 'curl -fsSL https://tailscale.com/install.sh | sh'
```
Expected: the script detects Debian 13 (trixie) arm64, adds Tailscale's apt
repo, installs `tailscale` and `tailscaled`, and enables the daemon. It ends
with an instruction to run `tailscale up`.

- [ ] **Step 2: Confirm the daemon is running**

```bash
ssh "$DASHCAM_HOST" 'systemctl is-active tailscaled && tailscale version'
```
Expected: `active`, then a version of 1.80 or newer.

- [ ] **Step 3: Bring the node up — owner action**

```bash
ssh "$DASHCAM_HOST" 'sudo tailscale up --hostname=<pi-host> --accept-routes=false'
```
Expected: it prints
`To authenticate, visit: https://login.tailscale.com/a/<code>`.

**Hand that URL to the owner.** They open it, sign in as `gabeduke@`, and
approve the machine. The command returns once approval lands.

`--accept-routes=false` is explicit because the Pi should not pull subnet
routes from other nodes; it is a leaf appliance, not a router.

- [ ] **Step 4: Confirm the node joined and note its name**

```bash
ssh "$DASHCAM_HOST" 'tailscale status --json' | python3 -c "import json,sys; d=json.load(sys.stdin); print('DNSName:', d['Self']['DNSName']); print('TailscaleIPs:', d['Self']['TailscaleIPs'])"
```
Expected: `DNSName: <pi-host>.<tailnet>.ts.net.` and a `100.x.y.z` address.

If the tailnet has device approval enabled, the node will show as needing
approval — the owner approves it at
`https://login.tailscale.com/admin/machines`.

- [ ] **Step 5: Confirm it is reachable from the Mac**

```bash
/Applications/Tailscale.app/Contents/MacOS/Tailscale ping <pi-host>
```
Expected: `pong from <pi-host> (100.x.y.z) via ...`

- [ ] **Step 6: Commit the note**

```bash
cd /Users/gabeduke/projects/audio-dashcam
git commit --allow-empty -m "Join the Pi to the tailnet as <pi-host>.<tailnet>.ts.net"
```

---

### Task 2: Enable HTTPS certificates for the tailnet

**Files:** none — Tailscale admin console. **Owner action, one time, tailnet-wide.**

`tailscale serve` cannot provision a TLS certificate until HTTPS is enabled for
the whole tailnet. This is a single toggle and it affects every node, not just
this one.

- [ ] **Step 1: Owner enables it**

Ask the owner to open `https://login.tailscale.com/admin/dns`, find
**HTTPS Certificates**, and click **Enable HTTPS**.

- [ ] **Step 2: Verify a certificate can actually be issued**

```bash
ssh "$DASHCAM_HOST" 'sudo tailscale cert <pi-host>.<tailnet>.ts.net'
```
Expected: it writes `<pi-host>.<tailnet>.ts.net.crt` and `.key` into the
working directory and prints their paths.

If it fails with `HTTPS must be enabled in the admin panel`, step 1 has not
taken effect yet — wait a minute and retry rather than changing anything else.

This step is purely a check. `tailscale serve` provisions and renews its own
cert; these files are not used by anything.

- [ ] **Step 3: Clean up the test cert files**

```bash
ssh "$DASHCAM_HOST" 'rm -f ~/<pi-host>.<tailnet>.ts.net.crt ~/<pi-host>.<tailnet>.ts.net.key'
```

---

### Task 3: Put the app behind `tailscale serve`

**Files:** none — Pi configuration.

- [ ] **Step 1: Start serving**

```bash
ssh "$DASHCAM_HOST" 'sudo tailscale serve --bg 80'
```
Expected: it reports that `https://<pi-host>.<tailnet>.ts.net/` is now
proxying to `http://127.0.0.1:80`.

`--bg` runs it as persistent background configuration rather than a foreground
process, and the configuration is stored in tailscaled's state so it survives a
reboot.

Proxying to **port 80 (nginx)** rather than straight to the Go server on 5000
is deliberate: nginx already carries the WebSocket upgrade configuration that
`/api/live` needs, and it is the path that has been running in production.
Pointing serve at 5000 would bypass tested configuration for no benefit.

- [ ] **Step 2: Confirm the serve configuration**

```bash
ssh "$DASHCAM_HOST" 'tailscale serve status'
```
Expected:
```
https://<pi-host>.<tailnet>.ts.net (tailnet only)
|-- / proxy http://127.0.0.1:80
```

- [ ] **Step 3: Fetch it over HTTPS from the Mac**

```bash
curl -sSI https://<pi-host>.<tailnet>.ts.net/ | head -5
curl -sS https://<pi-host>.<tailnet>.ts.net/api/status | python3 -m json.tool | head -8
```
Expected: `HTTP/2 200` with no certificate warning (no `-k` needed — the cert
is publicly trusted), and a valid status JSON body.

If curl reports a TLS error, the cert has not provisioned yet; wait 30 seconds
and retry before changing anything.

- [ ] **Step 4: Confirm the WebSocket upgrade survives the extra hop**

```bash
curl -sS -i -N --max-time 5 \
  -H "Connection: Upgrade" -H "Upgrade: websocket" \
  -H "Sec-WebSocket-Version: 13" -H "Sec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==" \
  https://<pi-host>.<tailnet>.ts.net/api/live | head -6
```
Expected: `HTTP/1.1 101 Switching Protocols` and a `Sec-WebSocket-Accept`
header.

**This is the step most likely to reveal a problem**, because it is the only
part of the stack with three hops (tailscale serve → nginx → Go). If it returns
`400` or `200` instead of `101`, stop and diagnose before continuing — the live
meters depend entirely on it.

- [ ] **Step 5: Confirm LAN access still works unchanged**

```bash
curl -sS http://${DASHCAM_HOST#*@}/api/status | python3 -m json.tool | grep capture_healthy
```
Expected: unchanged behaviour. Serve adds a front door; it does not replace the
LAN one.

- [ ] **Step 6: Confirm it survives a reboot**

```bash
ssh "$DASHCAM_HOST" 'sudo reboot'
# wait ~45 seconds
sleep 45
curl -sSI https://<pi-host>.<tailnet>.ts.net/ | head -3
```
Expected: `HTTP/2 200` again with no manual intervention.

- [ ] **Step 7: Commit the note**

```bash
cd /Users/gabeduke/projects/audio-dashcam
git commit --allow-empty -m "Serve the dashcam over HTTPS at <pi-host>.<tailnet>.ts.net

tailscale serve --bg 80, proxying to the existing nginx so the tested
WebSocket upgrade path is unchanged. Tailnet only, never funnel: the API has
no auth and handleDelete destroys takes."
```

---

### Task 4: Confirm the service worker now registers

**Files:** none — verification.

`app.js` registers `sw.js` only when `window.isSecureContext` is true, which has
never been the case on plain HTTP over the LAN. Nothing should need changing;
this task proves it.

- [ ] **Step 1: Load the HTTPS URL in a browser and check the console**

Open `https://<pi-host>.<tailnet>.ts.net/` in Chrome on the Mac (the Mac is on
the tailnet, so the name resolves). In DevTools → Application → Service Workers,
expect a worker for scope `https://<pi-host>.<tailnet>.ts.net/` in state
**activated and running**.

- [ ] **Step 2: Confirm the shell got cached**

DevTools → Application → Cache Storage → `dashcam-shell-v1`. Expect the ten
entries listed in `sw.js`: `/`, `/index.html`, `/styles.css`, `/app.js`,
`/lib/live.js`, `/lib/meter.js`, `/lib/takes.js`, `/vendor/wavesurfer.esm.js`,
`/manifest.json`, `/icons/icon-192.png`.

- [ ] **Step 3: Confirm the live meters work over wss**

With the interface powered on, watch the live waveform on the HTTPS URL for 30
seconds. DevTools → Network → WS should show `/api/live` with status 101 and
frames arriving. This re-confirms Task 3 Step 4 from the client side.

---

### Task 5: Manifest polish for a proper Android install

**Files:**
- Modify: `v2-go/static/manifest.json`
- Create: `v2-go/static/icons/screenshot-narrow.png`
- Create: `v2-go/static/icons/screenshot-wide.png`

Chrome on Android already has everything it strictly requires — HTTPS, a
manifest with `name`/`short_name`/`start_url`/`display: standalone`, 192px and
512px icons (all verified present and correctly sized), and a service worker
with a fetch handler. Without `screenshots`, though, Chrome shows the cramped
mini-infobar instead of the rich install dialog.

- [ ] **Step 1: Capture the screenshots**

Best done *after* the tablet layout work
(`docs/superpowers/plans/2026-09-08-tablet-landscape-layout.md`) so the wide
shot shows the two-column layout. With the interface powered on so the meters
have signal:

```bash
cd /Users/gabeduke/projects/audio-dashcam
npx --yes playwright screenshot --viewport-size=390,844 \
  https://<pi-host>.<tailnet>.ts.net/ v2-go/static/icons/screenshot-narrow.png
npx --yes playwright screenshot --viewport-size=1024,768 \
  https://<pi-host>.<tailnet>.ts.net/ v2-go/static/icons/screenshot-wide.png
```

- [ ] **Step 2: Verify the dimensions match what the manifest will claim**

```bash
cd /Users/gabeduke/projects/audio-dashcam/v2-go/static/icons
sips -g pixelWidth -g pixelHeight screenshot-narrow.png screenshot-wide.png
```
Expected: 390×844 and 1024×768. Chrome ignores `screenshots` entries whose
declared `sizes` do not match the actual file, silently, so this check matters.

- [ ] **Step 3: Update the manifest**

Replace `v2-go/static/manifest.json` entirely with:

```json
{
  "id": "/",
  "name": "Audio Dashcam",
  "short_name": "Dashcam",
  "description": "Always-listening audio buffer for catching the jam you just played.",
  "start_url": "/",
  "scope": "/",
  "display": "standalone",
  "orientation": "any",
  "background_color": "#0b1120",
  "theme_color": "#0b1120",
  "icons": [
    { "src": "/icons/icon-192.png", "sizes": "192x192", "type": "image/png" },
    { "src": "/icons/icon-512.png", "sizes": "512x512", "type": "image/png" },
    { "src": "/icons/icon-maskable-512.png", "sizes": "512x512", "type": "image/png", "purpose": "maskable" }
  ],
  "screenshots": [
    { "src": "/icons/screenshot-narrow.png", "sizes": "390x844", "type": "image/png", "form_factor": "narrow" },
    { "src": "/icons/screenshot-wide.png", "sizes": "1024x768", "type": "image/png", "form_factor": "wide" }
  ]
}
```

`"id": "/"` is the value Chrome already derives from `start_url`, so stating it
changes nothing today but pins the app's identity if `start_url` ever moves.
Setting it now, before anyone has installed the app, is free; setting it after
an install would create a second app entry.

- [ ] **Step 4: Correct the stale comment in index.html**

In `v2-go/static/index.html`, replace:

```html
<!-- iOS standalone launch works over plain HTTP; Android needs TLS for a
     full install, which this LAN device does not have. -->
```

with:

```html
<!-- Android needs TLS for a full install. That comes from `tailscale serve`
     on https://<pi-host>.<tailnet>.ts.net; the plain-HTTP LAN address still
     works but will not install, and app.js will not register the service
     worker there because isSecureContext is false. -->
```

- [ ] **Step 5: Deploy the static files**

Static assets are served with `http.FileServer(http.Dir(...))`, so no rebuild
and no restart is needed:

```bash
cd /Users/gabeduke/projects/audio-dashcam
rsync -az --delete ./v2-go/static/ "$DASHCAM_HOST":~/audio-dashcam/v2-go/static/
```

- [ ] **Step 6: Verify the manifest parses on the device**

```bash
curl -sS https://<pi-host>.<tailnet>.ts.net/manifest.json | python3 -m json.tool
curl -sSI https://<pi-host>.<tailnet>.ts.net/icons/screenshot-wide.png | head -3
```
Expected: valid JSON with the `screenshots` array, and `HTTP/2 200` with
`content-type: image/png` for the screenshot.

- [ ] **Step 7: Commit**

```bash
cd /Users/gabeduke/projects/audio-dashcam
git add v2-go/static/manifest.json v2-go/static/index.html v2-go/static/icons/screenshot-narrow.png v2-go/static/icons/screenshot-wide.png
git commit -m "Add manifest id and screenshots for the Android install dialog

Everything Chrome requires for installability was already present; without
screenshots it falls back to the mini-infobar instead of the rich dialog."
```

---

### Task 6: Install it on the phone

**Files:** none — owner action on `<android-phone>`.

- [ ] **Step 1: Confirm the phone is on the tailnet**

The device `<android-phone>` is already registered under `gabeduke@`. Ask the owner to
open the Tailscale app and confirm it is connected.

- [ ] **Step 2: Open the HTTPS URL in Chrome on the phone**

`https://<pi-host>.<tailnet>.ts.net/`

Expect a padlock and no certificate warning.

- [ ] **Step 3: Install**

Chrome menu → **Install app** (or the install prompt). Expect the rich dialog
showing the app name, icon and the screenshots from Task 5.

- [ ] **Step 4: Verify it behaves as an installed app**

- Launches from the home screen with no browser chrome (`display: standalone`)
- The icon is the maskable one, correctly shaped for the launcher
- The status bar picks up `theme_color` `#0b1120`
- Live meters run — this confirms `wss:` works from the phone
- Capture writes a take

- [ ] **Step 5: Check offline behaviour is honest**

Turn off the phone's Tailscale connection and reopen the app. Expect the cached
shell to render and the top bar to read **server unreachable**, with the capture
button disabled. There is deliberately nothing useful to serve offline — the
audio lives on the Pi.

- [ ] **Step 6: Record the outcome in STATE.md**

Update the Infrastructure checklist:

```markdown
### Infrastructure
- [x] Set up `tailscale serve` on the Pi; confirm the PWA installs on Android —
      done. https://<pi-host>.<tailnet>.ts.net proxies to nginx:80. Installed
      and verified on `<android-phone>`.
- [x] Verify tailnet device count against the free-tier limit — 10/100 devices,
      3/3 users after adding the Pi.
- [x] Enable HTTPS for the tailnet in the admin console.
```

- [ ] **Step 7: Commit**

```bash
cd /Users/gabeduke/projects/audio-dashcam
git add STATE.md
git commit -m "STATE: HTTPS via Tailscale, PWA installed on Android"
```

---

## Things that will bite, and what to do about them

- **Custom tailnet ACLs.** The default policy allows all tailnet traffic. If
  this tailnet has a hand-written ACL, port 443 to `<pi-host>` must be permitted
  or the phone will time out. Check `https://login.tailscale.com/admin/acls`
  before assuming serve is broken.
- **Key expiry.** Tailscale node keys expire (180 days by default) and the node
  drops off the tailnet when they do. For an appliance that nobody logs into,
  disable key expiry for `<pi-host>` at
  `https://login.tailscale.com/admin/machines`. Worth doing at the same time as
  Task 1 Step 4 while the machines page is already open.
- **MagicDNS off.** If MagicDNS is disabled for the tailnet, the `.ts.net` name
  will not resolve and only the `100.x` address will work — but a bare IP gets
  no certificate, so serve would be pointless. MagicDNS is on today (the Mac
  reports `MagicDNSSuffix: <tailnet>.ts.net`), so this is a
  do-not-turn-it-off note rather than a step.
- **The dev Mac's Tailscale is 1.78.1 with 1.102.3 available.** Not a blocker
  for any of this, but it is the client that will be used to verify, so if
  something behaves oddly on the Mac and correctly on the phone, the version
  gap is the first thing to suspect.

---

## Self-review notes

- Spec coverage: HTTPS (Tasks 1–3), service worker activation (Task 4),
  installability (Task 5), the actual install (Task 6).
- Placeholders: none. Every command is literal, including the tailnet name.
- Type consistency: the hostname `<pi-host>` and the full name
  `<pi-host>.<tailnet>.ts.net` are used identically throughout.
- Dependency noted: Task 5 Step 1's wide screenshot is best captured after the
  tablet layout plan lands, and this is called out in the step itself.
