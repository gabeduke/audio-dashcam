import { test } from 'node:test';
import assert from 'node:assert/strict';
import { WaveView } from './view.js';

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
  // The floor is 6px, not the full 24: there is room to drag-select nearby.
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
