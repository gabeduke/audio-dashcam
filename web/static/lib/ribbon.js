// The buffer ribbon: the capture ring drawn on a logarithmic age axis, with a
// marker at each capture tier and the selected tier's span shaded, so you can
// see which button catches your take before pressing it.
//
// Position comes from
//
//   left% = (1 - ln(age/A) / ln(T/A)) * 100
//
// with A = edge_seconds and T = ring_seconds, both read off the response so the
// server's bucketing and these markers cannot drift apart. The SVG viewBox is
// n-1 wide and vertex i sits at x = i, so bucket 0 (oldest) lands at 0% under
// the leftmost marker and bucket n-1 (newest) lands at 100% under "now".
//
// It replaces the live visualiser rather than sitting beside it. Resolution at
// the right edge improves (~50 px/s against the old flat 37.6), latency gets
// worse by 1-2s. Live level lives on the meters, still on the websocket.

import { fmtDur } from '/lib/meter.js';

const SVG_NS = 'http://www.w3.org/2000/svg';
const MAX_BUCKETS = 600;
const POLL_MS = 1000;

function add(parent, tag, cls) {
  const el = document.createElement(tag);
  el.className = cls;
  parent.appendChild(el);
  return el;
}

/** Decode the base64 envelope the server sends into bytes. */
function decode(b64) {
  const s = atob(b64 || '');
  const out = new Uint8Array(s.length);
  for (let i = 0; i < s.length; i++) out[i] = s.charCodeAt(i);
  return out;
}

export class Ribbon {
  /**
   * @param {HTMLElement} wrap the existing .viz-wrap
   * @param {{onToast?: (msg: string, kind?: string) => void}} [opts]
   */
  constructor(wrap, { onToast } = {}) {
    this.wrap = wrap;
    this.onToast = onToast;
    this.spans = [];      // capture tiers in seconds; 0 means the whole ring
    this.selected = null;
    this.data = null;
    this.dataSpans = null; // the spans this.data was actually fetched with
    this.timer = null;
    this._seq = 0; // monotonic guard against out-of-order poll responses

    wrap.textContent = '';
    this.hatch = add(wrap, 'div', 'rb-hatch');
    this.bandLayer = add(wrap, 'div', 'rb-layer');

    this.svg = document.createElementNS(SVG_NS, 'svg');
    this.svg.setAttribute('class', 'rb-wave');
    this.svg.setAttribute('preserveAspectRatio', 'none');
    this.path = document.createElementNS(SVG_NS, 'path');
    this.svg.appendChild(this.path);
    wrap.appendChild(this.svg);

    this.markLayer = add(wrap, 'div', 'rb-layer');
    this.flagLayer = add(wrap, 'div', 'rb-layer');
    add(wrap, 'div', 'rb-scrim');
    this.labelLayer = add(wrap, 'div', 'rb-layer');
    add(wrap, 'div', 'rb-now');

    const now = add(this.labelLayer, 'span', 'rb-label rb-now-label');
    now.textContent = 'now';

    this.readout = add(wrap, 'p', 'rb-readout');
    this.readout.textContent = 'connecting';

    this._onVis = () => (document.hidden ? this.stop() : this.start());
    document.addEventListener('visibilitychange', this._onVis);
    if (!document.hidden) this.start();
  }

  /** @param {number[]} spans tier lengths in seconds; 0 means the whole ring */
  setSpans(spans) {
    this.spans = spans.slice();
    if (this.selected === null || !this.spans.includes(this.selected)) {
      this.selected = this.spans[0] ?? null;
    }
    this.render();
    this.poll();
  }

  setSelected(seconds) {
    this.selected = seconds;
    this.render();
  }

  start() {
    if (this.timer) return;
    this.timer = setInterval(() => this.poll(), POLL_MS);
    this.poll();
  }

  stop() {
    clearInterval(this.timer);
    this.timer = null;
  }

  destroy() {
    this.stop();
    document.removeEventListener('visibilitychange', this._onVis);
  }

  async poll() {
    const spans = this.spans.slice(); // the spans this request actually asks for
    const w = Math.round(this.wrap.clientWidth) || 340;
    const n = Math.min(MAX_BUCKETS, Math.max(60, w));
    const q = `buckets=${n}&spans=${spans.join(',')}`;
    const seq = ++this._seq;

    let data;
    try {
      const res = await fetch(`/api/envelope?${q}`, { cache: 'no-store' });
      if (!res.ok) return; // keep the last ribbon drawn
      data = await res.json();
    } catch {
      // Keep the last ribbon drawn. The health dot already reports that the
      // server is unreachable, and a second indicator would add nothing.
      return;
    }

    if (seq !== this._seq) return; // a newer poll already superseded this one

    this.data = data;
    this.dataSpans = spans;
    this.render();
  }

  /** Backs undo of a mistap: click a ribbon tick to remove that live mark. */
  async removeFlag(frame) {
    try {
      const res = await fetch(`/api/flag?frame=${frame}`, { method: 'DELETE' });
      if (!res.ok) {
        const body = await res.json().catch(() => ({}));
        this.onToast?.(body.error || 'could not remove flag', 'bad');
        return;
      }
      this.poll(); // redraw the ribbon without waiting for the next tick
    } catch {
      this.onToast?.('could not remove flag', 'bad');
    }
  }

  render() {
    const d = this.data;
    if (!d) return;

    const T = d.ring_seconds;
    const A = d.edge_seconds;
    const denom = Math.log(T / A);
    const leftPct = (age) => (1 - Math.log(Math.max(age, A) / A) / denom) * 100;
    const abs = (s) => (s === 0 ? T : s);

    this.drawWave(decode(d.buckets));

    // Everything older than what is buffered is time that was never recorded,
    // not silence. Drawing it flat would read as "twelve minutes of quiet".
    this.hatch.style.width = `${leftPct(d.buffered_seconds).toFixed(2)}%`;

    const tiers = this.spans.map((s) => ({ s, age: abs(s) }));
    const sel = this.selected;

    this.bandLayer.textContent = '';
    for (const t of tiers) {
      if (t.s !== sel) continue;
      const band = add(this.bandLayer, 'div', 'rb-band');
      const l = leftPct(t.age);
      band.style.left = `${l.toFixed(2)}%`;
      band.style.width = `${(100 - l).toFixed(2)}%`;
    }

    this.markLayer.textContent = '';
    for (const t of tiers) {
      const mark = add(this.markLayer, 'div', 'rb-mark' + (t.s === sel ? ' on' : ''));
      mark.style.left = `${leftPct(t.age).toFixed(2)}%`;
    }

    // Rebuild labels but keep the fixed "now" at the right edge.
    for (const old of this.labelLayer.querySelectorAll('.rb-label:not(.rb-now-label)')) {
      old.remove();
    }
    tiers.forEach((t, i) => {
      const label = document.createElement('span');
      label.className = 'rb-label' + (t.s === sel ? ' on' : '');
      label.textContent = fmtDur(t.age);
      label.style.left = `${leftPct(t.age).toFixed(2)}%`;
      // The oldest label sits at 0% and would hang off the left edge if it
      // were centred like the rest.
      label.style.transform = i === tiers.length - 1 ? 'translateX(0)' : 'translateX(-50%)';
      this.labelLayer.appendChild(label);
    });

    // Live marks, drawn on the same log axis as everything else. The server
    // sends each mark as {age_seconds, frame}: age for the log-axis position,
    // in the same currency the rest of the ribbon already thinks in, and frame
    // so a click can undo a mistap with DELETE /api/flag?frame=N without a
    // second round trip. `d.flags` is absent on an older response shape,
    // hence the fallback.
    this.flagLayer.textContent = '';
    for (const f of d.flags || []) {
      const flag = add(this.flagLayer, 'div', 'rb-flag');
      flag.style.left = `${leftPct(f.age_seconds).toFixed(2)}%`;
      flag.title = `flag at ${fmtDur(f.age_seconds)} ago — click to remove`;
      flag.addEventListener('click', (e) => {
        e.stopPropagation();
        this.removeFlag(f.frame);
      });
    }

    this.readout.textContent = this.readoutText(d, abs(sel));
  }

  drawWave(bytes) {
    const n = bytes.length;
    if (!n) return;
    this.svg.setAttribute('viewBox', `0 0 ${Math.max(n - 1, 1)} 100`);

    const top = [];
    const bot = [];
    for (let i = 0; i < n; i++) {
      const a = (bytes[i] / 255) * 47; // 47 leaves room for the label scrim
      top.push(`${i},${(50 - a).toFixed(2)}`);
      bot.push(`${i},${(50 + a).toFixed(2)}`);
    }
    this.path.setAttribute('d', `M${top.join(' L')} L${bot.reverse().join(' L')} Z`);
  }

  readoutText(d, span) {
    if (d.buffered_seconds <= 0) return 'buffer empty';
    if (this.selected === null) return 'connecting';

    const i = this.dataSpans ? this.dataSpans.indexOf(this.selected) : -1;
    const signal = i >= 0 ? d.signal_seconds?.[i] : undefined;

    // Empty wins over under-buffered when both hold: if the buffer holds 90s,
    // the span is 7m and that 90s is silent, SILENT is the more actionable of
    // the two true statements. An unknown signal (index not found, or the
    // response predates the field) is not evidence of silence, so it reads as
    // a plain span — SILENT is the only line here that stops someone pressing
    // Capture, and it must earn that with positive evidence.
    if (signal === undefined) return `last ${fmtDur(span)}`;
    if (signal < 0.5) return `last ${fmtDur(span)} — SILENT, this would save nothing`;
    if (span > d.buffered_seconds + 0.5) {
      return `last ${fmtDur(span)} — only ${fmtDur(Math.round(d.buffered_seconds))} buffered`;
    }
    return `last ${fmtDur(span)}`;
  }
}
