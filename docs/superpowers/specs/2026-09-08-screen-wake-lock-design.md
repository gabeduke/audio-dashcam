# Screen Wake Lock Design

**Date:** 2026-09-08
**Status:** approved, implementing

## Goal

Leave a Google Pixel Tablet docked on its charging speaker base and have the
dashcam up and awake, so the owner can sit down and hit Capture without waking
or unlocking anything.

## What was rejected, and why

**A Hub Mode screensaver.** Hub Mode engages when the tablet is *locked and
docked* and is built on Android's `DreamService`. Third-party Daydream-era apps
can be selected as the Hub Mode screensaver, but **a PWA cannot register a
`DreamService`**, so it would need a wrapper app — Fully Kiosk Browser is the
standard one, and it is paid.

More importantly it is the wrong shape: a screensaver is glanceable-only, and
tapping it lands on the lock screen rather than on Capture. The ask is
interactive. So: **install the PWA and launch it from the launcher**, and keep
it awake with the Screen Wake Lock API.

## Accepted limitation

**A wake lock cannot wake a sleeping device.** It only prevents a *visible*
page from sleeping. The docked flow therefore works only while the app is left
foregrounded. Bringing the app up from sleep would need a dock/charging
automation (Android routines, MacroDroid, Tasker), which is deliberately out of
scope here.

## Behaviour

Hold the screen awake when **all four** hold:

| input | source |
|---|---|
| `supported` | `'wakeLock' in navigator` |
| `secure` | `window.isSecureContext` |
| `visible` | `!document.hidden` |
| `charging` | `navigator.getBattery()` → `battery.charging` |

**Charging-only, automatic. No toggle, no `localStorage`, no new persistence.**
The docked tablet is always charging, so it is always awake; a phone on battery
behaves exactly as it does today. This is the minimum that fully solves the
stated problem.

**Secure context is a real gate, not a formality.** `wakeLock` requires it, so
this works on the tailnet HTTPS name and **not** on the plain-HTTP LAN address
— the identical constraint the service worker already has, and the same trap.

**Where `navigator.getBattery()` is unavailable** — it is Chromium-only;
Firefox removed it and Safari never shipped it — charging is unknowable, so the
lock never engages and behaviour is unchanged from today. The Pixel Tablet is
Chrome, so this is graceful degradation rather than a gap.

### The re-acquire path is the part that matters

The browser **releases the lock whenever the document becomes hidden, and never
re-acquires it**. Worse, `request()` *rejects* if called while hidden. So the
only correct trigger is `visibilitychange` → visible, re-evaluating the policy.
This is where wake lock implementations usually go wrong, and it is what the
verification below is aimed at.

Three listeners drive the state:

- `document` `visibilitychange` — an existing listener in `app.js` already
  handles this event for polling; the wake lock adds to it rather than
  duplicating it.
- the battery's `chargingchange`
- the sentinel's own `release` event, so the indicator follows a lock the
  browser dropped on its own

A guard prevents overlapping requests when visibility flaps rapidly.

## Structure

A new `v2-go/static/lib/wakelock.js`, following the existing `/lib/` module
pattern (`live.js`, `meter.js`, `takes.js`) rather than inlining into `app.js`:

- **`shouldHold({supported, secure, visible, charging})`** — a pure function
  returning a boolean. Separately exported so it can be asserted directly.
- **`initWakeLock({onChange})`** — owns the sentinel, the listeners and the
  re-acquire logic. Invokes `onChange(held)` on every transition.

`app.js` gains one call and one element reference. **No Go changes**; this is
entirely static assets, so it deploys with `./deploy.sh --static`.

## The indicator

A small **icon-only** marker in the topbar beside the existing `● recording`
status, present only while the lock is actually held. Icon-only, with the
accessible name on `aria-label` and a `title` for pointer users.

**Why icon-only:** `.health` has already caused one topbar overflow bug — it
was `white-space: nowrap` without `min-width: 0`, so as a flex item it refused
to shrink and painted over the logo at 390px. Adding an element there can
reintroduce that. A word would cost more width than a glyph, and a phone that
is charging *will* show this indicator at 390px. **The 390px overflow check is
re-run as part of verification, not assumed.**

Without the indicator the feature would be invisible when working and equally
invisible when silently failing — and the HTTP-vs-HTTPS mistake above is easy
to make. The marker makes both states legible at a glance.

## Verification

There is no JS test harness in this repo and none is being added. The
established pattern is a throwaway Playwright script in the session scratchpad,
which is how the tablet layout was checked and how a rename race was caught
during phase 1. Follow it.

`addInitScript` stubs `navigator.wakeLock` and `navigator.getBattery` with a
call log, then drives four transitions:

| transition | expected |
|---|---|
| visible + charging | `request('screen')` called once; indicator shown |
| unplug (`chargingchange` → false) | sentinel released; indicator hidden |
| document hidden | released, and **no `request()` while hidden** |
| visible again, still charging | `request()` called again; indicator shown |

Plus a re-run of the 390px topbar overflow check, and a confirming pass on the
docked tablet (Tailscale is now installed on it).

## Out of scope

- Any toggle, setting or persisted preference
- Dock/charging automation to launch the app
- Waking an already-sleeping device
- Any Go or server-side change
