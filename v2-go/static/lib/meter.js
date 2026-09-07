// Live scrolling waveform + level meters.
//
// Three things the previous version got wrong, fixed here:
//   1. Canvas size was read once at parse time, so it was stale forever after
//      a rotate or a late layout. Sizing now comes from a ResizeObserver.
//   2. Amplitude was scaled linearly, which pins loud material to the rail and
//      makes quiet material sub-pixel. Amplitudes are warped through dB.
//   3. A translucent fillRect was painted *over* the finished waveform.

const FLOOR_DB = -60;

/** Map a linear -1..1 amplitude onto -1..1 with a dB response. */
function warp(v) {
  const a = Math.abs(v);
  if (a < 1e-7) return 0;
  const db = 20 * Math.log10(a);
  if (db <= FLOOR_DB) return 0;
  const t = (db - FLOOR_DB) / -FLOOR_DB; // 0..1
  return v < 0 ? -t : t;
}

function dbToFrac(db) {
  if (!isFinite(db) || db <= FLOOR_DB) return 0;
  return Math.min(1, (db - FLOOR_DB) / -FLOOR_DB);
}

export class Visualizer {
  /**
   * @param {HTMLCanvasElement} canvas
   * @param {{capacity?: number, channels: number[]}} opts
   *        channels: source channel indices to draw (zero-based)
   */
  constructor(canvas, opts) {
    this.canvas = canvas;
    this.ctx = canvas.getContext('2d', { alpha: false });
    this.capacity = opts.capacity ?? 900; // ~9s at 100 bins/s
    this.channels = opts.channels ?? [0, 1];

    // bins[i] = {min:[..], max:[..]} kept as a plain ring
    this.bins = new Array(this.capacity);
    this.head = 0;
    this.count = 0;

    this.cssW = 0;
    this.cssH = 0;
    this.dpr = 0;
    this.running = false;
    this.lastData = 0;

    this._ro = new ResizeObserver(() => this.resize());
    this._ro.observe(canvas);
    // Orientation changes on iOS can fire before layout settles.
    this._onOrient = () => setTimeout(() => this.resize(), 150);
    window.addEventListener('orientationchange', this._onOrient);

    this._onVis = () => (document.hidden ? this.stop() : this.start());
    document.addEventListener('visibilitychange', this._onVis);

    this.resize();
    this.start();
  }

  setChannels(ch) {
    if (Array.isArray(ch) && ch.length) this.channels = ch;
  }

  resize() {
    const r = this.canvas.getBoundingClientRect();
    const dpr = window.devicePixelRatio || 1;
    const w = Math.max(1, Math.round(r.width));
    const h = Math.max(1, Math.round(r.height));
    if (w === this.cssW && h === this.cssH && dpr === this.dpr) return;

    this.cssW = w;
    this.cssH = h;
    this.dpr = dpr;
    this.canvas.width = Math.round(w * dpr);
    this.canvas.height = Math.round(h * dpr);
    this.ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    this.draw();
  }

  push(frame) {
    const bins = frame.bins;
    if (!bins || !bins.length) return;
    this.lastData = performance.now();
    for (const b of bins) {
      this.bins[this.head] = b;
      this.head = (this.head + 1) % this.capacity;
      if (this.count < this.capacity) this.count++;
    }
  }

  start() {
    if (this.running) return;
    this.running = true;
    const loop = () => {
      if (!this.running) return;
      this.draw();
      this._raf = requestAnimationFrame(loop);
    };
    this._raf = requestAnimationFrame(loop);
  }

  stop() {
    this.running = false;
    cancelAnimationFrame(this._raf);
  }

  destroy() {
    this.stop();
    this._ro.disconnect();
    window.removeEventListener('orientationchange', this._onOrient);
    document.removeEventListener('visibilitychange', this._onVis);
  }

  draw() {
    const { ctx } = this;
    const w = this.cssW;
    const h = this.cssH;
    if (w <= 0 || h <= 0) return;

    ctx.fillStyle = '#060b16';
    ctx.fillRect(0, 0, w, h);

    const mid = h / 2;

    // grid
    ctx.strokeStyle = 'rgba(38, 50, 74, 0.6)';
    ctx.lineWidth = 1;
    ctx.beginPath();
    for (let db = -12; db > FLOOR_DB; db -= 12) {
      const f = dbToFrac(db) * (h / 2);
      ctx.moveTo(0, mid - f);
      ctx.lineTo(w, mid - f);
      ctx.moveTo(0, mid + f);
      ctx.lineTo(w, mid + f);
    }
    ctx.stroke();

    ctx.strokeStyle = 'rgba(52, 211, 153, 0.28)';
    ctx.beginPath();
    ctx.moveTo(0, mid);
    ctx.lineTo(w, mid);
    ctx.stroke();

    if (this.count === 0) return;

    // One column per device pixel column, newest at the right edge.
    const cols = Math.min(this.count, Math.floor(w));
    if (cols <= 0) return;
    const step = this.count / cols;
    const colW = w / cols;

    const colors = ['rgba(52, 211, 153, 0.95)', 'rgba(110, 231, 183, 0.62)'];

    this.channels.forEach((chIdx, ci) => {
      ctx.fillStyle = colors[ci % colors.length];
      ctx.beginPath();
      for (let i = 0; i < cols; i++) {
        // walk the ring oldest -> newest for the visible window
        const from = Math.floor(i * step);
        const to = Math.max(from + 1, Math.floor((i + 1) * step));
        let lo = 0;
        let hi = 0;
        for (let k = from; k < to; k++) {
          const idx = (this.head - this.count + k + this.capacity * 2) % this.capacity;
          const b = this.bins[idx];
          if (!b || !b.min || chIdx >= b.min.length) continue;
          const mn = b.min[chIdx];
          const mx = b.max[chIdx];
          if (mn < lo) lo = mn;
          if (mx > hi) hi = mx;
        }
        const yTop = mid - warp(hi) * (h / 2 - 2);
        const yBot = mid - warp(lo) * (h / 2 - 2);
        const x = i * colW;
        ctx.rect(x, yTop, Math.max(colW, 1), Math.max(yBot - yTop, 1));
      }
      ctx.fill();
    });
  }
}

/** Renders a row of DOM level meters and keeps them updated. */
export class Meters {
  constructor(container, { labels, selected = [] }) {
    this.container = container;
    this.rows = labels.map((label, i) => {
      const row = document.createElement('div');
      row.className = 'meter' + (selected.includes(i) ? ' sel' : '');
      row.innerHTML =
        '<span class="lbl"></span>' +
        '<span class="bar"><span class="fill"></span><span class="peak"></span></span>' +
        '<span class="val">–</span>';
      row.querySelector('.lbl').textContent = label;
      container.appendChild(row);
      return {
        row,
        fill: row.querySelector('.fill'),
        peak: row.querySelector('.peak'),
        val: row.querySelector('.val'),
      };
    });
  }

  update(rms, peak, clip) {
    this.rows.forEach((r, i) => {
      const db = rms?.[i] ?? FLOOR_DB;
      const pk = peak?.[i] ?? FLOOR_DB;
      r.fill.style.width = (dbToFrac(db) * 100).toFixed(1) + '%';
      r.peak.style.left = (dbToFrac(pk) * 100).toFixed(1) + '%';
      r.val.textContent = db <= FLOOR_DB ? '−∞' : db.toFixed(1);
      r.row.classList.toggle('clip', !!clip?.[i]);
    });
  }
}

export { FLOOR_DB, dbToFrac };
