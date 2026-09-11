// web/static/lib/wave/geometry.js
// Pure math for the waveform page. No DOM, no fetch, so it runs under
// `node --test`. Every other wave module imports from here rather than
// re-deriving frame/pixel/tile arithmetic.

export const TILE_BUCKETS = 1024;

// view = { start: first frame at x=0, fpp: frames per CSS pixel, width: CSS px }
export function frameToX(frame, view) { return (frame - view.start) / view.fpp; }
export function xToFrame(x, view) { return Math.round(view.start + x * view.fpp); }

// The finest tile level with at most two buckets per device pixel.
export function levelFor(fpp, dpr) {
  const need = (fpp * dpr) / 2;
  let k = 0;
  while (2 ** k < need) k++;
  return k;
}

export function tileSpan(level) { return TILE_BUCKETS * 2 ** level; }

export function tilesFor(view, level, totalFrames) {
  const span = tileSpan(level);
  const endFrame = Math.min(totalFrames, view.start + view.width * view.fpp) - 1;
  const maxTile = Math.floor(Math.max(0, totalFrames - 1) / span);
  const first = Math.max(0, Math.floor(Math.max(0, view.start) / span) - 1);
  const last = Math.min(maxTile, Math.floor(Math.max(0, endFrame) / span) + 1);
  return { first, last };
}

export function fileLevel(totalFrames) {
  return Math.max(0, Math.ceil(Math.log2(totalFrames / TILE_BUCKETS)));
}

const BEATS_PER_BAR = 4;
const MIN_BEAT_PX = 8;
const MIN_BAR_PX = 4;

export function framesPerBeat({ bpm, sampleRate }) { return (sampleRate * 60) / bpm; }

export function gridLines(view, grid) {
  if (!grid.bpm) return [];
  const fpb = framesPerBeat(grid);
  const beatPx = fpb / view.fpp;
  const barPx = beatPx * BEATS_PER_BAR;
  if (barPx < MIN_BAR_PX) return [];
  const showBeats = beatPx >= MIN_BEAT_PX;
  const step = showBeats ? fpb : fpb * BEATS_PER_BAR;
  const endFrame = view.start + view.width * view.fpp;
  const out = [];
  let n = Math.ceil((view.start - grid.downbeat) / step);
  for (;;) {
    const frame = grid.downbeat + n * step;
    if (frame >= endFrame) break;
    const beatIndex = showBeats ? n : n * BEATS_PER_BAR;
    out.push({ frame: Math.round(frame), bar: ((beatIndex % BEATS_PER_BAR) + BEATS_PER_BAR) % BEATS_PER_BAR === 0 });
    n++;
  }
  return out;
}

export function barBeat(frame, grid) {
  if (!grid.bpm) return '';
  const fpb = framesPerBeat(grid);
  const beats = Math.floor((frame - grid.downbeat) / fpb);
  const bar = Math.floor(beats / BEATS_PER_BAR);
  const beat = ((beats % BEATS_PER_BAR) + BEATS_PER_BAR) % BEATS_PER_BAR;
  const barLabel = bar >= 0 ? bar + 1 : bar;
  return `${barLabel}.${beat + 1}`;
}

export function fmtTime(frame, sampleRate) {
  const ms = Math.floor((frame / sampleRate) * 1000);
  const m = Math.floor(ms / 60000);
  const s = Math.floor((ms % 60000) / 1000);
  const r = ms % 1000;
  return `${m}:${String(s).padStart(2, '0')}.${String(r).padStart(3, '0')}`;
}

export function clampRegion(region, totalFrames, minLen) {
  let start = Math.max(0, Math.round(region.start));
  let end = Math.min(totalFrames, Math.round(region.end));
  if (end - start < minLen) {
    end = start + minLen;
    if (end > totalFrames) {
      end = totalFrames;
      start = Math.max(0, end - minLen);
    }
  }
  return { start, end };
}
