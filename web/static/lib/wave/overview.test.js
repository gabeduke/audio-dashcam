import { test } from 'node:test';
import assert from 'node:assert/strict';
import { windowRect, stripXToFrame, dragToStart, OVERVIEW_MIN_WINDOW_PX } from './overview.js';

const TOTAL = 48000 * 900; // 15 minutes
const W = 390;

test('window rect maps the viewport onto the strip', () => {
  // viewport = first quarter of the take
  const view = { start: 0, fpp: TOTAL / 4 / W, width: W };
  assert.deepEqual(windowRect(view, TOTAL, W), { x: 0, w: 97.5 });
  const mid = { start: TOTAL / 2, fpp: TOTAL / 4 / W, width: W };
  assert.deepEqual(windowRect(mid, TOTAL, W), { x: 195, w: 97.5 });
});

test('window is never narrower than the minimum and never overflows the strip', () => {
  const tiny = { start: TOTAL - 4800, fpp: 1, width: W }; // 390 frames visible at the very end
  const r = windowRect(tiny, TOTAL, W);
  assert.equal(r.w, OVERVIEW_MIN_WINDOW_PX);
  assert.ok(r.x + r.w <= W);
  assert.ok(r.x >= 0);
});

test('strip x to frame', () => {
  assert.equal(stripXToFrame(0, TOTAL, W), 0);
  assert.equal(stripXToFrame(W, TOTAL, W), TOTAL);
  assert.equal(stripXToFrame(195, TOTAL, W), TOTAL / 2);
});

test('dragging the window by its grab offset pans and clamps', () => {
  const view = { start: 0, fpp: TOTAL / 4 / W, width: W }; // window x=0..97.5
  // grabbed 10px into the window, pointer now at x=110 -> window x = 100
  assert.equal(dragToStart(110, 10, view, TOTAL, W), Math.round((100 / W) * TOTAL));
  // dragged past the right edge: clamp so the viewport ends at the take's end
  assert.equal(dragToStart(1000, 10, view, TOTAL, W), TOTAL - view.width * view.fpp);
  // dragged past the left edge
  assert.equal(dragToStart(-50, 10, view, TOTAL, W), 0);
});
