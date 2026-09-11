import { test } from 'node:test';
import assert from 'node:assert/strict';
import { WaveView, HOLD_MS } from './view.js';

// hit() reads only this.view, this.chipRects and the state it is handed, so a
// stubbed instance tests it without a canvas.
function stubView(view) {
  const v = Object.create(WaveView.prototype);
  Object.assign(v, { view, chipRects: [], cssH: 200 });
  return v;
}

const noGrid = { bpm: null, downbeat: 0 };

test('a wide region keeps full-size handle zones', () => {
  const v = stubView({ start: 0, fpp: 1, width: 400 }); // 1 frame per px
  const st = { grid: noGrid, region: { start: 100, end: 300 }, flags: [] }; // 200px wide
  assert.deepEqual(v.hit(100, 100, st), { kind: 'handle', edge: 'start' });
  assert.deepEqual(v.hit(120, 100, st), { kind: 'handle', edge: 'start' }); // 20px out, inside HANDLE_HIT
  assert.deepEqual(v.hit(300, 100, st), { kind: 'handle', edge: 'end' });
  assert.deepEqual(v.hit(200, 100, st), { kind: 'region' });
  assert.deepEqual(v.hit(70, 100, st), { kind: 'wave' });
});

test('a region only a few pixels wide shrinks its handle zones', () => {
  // Zoomed out hard: a 400-frame region is 4px on screen.
  const v = stubView({ start: 0, fpp: 100, width: 400 });
  const st = { grid: noGrid, region: { start: 10000, end: 10400 }, flags: [] }; // x 100..104
  // The floor is 6px, not the full 24: there is room to press-and-hold nearby.
  assert.deepEqual(v.hit(106, 100, st), { kind: 'handle', edge: 'start' });
  assert.deepEqual(v.hit(115, 100, st), { kind: 'wave' });
  assert.deepEqual(v.hit(85, 100, st), { kind: 'wave' });
  // The edges themselves are still grabbable.
  assert.deepEqual(v.hit(100, 100, st), { kind: 'handle', edge: 'start' });
  assert.deepEqual(v.hit(104, 100, st), { kind: 'handle', edge: 'start' });
});

test('a middling region scales the grab to a quarter of its width', () => {
  const v = stubView({ start: 0, fpp: 1, width: 400 });
  const st = { grid: noGrid, region: { start: 100, end: 140 }, flags: [] }; // 40px < 2*HANDLE_HIT
  // grab = 40/4 = 10px
  assert.deepEqual(v.hit(110, 100, st), { kind: 'handle', edge: 'start' });
  assert.deepEqual(v.hit(89, 100, st), { kind: 'wave' });
  assert.deepEqual(v.hit(150, 100, st), { kind: 'handle', edge: 'end' });
  assert.deepEqual(v.hit(151, 100, st), { kind: 'wave' });
});

// up() drives the tap/double-tap machinery end to end, so these exercise it
// on a stubbed instance rather than hit() alone.
test('a double-tap inside the region adds a flag, same as bare waveform', () => {
  const v = stubView({ start: 0, fpp: 10, width: 390 }); // x=200 -> frame 2000
  v.total = 100000;
  v.minLen = 289;
  v.pointers = new Map();
  v.lastTap = null;
  v.getState = () => ({ region: { start: 1000, end: 3000 }, flags: [], grid: noGrid, cursor: 0 });
  const log = [];
  v.emit = (ev, p) => log.push([ev, p]);
  v.pt = () => ({ x: 200, y: 50 }); // inside the region

  v.gesture = { kind: 'moveRegion', region: { start: 1000, end: 3000 }, x0: 200, y0: 50, t0: performance.now(), moved: false };
  v.up({ pointerId: 1 });
  assert.deepEqual(log.at(-1), ['seek', { frame: 2000 }]);

  v.gesture = { kind: 'moveRegion', region: { start: 1000, end: 3000 }, x0: 200, y0: 50, t0: performance.now(), moved: false };
  v.up({ pointerId: 1 });
  assert.deepEqual(log.at(-1), ['addFlag', { frame: 2000 }]);
});

test('a tap on a handle neither seeks nor flags', () => {
  const v = stubView({ start: 0, fpp: 10, width: 390 });
  v.total = 100000;
  v.minLen = 289;
  v.pointers = new Map();
  v.lastTap = null;
  v.getState = () => ({ region: { start: 1000, end: 3000 }, flags: [], grid: noGrid, cursor: 0 });
  const log = [];
  v.emit = (ev, p) => log.push([ev, p]);
  v.pt = () => ({ x: 100, y: 50 });

  v.gesture = { kind: 'handle', edge: 'start', region: { start: 1000, end: 3000 }, grabOffset: 0, x0: 100, y0: 50, t0: performance.now(), moved: false };
  v.up({ pointerId: 1 });
  assert.deepEqual(log, []);
});

// A press that travelled past TAP_MOVE before the hold fired became a pan.
// Releasing it must do nothing at all: no region, and no stray seek that would
// yank the cursor to wherever the pan happened to end.
test('a press that panned emits nothing on release', () => {
  const v = stubView({ start: 0, fpp: 10, width: 390 }); // x=200 -> frame 2000
  v.total = 100000;
  v.minLen = 289;
  v.pointers = new Map();
  v.lastTap = null;
  v.getState = () => ({ region: null, flags: [], grid: noGrid, cursor: 0 });
  const log = [];
  v.emit = (ev, p) => log.push([ev, p]);
  v.pt = () => ({ x: 210, y: 50 }); // 10px from x0: past TAP_MOVE

  v.gesture = { kind: 'select', anchor: 2000, prev: null, x0: 200, y0: 50, t0: performance.now(), moved: true, selecting: false, start: 0 };
  v.up({ pointerId: 1 });
  assert.deepEqual(log, []);
});

// Once the hold has armed selecting, a release that snaps back below
// MIN_REGION_PX / minLen must roll back to prev and never emit a tap on top.
test('a held select released under the region minimum rolls back', () => {
  const v = stubView({ start: 0, fpp: 10, width: 390 }); // x=200 -> frame 2000
  v.total = 100000;
  v.minLen = 289;
  v.pointers = new Map();
  v.lastTap = null;
  v.getState = () => ({ region: null, flags: [], grid: noGrid, cursor: 0 });
  const log = [];
  v.emit = (ev, p) => log.push([ev, p]);
  v.pt = () => ({ x: 210, y: 50 }); // anchor 2000 -> release frame 2100: 10px, 100 frames

  v.gesture = { kind: 'select', anchor: 2000, prev: null, x0: 200, y0: 50, t0: performance.now(), moved: true, selecting: true };
  v.up({ pointerId: 1 });
  assert.deepEqual(log, [['regionChange', { region: null, final: true }]]);
});

// A held select dragged past the region minimum commits.
test('a held select past the region minimum commits', () => {
  const v = stubView({ start: 0, fpp: 10, width: 390 }); // x=200 -> frame 2000
  v.total = 100000;
  v.minLen = 289;
  v.pointers = new Map();
  v.lastTap = null;
  v.getState = () => ({ region: null, flags: [], grid: noGrid, cursor: 0 });
  const log = [];
  v.emit = (ev, p) => log.push([ev, p]);
  v.pt = () => ({ x: 240, y: 50 }); // anchor 2000 -> release frame 2400: 40px, 400 frames

  v.gesture = { kind: 'select', anchor: 2000, prev: null, x0: 200, y0: 50, t0: performance.now(), moved: true, selecting: true };
  v.up({ pointerId: 1 });
  assert.deepEqual(log, [['regionChange', { region: { start: 2000, end: 2400 }, final: true }]]);
});


// --- the hold-to-select gesture, driven end to end -------------------------
// These drive down/move/up with fake pointer events instead of planting a
// gesture, because the whole point of the change is *when* the gesture becomes
// a select: that lives in the hold timer, not in up().
function pointerView({ region = null, start = 5000 } = {}) {
  const v = Object.create(WaveView.prototype);
  const log = [];
  Object.assign(v, {
    view: { start, fpp: 10, width: 390 }, // x=200 -> frame start+2000
    chipRects: [], cssH: 200,
    total: 100000,
    minLen: 289,
    pointers: new Map(),
    gesture: null,
    lastTap: null,
    canvas: {
      setPointerCapture() {},
      getBoundingClientRect: () => ({ left: 0, top: 0, width: 390, height: 200 }),
    },
    getState: () => ({ region, flags: [], grid: noGrid, cursor: 0 }),
    emit: (ev, p) => log.push([ev, p]),
    draw() {},
  });
  // clampView and maxFpp stay real: the pan has to be clamped like the page's.
  v.changed = () => log.push(['viewChange', v.view.start]);
  return { v, log };
}
const at = (x, id = 1) => ({ pointerId: id, clientX: x, clientY: 50 });
const regions = (log) => log.filter(([ev]) => ev === 'regionChange');

test('press, hold, then drag selects from the press point', (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const { v, log } = pointerView();
  v.down(at(200));                    // anchor = 7000
  assert.deepEqual(regions(log), []); // nothing until the hold fires
  t.mock.timers.tick(HOLD_MS);
  // The hold announces itself with a minLen band under the finger.
  assert.deepEqual(log.at(-1), ['regionChange', { region: { start: 7000, end: 7289 }, final: false }]);
  v.move(at(240));
  assert.deepEqual(log.at(-1), ['regionChange', { region: { start: 7000, end: 7400 }, final: false }]);
  v.up(at(240));
  assert.deepEqual(log.at(-1), ['regionChange', { region: { start: 7000, end: 7400 }, final: true }]);
});

test('a drag before the hold fires pans instead of selecting', (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const { v, log } = pointerView();
  v.down(at(200));
  v.move(at(230)); // 30px right: content follows the finger, start goes back
  assert.equal(v.view.start, 5000 - 30 * 10);
  assert.deepEqual(regions(log), []);
  // The hold is dead, not merely late: ticking past it must not start a region.
  t.mock.timers.tick(HOLD_MS);
  assert.deepEqual(regions(log), []);
  v.up(at(230));
  assert.deepEqual(regions(log), []);
});

test('a second finger during a held select rolls the region back', (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const prev = { start: 1000, end: 3000 };
  const { v, log } = pointerView({ region: prev });
  v.down(at(200));
  t.mock.timers.tick(HOLD_MS);
  assert.equal(log.length, 1); // the provisional band
  v.down(at(300, 2));          // pinch takes over
  assert.deepEqual(log.at(-1), ['regionChange', { region: prev, final: true }]);
  assert.equal(v.gesture.kind, 'pinch');
  // Pinching moves the viewport, never the abandoned region.
  v.move(at(340, 2));
  assert.deepEqual(regions(log), [
    ['regionChange', { region: { start: 7000, end: 7289 }, final: false }],
    ['regionChange', { region: prev, final: true }],
  ]);
});

test('a press released before the hold still seeks', (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const { v, log } = pointerView();
  v.down(at(200));
  v.move(at(205)); // 5px: under TAP_MOVE, so still undecided
  v.up(at(205));
  assert.deepEqual(log, [['seek', { frame: 7050 }]]);
});

// A pan is never half of a double-tap: it must clear lastTap, or a later tap
// landing near the original spot within TAP_MS would wrongly pair up and add
// a flag instead of seeking.
test('a pan between two taps breaks the double-tap', (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const { v, log } = pointerView();
  // First tap: seeks and arms lastTap.
  v.down(at(200));
  v.up(at(200));
  assert.deepEqual(log.at(-1), ['seek', { frame: 7000 }]);
  // A 30px pan on the next press: it must clear lastTap, not leave it armed.
  v.down(at(200));
  v.move(at(230));
  v.up(at(230));
  // A tap back at the original x, still within TAP_MS: without the fix this
  // pairs with the first tap (same spot, well under DOUBLE_TAP_MOVE) and adds
  // a stray flag instead of seeking.
  v.down(at(200));
  v.up(at(200));
  assert.deepEqual(log.at(-1), ['seek', { frame: 6700 }]);
});

// The hold fires at HOLD_MS (350); a still press released between TAP_MS
// (300) and HOLD_MS never moved and never started selecting, so it must still
// be treated as a tap rather than falling into a dead zone.
test('a still press held past TAP_MS but under HOLD_MS still seeks', (t) => {
  let now = 1000;
  t.mock.method(performance, 'now', () => now);
  const { v, log } = pointerView();
  v.down(at(200));  // t0 = 1000
  now = 1000 + 320; // held 320ms: past TAP_MS, short of HOLD_MS
  v.up(at(200));
  assert.deepEqual(log, [['seek', { frame: 7000 }]]);
});
