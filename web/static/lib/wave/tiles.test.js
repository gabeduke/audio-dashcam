// web/static/lib/wave/tiles.test.js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { TileCache } from './tiles.js';

const SR = 48000;
const TOTAL = 1024 * 64; // 65536 frames: fileLevel = 6

// Shaped like the server's PeakData: duration is (to-from)/sample_rate, which
// is how the client recovers the server's bucket size.
function fakePeaks(from, to, buckets, value) {
  const data = [[], []];
  for (let i = 0; i < buckets; i++) {
    const v = typeof value === 'function' ? value(i) : value;
    data[0].push(-v, v);
    data[1].push(-v / 2, v / 2);
  }
  return { version: 1, channels: 2, sample_rate: SR, duration: (to - from) / SR, buckets, from, data };
}

const filePeaks = fakePeaks(0, TOTAL, 1024, 0.1);

async function until(cond, ms = 2000) {
  const t0 = Date.now();
  while (!cond()) {
    if (Date.now() - t0 > ms) throw new Error('timed out waiting');
    await new Promise((r) => setTimeout(r, 2));
  }
}

function fakeFetch(log, { fail = () => false, value = () => 0.9 } = {}) {
  return async (url) => {
    log.push(url);
    const u = new URL(url, 'http://x');
    const from = Number(u.searchParams.get('from'));
    const to = Number(u.searchParams.get('to'));
    const buckets = Number(u.searchParams.get('buckets'));
    if (fail(url)) return { ok: false, status: 500, json: async () => ({}) };
    return { ok: true, status: 200, json: async () => fakePeaks(from, to, buckets, value(from, to, buckets)) };
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

test('a 404 marks the take gone, stops fetching, and reports it once', async () => {
  const log = [];
  const fetchFn = async (url) => { log.push(url); return { ok: false, status: 404, json: async () => ({}) }; };
  let gone = 0;
  const tc = new TileCache({ file: 'a.wav', totalFrames: TOTAL, filePeaks, fetchFn, onChange() {}, onGone() { gone++; } });
  tc.columns({ start: 0, fpp: 1, width: 100 }, 1);
  await until(() => tc.gone);
  assert.equal(tc.gone, true);
  const n = log.length;
  tc.columns({ start: 3000, fpp: 1, width: 100 }, 1);
  assert.equal(log.length, n);
  // Every tile of that first view 404s; the page hears about it exactly once.
  await new Promise((r) => setTimeout(r, 5));
  assert.equal(gone, 1);
});

test('the last tile of a ragged take maps frames to the server\'s buckets', async () => {
  // 64 whole tiles plus 700 frames. At level 1 the last tile spans only those
  // 700 frames, so the server's per is floor(700/1024) -> 1, not 2**1. Each
  // bucket's value encodes its own index, so a wrong bucket size shows up as
  // the wrong amplitude rather than as a subtle smear.
  const RAGGED = 1024 * 64 + 700;
  const bucketValue = (i) => (i + 1) / 2048;
  const log = [];
  const tc = new TileCache({
    file: 'a.wav', totalFrames: RAGGED, filePeaks,
    fetchFn: fakeFetch(log, { value: () => bucketValue }), onChange() {},
  });
  const view = { start: 66200, fpp: 4, width: 10 }; // level 1, inside the last tile
  tc.columns(view, 1);
  await until(() => tc.cache.has('1:32'));
  const r = tc.columns(view, 1);

  // x=0 covers frames [66200, 66204) of tile 32, which starts at 65536 and
  // has one frame per bucket: buckets 664..667.
  const want = bucketValue(667);
  assert.ok(Math.abs(r.cols[1] - want) < 1e-6, `max ${r.cols[1]} want ${want}`);
  assert.ok(Math.abs(r.cols[0] + want) < 1e-6, `min ${r.cols[0]}`);
  // The old bug read buckets 332..333 here -- the tile's own beginning.
  assert.ok(Math.abs(r.cols[1] - bucketValue(333)) > 1e-3, 'not the squeezed copy');

  // ...and one column further along stays in step.
  const want2 = bucketValue(671);
  assert.ok(Math.abs(r.cols[1 * 4 + 1] - want2) < 1e-6, `max ${r.cols[5]} want ${want2}`);
});

test('evicts least recently drawn tiles beyond maxTiles', async () => {
  const log = [];
  const tc = new TileCache({ file: 'a.wav', totalFrames: TOTAL, filePeaks, fetchFn: fakeFetch(log), onChange() {}, maxTiles: 3 });
  for (let i = 0; i < 6; i++) tc.columns({ start: i * 1024, fpp: 1, width: 10 }, 1);
  await until(() => tc.cache.size > 0 && tc.inflight.size === 0);
  assert.ok(tc.cache.size <= 3, `cache size ${tc.cache.size}`);
});

test('the default fetch is called with no receiver of ours', async () => {
  // Browsers reject fetch invoked as a method of anything but the window, so
  // the default must not be a bare reference passed around as this.fetchFn.
  const real = globalThis.fetch;
  const seen = [];
  globalThis.fetch = function (url) {
    seen.push(this);
    return Promise.resolve({ ok: true, status: 200, json: async () => fakePeaks(0, 1024, 1024, 0.9) });
  };
  try {
    const tc = new TileCache({ file: 'a.wav', totalFrames: TOTAL, filePeaks, onChange() {} });
    tc.columns({ start: 0, fpp: 1, width: 10 }, 1);
    await until(() => seen.length > 0);
    for (const recv of seen) {
      assert.ok(!(recv instanceof TileCache), 'fetch was called as a method of the cache');
    }
  } finally {
    globalThis.fetch = real;
  }
});
