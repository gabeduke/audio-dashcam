// web/static/lib/wave/tiles.js
import { levelFor, tileSpan, tilesFor, fileLevel, TILE_BUCKETS } from './geometry.js';

// TileCache turns a viewport into drawable min/max columns, fetching range
// peaks for whatever tiles it lacks and drawing the coarsest thing it has
// in the meantime -- ultimately the 1024-bucket file peaks, which are
// always present. Nothing here touches the DOM.
// A bare `globalThis.fetch` handed around as a value and then called as
// `this.fetchFn(url)` throws "Illegal invocation" in browsers -- fetch wants
// the window as its receiver -- while node's fetch does not care, so this
// only ever broke the real page. Wrapped, it works in both.
const defaultFetch = (...args) => globalThis.fetch(...args);

export class TileCache {
  constructor({ file, totalFrames, filePeaks, fetchFn = defaultFetch, onChange, onGone, maxTiles = 256 }) {
    this.file = file;
    this.totalFrames = totalFrames;
    this.filePeaks = filePeaks;
    this.fetchFn = fetchFn;
    this.onChange = onChange;
    this.onGone = onGone;
    this.maxTiles = maxTiles;
    this.fileLevel = fileLevel(totalFrames);
    this.cache = new Map();     // key -> { pd, used }
    this.inflight = new Set();
    this.timers = new Map();    // key -> timeout id
    this.attempts = new Map();  // key -> count
    this.retryBase = 500;
    this.gone = false;
    this.tick = 0;
  }

  stop() {
    for (const t of this.timers.values()) clearTimeout(t);
    this.timers.clear();
  }

  key(level, i) { return `${level}:${i}`; }

  // Best available peaks covering tile (level, i): the tile itself, else the
  // nearest coarser ancestor that is cached, else the file. Returns
  // { pd, level } so the caller can map frames to buckets.
  best(level, i) {
    for (let l = level, idx = i; l < this.fileLevel; l++, idx = Math.floor(idx / 2)) {
      const hit = this.cache.get(this.key(l, idx));
      if (hit) { hit.used = ++this.tick; return { pd: hit.pd, level: l }; }
    }
    return { pd: this.filePeaks, level: this.fileLevel, isFile: true };
  }

  request(level, i) {
    const k = this.key(level, i);
    if (this.gone || this.cache.has(k) || this.inflight.has(k) || this.timers.has(k)) return;
    const span = tileSpan(level);
    const from = i * span;
    const to = Math.min(this.totalFrames, from + span);
    if (from >= to) return;
    this.inflight.add(k);
    const url = `/api/peaks?file=${encodeURIComponent(this.file)}&from=${from}&to=${to}&buckets=${TILE_BUCKETS}`;
    this.fetchFn(url).then(async (res) => {
      if (res.status === 404) {
        const first = !this.gone;
        this.gone = true;
        this.stop();
        if (first) this.onGone?.();
        return;
      }
      if (!res.ok) throw new Error(`status ${res.status}`);
      const pd = await res.json();
      this.cache.set(k, { pd, used: ++this.tick });
      this.attempts.delete(k);
      this.evict();
      this.onChange?.();
    }).catch(() => {
      const n = (this.attempts.get(k) || 0) + 1;
      this.attempts.set(k, n);
      const delay = Math.min(this.retryBase * 2 ** (n - 1), this.retryBase * 16);
      this.timers.set(k, setTimeout(() => { this.timers.delete(k); this.request(level, i); }, delay));
    }).finally(() => this.inflight.delete(k));
  }

  evict() {
    while (this.cache.size > this.maxTiles) {
      let oldest = null;
      for (const [k, v] of this.cache) if (!oldest || v.used < oldest[1].used) oldest = [k, v];
      this.cache.delete(oldest[0]);
    }
  }

  // Columns for the viewport: 2*channels floats per CSS pixel column.
  columns(view, dpr) {
    let level = levelFor(view.fpp, dpr);
    const ch = this.filePeaks.channels;
    const cols = new Float32Array(Math.ceil(view.width) * ch * 2);
    const useFile = level >= this.fileLevel;
    if (!useFile) {
      const { first, last } = tilesFor(view, level, this.totalFrames);
      for (let i = first; i <= last; i++) this.request(level, i);
    }
    for (let x = 0; x < Math.ceil(view.width); x++) {
      const f0 = view.start + x * view.fpp;
      const f1 = f0 + view.fpp;
      if (f1 <= 0 || f0 >= this.totalFrames) continue;
      const src = useFile
        ? { pd: this.filePeaks, level: this.fileLevel, isFile: true }
        : this.best(level, Math.floor(f0 / tileSpan(level)));
      const pd = src.pd;
      // A tile's real bucket size comes from the response, not from its
      // level: RangePeaks uses per = floor((to-from)/buckets), and the last
      // tile of a take is clamped to totalFrames, so its per is smaller than
      // 2**level. Deriving it from the payload keeps the tail of the last
      // tile from rendering as a squeezed copy of its own beginning; the
      // clamp of b0/b1 below covers the remainder the last bucket absorbs.
      const bucketFrames = src.isFile
        ? this.totalFrames / pd.buckets
        : Math.max(1, Math.floor((pd.duration * pd.sample_rate) / pd.buckets));
      const base = src.isFile ? 0 : pd.from;
      let b0 = Math.floor((f0 - base) / bucketFrames);
      let b1 = Math.ceil((f1 - base) / bucketFrames) - 1;
      b0 = Math.max(0, Math.min(pd.buckets - 1, b0));
      b1 = Math.max(b0, Math.min(pd.buckets - 1, b1));
      for (let c = 0; c < ch; c++) {
        let mn = Infinity, mx = -Infinity;
        for (let b = b0; b <= b1; b++) {
          const v0 = pd.data[c][b * 2], v1 = pd.data[c][b * 2 + 1];
          if (v0 < mn) mn = v0;
          if (v1 > mx) mx = v1;
        }
        cols[(x * ch + c) * 2] = mn;
        cols[(x * ch + c) * 2 + 1] = mx;
      }
    }
    return { level, cols, channels: ch };
  }
}
