// web/static/lib/wave/geometry.test.js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  frameToX, xToFrame, levelFor, tileSpan, tilesFor, fileLevel,
  gridLines, barBeat, fmtTime, clampRegion, TILE_BUCKETS,
} from './geometry.js';

const view = { start: 48000, fpp: 100, width: 390 };

test('frame and pixel round-trip', () => {
  assert.equal(frameToX(48000, view), 0);
  assert.equal(frameToX(48000 + 39000, view), 390);
  assert.equal(xToFrame(195, view), 48000 + 19500);
  assert.equal(xToFrame(frameToX(50123, view), view), 50123);
});

test('level picks at most two buckets per device pixel', () => {
  assert.equal(levelFor(1, 1), 0);      // 1 frame/px: 2^0 >= 0.5
  assert.equal(levelFor(4, 1), 1);      // need 2^k >= 2
  assert.equal(levelFor(4, 3), 3);      // need 2^k >= 6 -> 8
  assert.equal(levelFor(1000, 2), 10);  // need >= 1000 -> 1024
});

test('tile span and indices with one tile margin, clamped', () => {
  assert.equal(tileSpan(0), TILE_BUCKETS);
  assert.equal(tileSpan(3), TILE_BUCKETS * 8);
  // level 2: 4096 frames per tile. View [48000, 87000).
  const t = tilesFor(view, 2, 1_000_000);
  assert.deepEqual(t, { first: Math.floor(48000 / 4096) - 1, last: Math.floor(86999 / 4096) + 1 });
  const edge = tilesFor({ start: 0, fpp: 10, width: 100 }, 0, 2000);
  assert.deepEqual(edge, { first: 0, last: 1 }); // 2000 frames = tiles 0..1, no tile -1 or 2
});

test('file level is where the saved peaks are exact', () => {
  assert.equal(fileLevel(1024), 0);
  assert.equal(fileLevel(1024 * 8), 3);
  assert.equal(fileLevel(1024 * 8 + 1), 4);
  assert.equal(fileLevel(48000 * 900), 16); // 15 min: 43.2M/1024 = 42187 -> 2^16
});

test('grid lines: bars and beats from the downbeat, hidden when dense', () => {
  const g = { bpm: 120, sampleRate: 48000, downbeat: 0 }; // 24000 frames per beat
  // 100 fpp: beat = 240px apart, all shown.
  const lines = gridLines({ start: 0, fpp: 100, width: 390 }, g);
  assert.deepEqual(lines.map((l) => [l.frame, l.bar]), [[0, true], [24000, false]]);
  // 4000 fpp: beats 6px apart -> hidden; bars 24px -> shown.
  const coarse = gridLines({ start: 0, fpp: 4000, width: 100 }, g);
  assert.ok(coarse.every((l) => l.bar));
  assert.equal(coarse.length, 5); // bars at 0, 96000, 192000, 288000, 384000 within 400000
  // 30000 fpp: bars 3.2px -> nothing.
  assert.deepEqual(gridLines({ start: 0, fpp: 30000, width: 100 }, g), []);
  // No bpm, no grid.
  assert.deepEqual(gridLines(view, { bpm: null, sampleRate: 48000, downbeat: 0 }), []);
  // Downbeat offset shifts everything; lines before the downbeat still draw.
  const shifted = gridLines({ start: 0, fpp: 100, width: 390 }, { ...g, downbeat: 10000 });
  assert.deepEqual(shifted.map((l) => l.frame), [10000, 34000]);
});

test('bar.beat readout', () => {
  const g = { bpm: 120, sampleRate: 48000, downbeat: 0 };
  assert.equal(barBeat(0, g), '1.1');
  assert.equal(barBeat(24000 * 5, g), '2.2');
  assert.equal(barBeat(24000 * 5 + 100, g), '2.2');
  assert.equal(barBeat(-1, g), '-1.4');
  assert.equal(barBeat(0, { ...g, bpm: null }), '');
});

test('time readout', () => {
  assert.equal(fmtTime(0, 48000), '0:00.000');
  assert.equal(fmtTime(48000 * 61.5, 48000), '1:01.500');
  assert.equal(fmtTime(47, 48000), '0:00.000'); // floors to ms
});

test('region clamp keeps order, bounds and minimum length', () => {
  assert.deepEqual(clampRegion({ start: -5, end: 100 }, 1000, 10), { start: 0, end: 100 });
  assert.deepEqual(clampRegion({ start: 900, end: 2000 }, 1000, 10), { start: 900, end: 1000 });
  assert.deepEqual(clampRegion({ start: 500, end: 503 }, 1000, 10), { start: 500, end: 510 });
  assert.deepEqual(clampRegion({ start: 995, end: 998 }, 1000, 10), { start: 990, end: 1000 });
});
