// web/static/lib/wave/tiles.test.js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { TileCache } from './tiles.js';

const SR = 48000;
const TOTAL = 1024 * 64; // 65536 frames: fileLevel = 6

function fakePeaks(from, buckets, value) {
  const data = [[], []];
  for (let i = 0; i < buckets; i++) { data[0].push(-value, value); data[1].push(-value / 2, value / 2); }
  return { version: 1, channels: 2, sample_rate: SR, duration: 0, buckets, from, data };
}

const filePeaks = fakePeaks(0, 1024, 0.1);

async function until(cond, ms = 2000) {
  const t0 = Date.now();
  while (!cond()) {
    if (Date.now() - t0 > ms) throw new Error('timed out waiting');
    await new Promise((r) => setTimeout(r, 2));
  }
}

function fakeFetch(log, { fail = () => false } = {}) {
  return async (url) => {
    log.push(url);
    const u = new URL(url, 'http://x');
    const from = Number(u.searchParams.get('from'));
    const to = Number(u.searchParams.get('to'));
    const buckets = Number(u.searchParams.get('buckets'));
    if (fail(url)) return { ok: false, status: 500, json: async () => ({}) };
    return { ok: true, status: 200, json: async () => fakePeaks(from, buckets, 0.9) };
  };
}

test('zoomed-out view uses the file peaks and fetches nothing', () => {
  const log = [];
  const tc = new TileCache({ file: 'a.wav', totalFrames: TOTAL, filePeaks, fetchFn: fakeFetch(log), onChange() {} });
  const r = tc.columns({ start: 0, fpp: TOTAL / 390, width: 390 }, 1);
  assert.equal(log.length, 0);
  assert.equal(r.cols.length, 390 * 4);
  assert.ok(Math.abs(r.cols[1] - 0.1) < 1e-6);
});

test('zoomed-in view requests the tiles it needs once and then draws them', async () => {
  const log = [];
  let changes = 0;
  const tc = new TileCache({ file: 'a.wav', totalFrames: TOTAL, filePeaks, fetchFn: fakeFetch(log), onChange() { changes++; } });
  const view = { start: 5000, fpp: 4, width: 390 }; // level 1 at dpr 1: tile span 2048
  const first = tc.columns(view, 1);
  assert.ok(Math.abs(first.cols[1] - 0.1) < 1e-6, 'falls back to file peaks while loading');
  // tiles 1..4 (view [5000, 6560) -> tiles 2,3 plus margins 1 and 4)
  assert.equal(log.length, 4);
  assert.ok(log[0].includes('from=2048&to=4096&buckets=1024'));
  await until(() => changes === 4);
  assert.equal(changes, 4);
  const second = tc.columns(view, 1);
  assert.equal(log.length, 4, 'no refetch');
  assert.ok(Math.abs(second.cols[1] - 0.9) < 1e-6, 'drawn from the tile now');
});

test('a failed tile keeps the fallback and retries with backoff', async () => {
  const log = [];
  let failing = true;
  const tc = new TileCache({ file: 'a.wav', totalFrames: TOTAL, filePeaks, fetchFn: fakeFetch(log, { fail: () => failing }), onChange() {} });
  tc.retryBase = 1; // ms, keep the test fast
  const view = { start: 0, fpp: 1, width: 100 }; // level 0, tile 0 only (+ margin 1)
  tc.columns(view, 1);
  await until(() => log.length >= 3);
  assert.ok(log.length >= 3, `retried: ${log.length}`);
  failing = false;
  await until(() => {
    const r = tc.columns(view, 1);
    return Math.abs(r.cols[1] - 0.9) < 1e-6;
  });
  const r = tc.columns(view, 1);
  assert.ok(Math.abs(r.cols[1] - 0.9) < 1e-6);
  tc.stop();
});

test('a 404 marks the take gone and stops fetching', async () => {
  const log = [];
  const fetchFn = async (url) => { log.push(url); return { ok: false, status: 404, json: async () => ({}) }; };
  const tc = new TileCache({ file: 'a.wav', totalFrames: TOTAL, filePeaks, fetchFn, onChange() {} });
  tc.columns({ start: 0, fpp: 1, width: 100 }, 1);
  await new Promise((r) => setTimeout(r, 0));
  assert.equal(tc.gone, true);
  const n = log.length;
  tc.columns({ start: 3000, fpp: 1, width: 100 }, 1);
  assert.equal(log.length, n);
});

test('evicts least recently drawn tiles beyond maxTiles', async () => {
  const log = [];
  const tc = new TileCache({ file: 'a.wav', totalFrames: TOTAL, filePeaks, fetchFn: fakeFetch(log), onChange() {}, maxTiles: 3 });
  for (let i = 0; i < 6; i++) tc.columns({ start: i * 1024, fpp: 1, width: 10 }, 1);
  await until(() => tc.cache.size > 0 && tc.inflight.size === 0);
  assert.ok(tc.cache.size <= 3, `cache size ${tc.cache.size}`);
});
