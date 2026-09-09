// Capture the README screenshots against a running demo instance.
//
//   RING_SECONDS=120 OUTPUT_DIR=/tmp/hindsight-shots PORT=15173 \
//     go run ./cmd/hindsight --demo &
//   npx --yes playwright@1.49.0 install chromium
//   HINDSIGHT_URL=http://127.0.0.1:15173 node scripts/screenshots.mjs
//
// Everything is localhost on purpose: this repo is public, and a screenshot
// of the real rig would put its hostname in the URL bar.
//
// Port 5000, the app's default, is occupied by macOS ControlCenter on this
// machine -- run the demo on another port via PORT and point this script at
// the same port via HINDSIGHT_URL. The app's own default is untouched.
import { chromium } from 'playwright';

const BASE = process.env.HINDSIGHT_URL ?? 'http://127.0.0.1:5000';

const VIEWPORTS = [
  { name: 'desktop', width: 1440, height: 900 },
  { name: 'tablet', width: 1024, height: 768 },
  { name: 'phone', width: 390, height: 844 },
];

// Long enough for the ring to hold several bars, so the ribbon and the take
// waveform show the loop's structure rather than a sliver.
const FILL_MS = 45_000;

const browser = await chromium.launch();

const warm = await browser.newPage();
await warm.goto(BASE);
console.log(`filling the ring for ${FILL_MS / 1000}s...`);
await warm.waitForTimeout(FILL_MS);

// One take, so the library panel is not empty.
const res = await warm.request.post(`${BASE}/api/trigger?seconds=30`);
if (!res.ok()) throw new Error(`capture failed: ${res.status()}`);

// Peaks land synchronously with the save; the mp3 preview is rendered by a
// background ffmpeg process afterwards, and the waveform only mounts once
// both sidecars exist (has_peaks && has_preview in takes.js). Poll the row
// itself rather than guessing a fixed delay.
await warm.waitForSelector('#takes .take .wave:not(.pending)', { timeout: 20_000 });
await warm.waitForTimeout(1000); // let the waveform canvas finish painting
await warm.close();

for (const vp of VIEWPORTS) {
  const page = await browser.newPage({
    viewport: { width: vp.width, height: vp.height },
    deviceScaleFactor: 2,
  });
  await page.goto(BASE);
  await page.waitForSelector('#takes .take .wave:not(.pending)', { timeout: 15_000 });
  await page.waitForTimeout(2500); // let the meters settle mid-swing
  await page.screenshot({ path: `docs/images/${vp.name}.png` });
  console.log(`docs/images/${vp.name}.png`);
  await page.close();
}

await browser.close();
