# Tablet and Landscape Layout Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the UI use the width a tablet gives it — especially held sideways — by splitting into a sticky monitor column and a scrolling takes column, so Capture is never below the fold.

**Architecture:** Wrap the three existing panels into two column containers in the HTML. Mobile keeps today's single flex column, unchanged. Above a width breakpoint — or on any short landscape screen — `main` becomes a two-column grid with the monitor column sticky. All changes are HTML and CSS; no JavaScript and no Go.

**Tech Stack:** Hand-written CSS with media queries, no framework, no build step.

---

## Why this shape

The controlling insight is that **a landscape screen is short, not just wide**.
An iPad in landscape is 1024×768 CSS px; a phone sideways is about 844×390.
Today's single 780px column stacks live monitor → capture → takes, so on a short
screen the Capture button is pushed below the fold. On a device whose entire
purpose is "you just played something, grab it", making the user scroll to
reach Capture is the failure worth fixing. Width is the resource available to
fix it with.

So: monitor and Capture go in a **sticky left column**, takes get the right
column and scroll freely beside them.

This was previously deferred to land with the Phase 2 trim editor. It is being
pulled forward on its own at the owner's request.

**Deliberately not doing:** a separate tablet design. The requirement settled
during the take-triage brainstorm was "touch-first at 390px, tablet supported
via breakpoints, not a separate design", and that still holds. Tap targets stay
at the `--tap: 48px` they are now.

---

## File Structure

| File | Responsibility |
|---|---|
| `v2-go/static/index.html` (modify) | Wrap the panels in two column containers. No other change. |
| `v2-go/static/styles.css` (modify) | `--topbar-h` variable, `.col` base rule, and a rewritten "wider screens" section. |
| `deploy.sh` (modify) | A `--static` fast path, because static assets need no rebuild. |

Everything the JavaScript touches is addressed by `id`, and none of those move
or change, so `app.js`, `takes.js`, `meter.js` and `live.js` need no edits. The
`ResizeObserver` in `meter.js` already re-sizes the canvas when the column
width changes, and it already listens for `orientationchange`.

---

### Task 1: A `--static` deploy fast path

**Files:**
- Modify: `deploy.sh`

Do this first: every later task iterates on CSS, and a full `deploy.sh` rebuilds
the cgo binary on a Raspberry Pi for no reason.

- [x] **Step 1: Add the fast path**

In `deploy.sh`, immediately after the line
`DEST="${DASHCAM_DEST:-audio-dashcam}"`, insert:

```bash
# Static-only fast path. main.go serves the UI with
# http.FileServer(http.Dir(staticDir())), so HTML/CSS/JS changes are picked up
# from disk on the next request -- no rebuild, no restart. That turns the
# layout iteration loop from about a minute into about a second.
if [ "${1:-}" = "--static" ]; then
  echo "[*] syncing v2-go/static only to $HOST:~/$DEST"
  rsync -az --delete ./v2-go/static/ "$HOST:~/$DEST/v2-go/static/"
  echo "[*] done (no rebuild, no restart)"
  exit 0
fi
```

- [x] **Step 2: Verify it works and changes nothing else**

```bash
cd /Users/gabeduke/projects/audio-dashcam
./deploy.sh --static
curl -sS http://${DASHCAM_HOST#*@}/styles.css | head -3
```
Expected: the sync reports done in a second or two, and the CSS comes back.

- [x] **Step 3: Verify the full path still works**

```bash
./deploy.sh
```
Expected: unchanged behaviour — sync, build on the Pi, restart, status JSON.

- [x] **Step 4: Commit**

```bash
git add deploy.sh
git commit -m "Add deploy.sh --static for HTML/CSS/JS-only changes

The UI is served straight off disk with http.FileServer, so there is no
reason to rebuild a cgo binary on a Pi to change a stylesheet."
```

---

### Task 2: Wrap the panels in column containers

**Files:**
- Modify: `v2-go/static/index.html`

- [x] **Step 1: Restructure `<main>`**

Replace the entire `<main>` element in `v2-go/static/index.html` with:

```html
<main>

  <div class="col col-monitor">

    <section class="panel">
      <div class="viz-wrap" id="viz-wrap">
        <canvas id="viz"></canvas>
      </div>

      <div class="meters" id="meters"></div>

      <div class="stats">
        <div class="stat">
          <div class="k">buffered</div>
          <div class="v" id="stat-buffered">–</div>
        </div>
        <div class="stat">
          <div class="k">disk free</div>
          <div class="v" id="stat-disk">–</div>
        </div>
        <div class="stat">
          <div class="k">dropped</div>
          <div class="v" id="stat-xruns">0</div>
        </div>
      </div>

      <details class="chan" id="chan-diag">
        <summary>Input channels</summary>
        <div class="grid" id="chan-grid"></div>
        <p class="hint">
          Saving channels <b id="chan-saving">–</b> from <b id="chan-device">–</b>.
          Change with <code>SAVE_CHANNELS</code>.
        </p>
      </details>
    </section>

    <section class="panel">
      <h2>Capture</h2>
      <div class="seg" id="dur-seg" role="group" aria-label="Capture length"></div>
      <button class="capture-btn" id="capture-btn">Capture</button>
      <p class="last">last saved <span id="last-saved">none</span></p>
    </section>

  </div>

  <div class="col col-library">

    <section class="panel">
      <h2>Takes</h2>
      <div class="takes" id="takes"></div>
      <p class="empty" id="takes-empty">No takes yet — hit Capture.</p>
    </section>

  </div>

</main>
```

Every `id` is unchanged and in the same order, so no JavaScript is affected.

- [x] **Step 2: Add the `.col` base rule so mobile is unchanged**

In `v2-go/static/styles.css`, in the `/* ---------- layout ---------- */`
section, immediately after the `main { ... }` rule, add:

```css
/* On a phone the columns are just pass-through containers: main is still one
   flex column and the panels still stack in source order. They only become
   real columns at the breakpoints at the bottom of this file. */
.col {
  display: flex;
  flex-direction: column;
  gap: 14px;
  min-width: 0;
}
```

`min-width: 0` matters: as a grid item later, the default `min-width: auto`
would refuse to shrink below its content and reintroduce the horizontal
overflow that the top bar already had to be fixed for once.

- [x] **Step 3: Deploy and confirm nothing changed on mobile**

```bash
cd /Users/gabeduke/projects/audio-dashcam && ./deploy.sh --static
```

Open `http://${DASHCAM_HOST#*@}/` at 390×844 in a browser. Expect the layout to be
pixel-identical to before: three stacked panels, 14px gaps, no horizontal
scroll.

- [x] **Step 4: Commit**

```bash
git add v2-go/static/index.html v2-go/static/styles.css
git commit -m "Wrap the panels in column containers

No visual change: .col is a plain flex column, so main still stacks exactly as
it did. This is the structure the two-column landscape layout needs."
```

---

### Task 3: The two-column layout

**Files:**
- Modify: `v2-go/static/styles.css`

- [x] **Step 1: Add the top bar height variable**

In `styles.css`, inside `:root`, add after `--tap: 48px;`:

```css
  /* 10px top padding + 24px logo + 10px bottom padding + 1px border. The
     sticky monitor column parks just below it. */
  --topbar-h:  calc(45px + env(safe-area-inset-top));
```

- [x] **Step 2: Replace the whole "wider screens" section**

At the bottom of `styles.css`, replace this:

```css
/* ---------- wider screens ---------- */

@media (min-width: 620px) {
  main { padding: 18px 16px 48px; gap: 16px; }
  .panel { padding: 18px; }
  .viz-wrap { height: 172px; }
  .take-actions { gap: 8px; }
}

@media (min-width: 860px) {
  .viz-wrap { height: 200px; }
}
```

with this:

```css
/* ---------- wider screens ---------- */

@media (min-width: 620px) {
  main { padding: 18px 16px 48px; gap: 16px; }
  .col { gap: 16px; }
  .panel { padding: 18px; }
  .viz-wrap { height: 172px; }
  .take-actions { gap: 8px; }
}

/* Two columns once there is width to spare -- and also on a short landscape
   screen wide enough to take it, even a phone held sideways. Height is what a
   landscape screen is short of, and the thing worth protecting is the Capture
   button: you have just played something and want to hit it without hunting
   for it. */
@media (min-width: 860px),
       (orientation: landscape) and (min-width: 700px) and (max-height: 560px) {
  main {
    max-width: 1240px;
    display: grid;
    grid-template-columns: minmax(0, 5fr) minmax(0, 6fr);
    align-items: start;
  }

  /* Sticky inside its grid area, so the meters and Capture stay in place while
     a long takes list scrolls past them. Needs align-items:start above, or the
     item would stretch to the row height and have nowhere to slide. */
  .col-monitor {
    position: sticky;
    top: calc(var(--topbar-h) + 16px);
  }

  .viz-wrap { height: 160px; }
}

@media (min-width: 1200px) {
  main { max-width: 1320px; }
  .viz-wrap { height: 190px; }
  .wave { height: 60px; }
}

/* Short landscape: height is the scarce resource, so claw it back. This has to
   come last -- a 1024x600 tablet matches the width rule above as well, and the
   heights here are the ones that should win. */
@media (orientation: landscape) and (max-height: 560px) {
  main { padding-top: 10px; gap: 12px; }
  .col { gap: 12px; }
  .panel { padding: 12px; }
  .viz-wrap { height: 96px; }
  .capture-btn { min-height: 50px; margin-top: 10px; }
  .wave { height: 40px; }
  .last { margin: 8px 0 0; }
}
```

Note the old `min-width: 860px` rule that set `.viz-wrap` to 200px is gone on
purpose: at 860px the visualiser now lives in a ~390px column, where 200px tall
is out of proportion. 160px there, growing to 190px at 1200px, keeps roughly
the aspect it has on a phone.

- [x] **Step 3: Deploy**

```bash
cd /Users/gabeduke/projects/audio-dashcam && ./deploy.sh --static
```

- [x] **Step 4: Commit**

```bash
git add v2-go/static/styles.css
git commit -m "Two-column layout for tablets and landscape

Monitor and Capture go in a sticky left column, takes scroll on the right, so
Capture is reachable without scrolling on a short screen. Triggers on width
>=860px or on any landscape screen under 560px tall and at least 700px wide."
```

---

> **Executed 2026-09-08. Results, including one real failure the plan did not
> anticipate and two findings worth carrying forward:**
>
> **The 844×390 phone-landscape case failed on the first run** — Capture at
> bottom 499 against a 390px viewport. The `max-height: 560px` tier is tuned for
> a 1024×600 tablet and leaves the monitor column ~130px too tall on a phone
> held sideways, which is precisely the failure this layout exists to prevent.
> Fixed by adding a second, tighter tier at `max-height: 440px` that drops the
> input-channels disclosure, the "last saved" line and the visible panel heading
> (kept in the a11y tree via clip, not `display: none`), and takes the
> visualiser to 64px. The meters and stats row were deliberately kept: buffered
> seconds is the number you check *before* deciding to hit Capture.
>
> **The 2-column landscape layout needs ≥383px of height.** Every real target
> device clears it — Pixel 10 915×412, iPhone 15 852×393, 15 Pro Max 932×430,
> and tablets by a wide margin. A synthetic 740×360 does not, and was left
> failing rather than trading away the stats row for a size no target has.
> iPhone SE landscape (667×375) correctly stays 1-column, below the 700px trigger.
>
> **Pre-existing, not introduced here, not fixed here:** `.seg button` has
> `min-height: 40px`, so the duration selector's tap targets are 40px on *every*
> viewport including mobile portrait — below both the 44px floor the check
> asserts for stars and this project's own `--tap: 48px`. The plan's claim that
> "tap targets stay at the `--tap: 48px` they are now" is not accurate about the
> current state. Worth a separate one-line fix.
>
> Sticky was verified by padding the takes list in the DOM client-side (nothing
> written to the device): the monitor column pins at top 61px and holds through
> 2071px of scroll with Capture visible throughout. Canvas DPR sizing re-checked
> at four viewports and is correct.

### Task 4: Verify across the viewport matrix

**Files:**
- Create: session scratchpad only — `layout-check.mjs`, deliberately not committed

This repo has no JS test harness and none is being added; the established
pattern here is a throwaway Playwright script in the session scratchpad, which
is how defect 7 was found during the take-triage work. Follow it.

- [x] **Step 1: Write the check script**

Create `layout-check.mjs` in the session scratchpad directory:

```js
// Layout regression check. Not committed: this repo has no JS harness and this
// is a throwaway, matching how the take-triage frontend work was verified.
//
// Run: npx --yes playwright@latest install chromium && node layout-check.mjs
import { chromium } from 'playwright';

const URL = process.env.DASHCAM_URL || 'http://dashcam.local/';

const VIEWPORTS = [
  { name: 'phone portrait',   width: 390,  height: 844, cols: 1 },
  { name: 'phone landscape',  width: 844,  height: 390, cols: 2 },
  { name: 'ipad portrait',    width: 768,  height: 1024, cols: 1 },
  { name: 'ipad landscape',   width: 1024, height: 768, cols: 2 },
  { name: 'android tablet',   width: 1280, height: 800, cols: 2 },
  { name: 'desktop',          width: 1440, height: 900, cols: 2 },
];

const browser = await chromium.launch();
let failures = 0;

for (const vp of VIEWPORTS) {
  const page = await browser.newPage({ viewport: { width: vp.width, height: vp.height } });
  await page.goto(URL, { waitUntil: 'networkidle' });
  await page.waitForTimeout(600); // let the status poll build the meters

  const r = await page.evaluate(() => {
    const de = document.documentElement;
    const btn = document.getElementById('capture-btn').getBoundingClientRect();
    const main = getComputedStyle(document.querySelector('main'));
    const star = document.querySelector('.star');
    return {
      overflow: de.scrollWidth - de.clientWidth,
      captureBottom: btn.bottom,
      captureVisible: btn.bottom <= window.innerHeight && btn.top >= 0,
      display: main.display,
      columns: main.gridTemplateColumns,
      starSize: star ? Math.min(star.getBoundingClientRect().width,
                                star.getBoundingClientRect().height) : null,
    };
  });

  const wantCols = vp.cols === 2;
  const gotCols = r.display === 'grid';
  const problems = [];

  if (r.overflow > 0) problems.push(`horizontal overflow ${r.overflow}px`);
  if (gotCols !== wantCols) problems.push(`want ${vp.cols} column(s), got ${gotCols ? 2 : 1}`);
  if (wantCols && !r.captureVisible) problems.push(`Capture below the fold (bottom ${Math.round(r.captureBottom)} > ${vp.height})`);
  if (r.starSize !== null && r.starSize < 44) problems.push(`star tap target ${Math.round(r.starSize)}px < 44px`);

  if (problems.length) { failures++; console.log(`FAIL ${vp.name} ${vp.width}x${vp.height}: ${problems.join('; ')}`); }
  else console.log(`ok   ${vp.name} ${vp.width}x${vp.height}  ${gotCols ? '2col' : '1col'}`);

  await page.screenshot({ path: `shot-${vp.width}x${vp.height}.png`, fullPage: false });
  await page.close();
}

await browser.close();
console.log(failures ? `\n${failures} viewport(s) failed` : '\nall viewports pass');
process.exit(failures ? 1 : 0);
```

- [x] **Step 2: Run it**

```bash
cd "$SCRATCHPAD"   # the session scratchpad directory
npm init -y >/dev/null 2>&1
npm install playwright >/dev/null 2>&1
npx playwright install chromium
node layout-check.mjs
```

Expected:
```
ok   phone portrait 390x844  1col
ok   phone landscape 844x390  2col
ok   ipad portrait 768x1024  1col
ok   ipad landscape 1024x768  2col
ok   android tablet 1280x800  2col
ok   desktop 1440x900  2col

all viewports pass
```

- [x] **Step 3: Look at the screenshots**

The script writes `shot-<w>x<h>.png` for each viewport. Open
`shot-1024x768.png` and `shot-844x390.png` and check by eye:

- The takes column is beside the monitor column, not under it
- The visualiser is not squashed or stretched out of proportion
- Nothing overlaps the sticky column
- Text has not wrapped into something ugly in the narrower panels

Automated checks catch overflow and geometry; they do not catch ugly.

- [x] **Step 4: Check the sticky behaviour by hand**

At 1024×768, with enough takes to make the right column taller than the
viewport, scroll down. The monitor column should stay parked below the top bar
until its own bottom reaches the bottom of the grid row, then scroll away
normally. Capture should remain reachable throughout the takes list.

If there are not enough takes on the device to overflow, that is fine — note
it and re-check after the next few captures rather than manufacturing takes on
a real device.

- [x] **Step 5: Confirm the visualiser resized correctly**

The canvas is sized from a `ResizeObserver`, and a stale canvas size was one of
the original seven bugs. At 1024×768 check in DevTools that
`document.getElementById('viz').width` equals the CSS width times
`devicePixelRatio`, not the width it had at 390px.

- [ ] **Step 6: Commit the verification note**

```bash
cd /Users/gabeduke/projects/audio-dashcam
git commit --allow-empty -m "Verify the tablet layout across six viewports

390x844, 844x390, 768x1024, 1024x768, 1280x800, 1440x900: no horizontal
overflow, column count as intended, Capture above the fold in landscape, star
tap target still >=44px."
```

---

### Task 5 (optional): Two-up takes on very wide screens

**Files:**
- Modify: `v2-go/static/styles.css`

Only do this if Task 4's `shot-1440x900.png` shows the takes column looking
sparse. A take is a name, a waveform and three buttons; below roughly 320px
wide the waveform stops being readable, so two-up only earns its place when the
library column is wider than about 700px.

- [ ] **Step 1: Add the rule**

At the very end of `styles.css`, after the short-landscape block:

```css
/* Two-up takes only once each card still clears ~340px. Below that the
   waveform stops being worth drawing. */
@media (min-width: 1500px) {
  .takes {
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 10px;
  }
}
```

`.takes` is `display: flex; flex-direction: column` at every smaller size, and
`takes.js` reorders rows with `insertBefore`, which grid handles the same way
flex does — so the starred-first ordering and the mid-edit row-freeze logic are
unaffected.

- [ ] **Step 2: Verify at 1600×900**

Add `{ name: 'wide desktop', width: 1600, height: 900, cols: 2 }` to the
`VIEWPORTS` array in `layout-check.mjs` and re-run. Expect no overflow, and
check `shot-1600x900.png` shows two take cards per row with readable waveforms.

- [ ] **Step 3: Commit**

```bash
git add v2-go/static/styles.css
git commit -m "Two-up takes above 1500px"
```

---

## Interactions to keep in mind

- **`meter.js` sizes its canvas from a `ResizeObserver`** and also re-sizes 150ms
  after `orientationchange`. Moving the canvas into a narrower column is exactly
  the case that observer exists for, so no JS change is needed — but Task 4
  Step 5 verifies it rather than assuming.
- **`takes.js` freezes a row that is mid-rename** so a poll cannot move it and
  blur the input. That logic is about DOM order, not layout, and survives the
  flex-to-grid change in Task 5.
- **The top bar was recently fixed for overflow** with `min-width: 0` on
  `.health`. The `.col` rule adds the same guard for the new grid items, for the
  same reason.
- **`--tap: 48px` is untouched.** Wider screens do not mean smaller targets;
  this is still a device you poke at while holding an instrument.

---

## Self-review notes

- Spec coverage: two-up takes and a capped, centred content column were the
  three things the deferred STATE.md item asked for; the column cap is in Task
  3 (`max-width: 1240px`/`1320px`), two-up is Task 5, and side-by-side monitor
  and library is Task 3.
- Placeholders: none — every CSS block and HTML block is given in full.
- Type consistency: `.col`, `.col-monitor`, `.col-library` and `--topbar-h` are
  introduced in Task 2/3 and used under exactly those names in Tasks 3–5.
- The deferred note said extra width "helps drag resolution but does not
  replace the detail view" for the Phase 2 trim editor. Nothing here forecloses
  that: the trim editor will land inside the takes column and can still have its
  own overview-plus-detail waveform.
