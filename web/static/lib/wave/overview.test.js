import { test } from 'node:test';
import assert from 'node:assert/strict';
import { Overview, windowRect, stripXToFrame, dragToStart, OVERVIEW_MIN_WINDOW_PX } from './overview.js';

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

// The gesture plumbing is not a pure function, but it only touches the canvas
// through setPointerCapture and getBoundingClientRect, so a stubbed instance
// exercises down/move/up without a DOM.
function stubOverview(view) {
  const events = [];
  const ov = Object.create(Overview.prototype);
  Object.assign(ov, {
    canvas: { setPointerCapture() {}, getBoundingClientRect: () => ({ left: 0, top: 0, width: W, height: 36 }) },
    cssW: W,
    total: TOTAL,
    gesture: null,
    lastTap: 0,
    getView: () => view,
    emit: (name, detail) => events.push({ name, detail }),
  });
  return { ov, events };
}

const ptr = (id, x) => ({ pointerId: id, clientX: x, clientY: 10 });

test('a second finger cannot steer or end the first finger\'s drag', () => {
  const view = { start: 0, fpp: TOTAL / 4 / W, width: W }; // window x=0..97.5
  const { ov, events } = stubOverview(view);
  ov.down(ptr(1, 10));                 // grab inside the window
  ov.move(ptr(2, 300));                // a second finger: ignored
  assert.deepEqual(events, []);
  ov.move(ptr(1, 110));                // the owning finger pans
  assert.equal(events.length, 1);
  assert.equal(events[0].name, 'panTo');
  ov.up(ptr(2, 300));                  // the second finger lifting must not end it
  assert.ok(ov.gesture, 'gesture survives the other pointer lifting');
  ov.move(ptr(1, 120));                // still steerable
  assert.equal(events.length, 2);
  ov.up(ptr(1, 120));                  // the owner ends it
  assert.equal(ov.gesture, null);
  assert.equal(events.length, 2, 'a drag that moved is not also a tap');
});

test('a stray pointerup outside any gesture is ignored', () => {
  const view = { start: 0, fpp: TOTAL / 4 / W, width: W };
  const { ov, events } = stubOverview(view);
  ov.up(ptr(7, 300));
  assert.deepEqual(events, []);
  assert.equal(ov.gesture, null);
});
