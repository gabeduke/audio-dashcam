// web/static/lib/wave/overview.js
// The whole-take strip above the main waveform. It exists so the main
// waveform's one-finger drag can select a region: navigation lives here.
// The pure functions are what the tests cover; the class is the canvas
// and pointer plumbing around them.
export const OVERVIEW_MIN_WINDOW_PX = 24;
const TAP_MOVE = 6;
const TAP_MS = 300;

export function windowRect(view, totalFrames, stripWidth) {
  const span = view.width * view.fpp;
  let w = Math.max(OVERVIEW_MIN_WINDOW_PX, (span / totalFrames) * stripWidth);
  w = Math.min(w, stripWidth);
  let x = (view.start / totalFrames) * stripWidth;
  x = Math.max(0, Math.min(stripWidth - w, x));
  return { x, w };
}

export function stripXToFrame(x, totalFrames, stripWidth) {
  return Math.round(Math.max(0, Math.min(stripWidth, x)) / stripWidth * totalFrames);
}

export function dragToStart(x, grabOffset, view, totalFrames, stripWidth) {
  const span = view.width * view.fpp;
  const maxStart = Math.max(0, totalFrames - span);
  const start = ((x - grabOffset) / stripWidth) * totalFrames;
  return Math.round(Math.max(0, Math.min(maxStart, start)));
}

export class Overview {
  constructor({ canvas, filePeaks, totalFrames, getState, getView, emit }) {
    this.canvas = canvas;
    this.ctx = canvas.getContext('2d');
    this.peaks = filePeaks;
    this.total = totalFrames;
    this.getState = getState;
    this.getView = getView;
    this.emit = emit;
    this.raf = 0;
    this.gesture = null;
    this.lastTap = 0;
    this.ac = new AbortController();
    canvas.style.touchAction = 'none';
    const s = this.ac.signal;
    canvas.addEventListener('pointerdown', (e) => this.down(e), { signal: s });
    canvas.addEventListener('pointermove', (e) => this.move(e), { signal: s });
    canvas.addEventListener('pointerup', (e) => this.up(e), { signal: s });
    canvas.addEventListener('pointercancel', (e) => this.up(e), { signal: s });
    this.ro = new ResizeObserver(() => this.resize());
    this.ro.observe(canvas);
    this.resize();
  }

  destroy() { this.ac.abort(); this.ro.disconnect(); cancelAnimationFrame(this.raf); this.destroyed = true; }

  resize() {
    const r = this.canvas.getBoundingClientRect();
    const dpr = window.devicePixelRatio || 1;
    this.dpr = dpr; this.cssW = r.width; this.cssH = r.height;
    this.canvas.width = Math.round(r.width * dpr);
    this.canvas.height = Math.round(r.height * dpr);
    this.draw();
  }

  draw() {
    if (this.destroyed || this.raf) return;
    this.raf = requestAnimationFrame(() => { this.raf = 0; this.paint(); });
  }

  paint() {
    const { ctx, dpr, cssW: W, cssH: H } = this;
    if (!W) return;
    const st = this.getState();
    const css = getComputedStyle(this.canvas);
    const col = (n, fb) => css.getPropertyValue(n).trim() || fb;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.fillStyle = col('--panel-2', '#1a2437');
    ctx.fillRect(0, 0, W, H);

    // Whole-take waveform, both channels folded into one lane.
    const pd = this.peaks;
    ctx.fillStyle = col('--ink-faint', '#5d6b85');
    const mid = H / 2;
    for (let x = 0; x < W; x++) {
      const b0 = Math.floor((x / W) * pd.buckets), b1 = Math.max(b0, Math.floor(((x + 1) / W) * pd.buckets) - 1);
      let mn = Infinity, mx = -Infinity;
      for (let c = 0; c < pd.channels; c++) for (let b = b0; b <= b1; b++) {
        mn = Math.min(mn, pd.data[c][b * 2]); mx = Math.max(mx, pd.data[c][b * 2 + 1]);
      }
      if (!(mx >= mn)) continue;
      ctx.fillRect(x, mid - mx * mid * 0.9, 1, Math.max(1, (mx - mn) * mid * 0.9));
    }

    // Region band and flags
    if (st.region) {
      ctx.fillStyle = 'rgba(52,211,153,0.25)';
      const x0 = (st.region.start / this.total) * W, x1 = (st.region.end / this.total) * W;
      ctx.fillRect(x0, 0, Math.max(1, x1 - x0), H);
    }
    ctx.fillStyle = col('--flag', '#ffb020');
    for (const f of st.flags || []) ctx.fillRect(Math.round((f.frame / this.total) * W), 0, 1, H);

    // Cursor
    ctx.fillStyle = col('--ink', '#eef2f8');
    ctx.fillRect(Math.round((st.cursor / this.total) * W), 0, 1, H);

    // Viewport window
    const { x, w } = windowRect(this.getView(), this.total, W);
    ctx.fillStyle = 'rgba(238,242,248,0.12)';
    ctx.fillRect(x, 0, w, H);
    ctx.strokeStyle = 'rgba(238,242,248,0.6)';
    ctx.lineWidth = 1;
    ctx.strokeRect(x + 0.5, 0.5, w - 1, H - 1);
  }

  pt(e) { const r = this.canvas.getBoundingClientRect(); return { x: e.clientX - r.left, y: e.clientY - r.top }; }

  down(e) {
    this.canvas.setPointerCapture(e.pointerId);
    const p = this.pt(e);
    const { x, w } = windowRect(this.getView(), this.total, this.cssW);
    const inside = p.x >= x && p.x <= x + w;
    this.gesture = { x0: p.x, t0: performance.now(), moved: false, inside, grabOffset: p.x - x };
  }

  move(e) {
    const g = this.gesture;
    if (!g) return;
    const p = this.pt(e);
    if (Math.abs(p.x - g.x0) > TAP_MOVE) g.moved = true;
    if (g.inside && g.moved) {
      this.emit('panTo', { start: dragToStart(p.x, g.grabOffset, this.getView(), this.total, this.cssW) });
    }
  }

  up(e) {
    const g = this.gesture;
    this.gesture = null;
    if (!g) return;
    const p = this.pt(e);
    const isTap = !g.moved && performance.now() - g.t0 < TAP_MS;
    if (!isTap) return;
    const now = performance.now();
    if (now - this.lastTap < TAP_MS) { this.lastTap = 0; this.emit('fitAll', {}); return; }
    this.lastTap = now;
    if (!g.inside) this.emit('centerOn', { frame: stripXToFrame(p.x, this.total, this.cssW) });
  }
}
