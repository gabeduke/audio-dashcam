// web/static/lib/wave/view.js
// The canvas view of the waveform page: it paints from state it is handed and
// turns pointer/wheel gestures into events. It owns exactly one piece of
// state, the viewport {start, fpp, width}; everything else is read fresh from
// getState() on each paint so the page stays the single source of truth.
import { frameToX, xToFrame, gridLines, clampRegion } from './geometry.js';

const HANDLE_HIT = 24;   // CSS px each side of a handle
const FLAG_HIT = 12;
const TAP_MOVE = 8;
const TAP_MS = 300;
const MIN_FPP = 1 / 8;   // 8 px per frame: far enough
const CHIP_H = 16;
const DOWNBEAT_HIT_H = 24; // the downbeat is only grabbable in the top strip
const DOUBLE_TAP_MOVE = 20;
const MIN_PINCH = 8;       // below this the ratio g.dist/dist goes wild

// True separation of two fingers, floored so a near-vertical pinch cannot
// divide by ~0 and fling the zoom.
function pinchDist(a, b) { return Math.max(MIN_PINCH, Math.hypot(a.x - b.x, a.y - b.y)); }

export class WaveView {
  constructor({ canvas, tiles, totalFrames, sampleRate, getState, emit }) {
    this.canvas = canvas;
    this.ctx = canvas.getContext('2d');
    this.tiles = tiles;
    this.total = totalFrames;
    this.sr = sampleRate;
    this.getState = getState;
    this.emit = emit;
    this.view = { start: 0, fpp: 1, width: 1 };
    this.dpr = window.devicePixelRatio || 1;
    this.cssW = 0;
    this.cssH = 0;
    this.raf = 0;
    this.fitted = false;      // has a first real layout happened yet?
    this.destroyed = false;
    this.pointers = new Map();
    this.gesture = null;      // { kind, ...}
    this.lastTap = null;
    this.minLen = Math.floor(sampleRate * 3 / 1000) * 2 + 1;
    this.chipRects = [];      // [{flag, x, y, w, h}] from the last draw
    this.resize = this.resize.bind(this);
    this.ro = new ResizeObserver(this.resize);
    this.ro.observe(canvas);
    canvas.style.touchAction = 'none';
    // One AbortController so destroy() actually detaches the listeners.
    this.ac = new AbortController();
    const sig = { signal: this.ac.signal };
    canvas.addEventListener('pointerdown', (e) => this.down(e), sig);
    canvas.addEventListener('pointermove', (e) => this.move(e), sig);
    canvas.addEventListener('pointerup', (e) => this.up(e), sig);
    canvas.addEventListener('pointercancel', (e) => this.up(e), sig);
    canvas.addEventListener('wheel', (e) => this.wheel(e), { passive: false, signal: this.ac.signal });
    this.resize();
  }

  destroy() {
    this.destroyed = true;
    this.ro.disconnect();
    this.ac.abort();
    cancelAnimationFrame(this.raf);
    this.raf = 0;
  }

  resize() {
    const r = this.canvas.getBoundingClientRect();
    const dpr = window.devicePixelRatio || 1;
    this.dpr = dpr;
    this.canvas.width = Math.round(r.width * dpr);
    this.canvas.height = Math.round(r.height * dpr);
    this.cssW = r.width; this.cssH = r.height;
    // A zero-width layout (hidden panel) carries no information: keep the
    // viewport we have and wait for a real one, or maxFpp() goes infinite.
    if (r.width <= 0) return;
    this.view.width = r.width;
    // Fit only on the first real layout; later resizes keep start/fpp and are
    // merely re-clamped against the new width.
    if (!this.fitted) { this.fitted = true; this.fitAll(); } else { this.clampView(); }
    this.draw();
  }

  maxFpp() { return Math.max(MIN_FPP, this.total / this.view.width); }
  fitAll() { this.view.fpp = this.maxFpp(); this.view.start = 0; this.changed(); }
  centerOn(frame) { this.view.start = frame - (this.view.width * this.view.fpp) / 2; this.clampView(); this.changed(); }
  panTo(start) { this.view.start = start; this.clampView(); this.changed(); }
  zoomTo(fpp, aroundX) {
    const f = xToFrame(aroundX, this.view);
    this.view.fpp = Math.min(this.maxFpp(), Math.max(MIN_FPP, fpp));
    this.view.start = f - aroundX * this.view.fpp;
    this.clampView(); this.changed();
  }
  clampView() {
    // fpp first: a resize changes what maxFpp() means, and leaving fpp above it
    // would leave width*fpp > total, i.e. dead space past the end of the file.
    this.view.fpp = Math.min(this.maxFpp(), Math.max(MIN_FPP, this.view.fpp));
    const span = this.view.width * this.view.fpp;
    this.view.start = Math.max(0, Math.min(this.total - span, this.view.start));
    if (span >= this.total) this.view.start = 0;
  }
  changed() { this.emit('viewChange', { view: { ...this.view } }); this.draw(); }

  draw() {
    if (this.destroyed || this.raf) return;
    this.raf = requestAnimationFrame(() => { this.raf = 0; this.paint(); });
  }

  paint() {
    const { ctx, view, dpr } = this;
    const st = this.getState();
    const flags = st.flags || [];
    const css = getComputedStyle(this.canvas);
    const col = (name, fb) => css.getPropertyValue(name).trim() || fb;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, this.cssW, this.cssH);
    ctx.fillStyle = col('--panel', '#131c2e');
    ctx.fillRect(0, 0, this.cssW, this.cssH);

    // Grid
    for (const g of gridLines(view, st.grid)) {
      const x = frameToX(g.frame, view);
      ctx.fillStyle = g.bar ? col('--line', '#26324a') : 'rgba(255,255,255,0.06)';
      ctx.fillRect(Math.round(x), 0, 1, this.cssH);
    }

    // Waveform: channels stacked
    const { cols, channels } = this.tiles.columns(view, dpr);
    const laneH = (this.cssH - CHIP_H) / channels;
    ctx.fillStyle = col('--accent', '#34d399');
    for (let x = 0; x < Math.ceil(view.width); x++) {
      for (let c = 0; c < channels; c++) {
        const mn = cols[(x * channels + c) * 2], mx = cols[(x * channels + c) * 2 + 1];
        if (!(mx >= mn)) continue;
        const mid = CHIP_H + laneH * c + laneH / 2;
        const y0 = mid - mx * (laneH / 2) * 0.95, y1 = mid - mn * (laneH / 2) * 0.95;
        ctx.fillRect(x, y0, 1, Math.max(1, y1 - y0));
      }
    }

    // Region
    if (st.region) {
      const x0 = frameToX(st.region.start, view), x1 = frameToX(st.region.end, view);
      ctx.fillStyle = 'rgba(52,211,153,0.14)';
      ctx.fillRect(x0, CHIP_H, x1 - x0, this.cssH - CHIP_H);
      ctx.fillStyle = col('--accent', '#34d399');
      for (const x of [x0, x1]) {
        ctx.fillRect(Math.round(x) - 1, CHIP_H, 2, this.cssH - CHIP_H);
        ctx.fillRect(Math.round(x) - 8, 0, 16, CHIP_H - 2); // grip tab
      }
    }

    // Downbeat marker
    if (st.grid.bpm) {
      const x = frameToX(st.grid.downbeat, view);
      ctx.fillStyle = col('--warn', '#fbbf24');
      ctx.fillRect(Math.round(x) - 1, 0, 2, this.cssH);
      ctx.beginPath(); ctx.moveTo(x - 6, 0); ctx.lineTo(x + 6, 0); ctx.lineTo(x, 8); ctx.fill();
    }

    // Flags with chips (a chip is hidden if it would overlap the previous one)
    this.chipRects = [];
    let lastChipRight = -Infinity;
    ctx.font = '11px ' + col('--font', 'system-ui');
    for (const f of flags) {
      const x = frameToX(f.frame, view);
      if (x < -200 || x > this.cssW + 200) continue;
      const sel = st.selectedFlag && st.selectedFlag.frame === f.frame;
      ctx.fillStyle = col('--flag', '#ffb020');
      ctx.fillRect(Math.round(x) - (sel ? 2 : 1), CHIP_H, sel ? 4 : 2, this.cssH - CHIP_H);
      if (f.label && x + 4 > lastChipRight) {
        const w = Math.min(160, ctx.measureText(f.label).width + 10);
        ctx.fillRect(x + 4, 1, w, CHIP_H - 3);
        ctx.fillStyle = '#000';
        ctx.fillText(f.label, x + 9, CHIP_H - 5, w - 10);
        this.chipRects.push({ flag: f, x: x + 4, y: 0, w, h: CHIP_H });
        lastChipRight = x + 4 + w + 4;
      }
    }

    // Cursor
    if (st.cursor != null) {
      const cx = frameToX(st.cursor, view);
      ctx.fillStyle = col('--ink', '#eef2f8');
      ctx.fillRect(Math.round(cx), 0, 1, this.cssH);
    }
  }

  // --- hit testing -------------------------------------------------------
  // Priority: chips, downbeat, handles, region body, flag ticks, bare wave.
  // The downbeat outranks the region because the region body matches at every
  // y: left below it, a downbeat enclosed by the region would be ungrabbable.
  // It is gated to the top strip, so it only steals from the drawn grip tab.
  hit(x, y, st = this.getState()) {
    for (const r of this.chipRects) if (x >= r.x && x <= r.x + r.w && y <= r.h) return { kind: 'flag', flag: r.flag };
    if (st.grid.bpm && Math.abs(x - frameToX(st.grid.downbeat, this.view)) <= HANDLE_HIT && y < DOWNBEAT_HIT_H) return { kind: 'downbeat' };
    if (st.region) {
      const x0 = frameToX(st.region.start, this.view), x1 = frameToX(st.region.end, this.view);
      if (Math.abs(x - x0) <= HANDLE_HIT) return { kind: 'handle', edge: 'start' };
      if (Math.abs(x - x1) <= HANDLE_HIT) return { kind: 'handle', edge: 'end' };
      if (x > x0 && x < x1) return { kind: 'region' };
    }
    for (const f of (st.flags || [])) if (Math.abs(x - frameToX(f.frame, this.view)) <= FLAG_HIT) return { kind: 'flag', flag: f };
    return { kind: 'wave' };
  }

  // --- pointer events ----------------------------------------------------
  pt(e) { const r = this.canvas.getBoundingClientRect(); return { x: e.clientX - r.left, y: e.clientY - r.top }; }

  down(e) {
    this.canvas.setPointerCapture(e.pointerId);
    const p = this.pt(e);
    this.pointers.set(e.pointerId, p);
    // Two or more fingers is always a pinch: replacing this.gesture discards
    // whatever the first finger had started (a select, a handle drag) so a
    // pinch never also selects. A third finger must not fall through to the
    // hit test.
    if (this.pointers.size >= 2) {
      const [a, b] = [...this.pointers.values()];
      this.gesture = { kind: 'pinch', dist: pinchDist(a, b), mid: (a.x + b.x) / 2, fpp: this.view.fpp, start: this.view.start };
      this.lastTap = null;
      return;
    }
    const st = this.getState();
    const h = this.hit(p.x, p.y, st);
    const base = { x0: p.x, y0: p.y, t0: performance.now(), moved: false, start: this.view.start };
    // Every drag carries the offset from the grab point to the thing being
    // dragged, so the first pointermove nudges it instead of teleporting it
    // under the finger.
    switch (h.kind) {
      case 'handle': this.gesture = { ...base, kind: 'handle', edge: h.edge, region: { ...st.region }, grabOffset: p.x - frameToX(st.region[h.edge], this.view) }; break;
      case 'region': this.gesture = { ...base, kind: 'moveRegion', region: { ...st.region } }; break;
      case 'downbeat': this.gesture = { ...base, kind: 'downbeat', grabOffset: p.x - frameToX(st.grid.downbeat, this.view) }; break;
      case 'flag': this.gesture = { ...base, kind: 'flag', flag: h.flag }; break;
      // A one-finger drag on bare waveform selects. Panning lives on the
      // overview strip and in the two-finger gesture, so the one gesture a
      // thumb reaches for on the couch makes the thing you want to share.
      // prev is the region the drag started from: a sliver release restores it.
      default: this.gesture = { ...base, kind: 'select', anchor: xToFrame(p.x, this.view), emitted: false, prev: st.region ? { ...st.region } : null };
    }
    // Only a bare-wave tap can be half of a double-tap; anything else breaks
    // the pair so a tap-then-flag-tap never lands an unwanted flag.
    if (h.kind !== 'wave') this.lastTap = null;
  }

  move(e) {
    if (!this.pointers.has(e.pointerId)) return;
    const p = this.pt(e);
    this.pointers.set(e.pointerId, p);
    const g = this.gesture;
    if (!g) return;
    if (g.kind === 'pinch' && this.pointers.size >= 2) {
      const [a, b] = [...this.pointers.values()];
      const dist = pinchDist(a, b);
      const mid = (a.x + b.x) / 2;
      const fpp = Math.min(this.maxFpp(), Math.max(MIN_FPP, g.fpp * (g.dist / dist)));
      const anchor = g.start + g.mid * g.fpp; // frame under the original midpoint
      this.view.fpp = fpp;
      this.view.start = anchor - mid * fpp;
      this.clampView(); this.changed();
      return;
    }
    const dx = p.x - g.x0;
    if (Math.abs(dx) > TAP_MOVE || Math.abs(p.y - g.y0) > TAP_MOVE) g.moved = true;
    switch (g.kind) {
      case 'select': {
        if (!g.moved) break;
        const cur = xToFrame(p.x, this.view);
        const r = { start: Math.min(g.anchor, cur), end: Math.max(g.anchor, cur) };
        g.emitted = true;
        // The provisional clamp allows a region shorter than minLen while the
        // finger is still moving, so the band tracks it from the first pixel;
        // the minimum is enforced only on release.
        this.emit('regionChange', { region: clampRegion(r, this.total, Math.max(1, Math.min(this.minLen, r.end - r.start))), final: false });
        this.draw(); break;
      }
      case 'handle': {
        const f = xToFrame(p.x - g.grabOffset, this.view);
        const r = { ...g.region, [g.edge]: f };
        if (g.edge === 'start') r.start = Math.min(r.start, r.end - this.minLen);
        else r.end = Math.max(r.end, r.start + this.minLen);
        this.emit('regionChange', { region: clampRegion(r, this.total, this.minLen), final: false });
        this.draw(); break;
      }
      case 'moveRegion': {
        const d = Math.round(dx * this.view.fpp);
        const len = g.region.end - g.region.start;
        const start = Math.max(0, Math.min(this.total - len, g.region.start + d));
        this.emit('regionChange', { region: { start, end: start + len }, final: false });
        this.draw(); break;
      }
      case 'downbeat':
        this.emit('downbeatChange', { frame: Math.max(0, Math.min(this.total - 1, xToFrame(p.x - g.grabOffset, this.view))), final: false });
        this.draw(); break;
      default: break;
    }
  }

  up(e) {
    const p = this.pt(e);
    this.pointers.delete(e.pointerId);
    const g = this.gesture;
    if (!g) return;
    // Dropping to one finger ends the pinch outright: the survivor does
    // nothing until it too lifts, rather than selecting from a stale anchor.
    if (g.kind === 'pinch') { if (this.pointers.size < 2) this.gesture = null; return; }
    this.gesture = null;
    const st = this.getState();
    const isTap = !g.moved && performance.now() - g.t0 < TAP_MS;
    switch (g.kind) {
      case 'handle':
      case 'moveRegion':
        if (g.moved) this.emit('regionChange', { region: st.region, final: true });
        else if (g.kind === 'moveRegion' && isTap) this.emit('seek', { frame: xToFrame(p.x, this.view) });
        break;
      case 'downbeat':
        if (g.moved) this.emit('downbeatChange', { frame: st.grid.downbeat, final: true });
        break;
      case 'flag':
        // A tap on a flag selects it and never reaches the double-tap path,
        // so double-tapping a flag cannot stack a second flag on top of it.
        if (isTap) this.emit('selectFlag', { flag: g.flag });
        break;
      case 'select':
        if (g.moved) {
          const cur = xToFrame(p.x, this.view);
          const r = { start: Math.min(g.anchor, cur), end: Math.max(g.anchor, cur) };
          if (r.end - r.start >= this.minLen) this.emit('regionChange', { region: clampRegion(r, this.total, this.minLen), final: true });
          // A sliver: discard it and put back whatever region the drag began
          // from, so a twitchy tap-drag does not destroy the take.
          else if (g.emitted) this.emit('regionChange', { region: g.prev, final: true });
        } else if (isTap) {
          const now = performance.now();
          if (this.lastTap && now - this.lastTap.t < TAP_MS && Math.hypot(p.x - this.lastTap.x, p.y - this.lastTap.y) < DOUBLE_TAP_MOVE) {
            this.lastTap = null;
            this.emit('addFlag', { frame: xToFrame(p.x, this.view) });
          } else {
            this.lastTap = { t: now, x: p.x, y: p.y };
            this.emit('seek', { frame: xToFrame(p.x, this.view) });
          }
        }
        break;
    }
  }

  wheel(e) {
    e.preventDefault();
    const p = this.pt(e);
    // Plain wheel zooms about the pointer: on a timeline that is the thing a
    // mouse user reaches for first, and the overview strip covers panning.
    // Shift, or a trackpad's horizontal axis, pans.
    const horizontal = Math.abs(e.deltaX) > Math.abs(e.deltaY);
    if (e.shiftKey || horizontal) this.panTo(this.view.start + (horizontal ? e.deltaX : e.deltaY) * this.view.fpp);
    else this.zoomTo(this.view.fpp * Math.exp(e.deltaY * 0.01), p.x);
  }
}
