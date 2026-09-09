// Level meters, plus the shared dB and duration formatting helpers.
//
// The scrolling canvas visualiser that used to live here was replaced by the
// buffer ribbon (lib/ribbon.js): it could only ever show the ~9s that arrived
// while the page was open, and a dashcam is something you open after the
// moment.

const FLOOR_DB = -60;

function dbToFrac(db) {
  if (!isFinite(db) || db <= FLOOR_DB) return 0;
  return Math.min(1, (db - FLOOR_DB) / -FLOOR_DB);
}

/**
 * Maps a linear amplitude onto the same -60..0 dBFS scale the meters and the
 * buffer ribbon use, keeping the sign so a min/max pair still straddles the
 * centre line.
 *
 * Stored peaks are linear because that is what the audio actually is, and what
 * trim and any offline analysis want. Drawing them linearly is the mistake:
 * the MAIN bus sits around -25 dBFS, which is 5% of full scale, so a real take
 * renders as a one-pixel band while the same signal fills more than half the
 * ribbon. Same lesson the envelope already learned -- below about -20 dBFS,
 * linear amplitude looks like silence.
 */
function ampToFrac(v) {
  const a = Math.abs(v);
  if (a <= 0) return 0;
  // log10(0) is -Infinity, which dbToFrac already floors to 0.
  const f = dbToFrac(20 * Math.log10(a));
  return v < 0 ? -f : f;
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

/** Format a duration in seconds the way the capture tiers and stats read. */
export function fmtDur(s) {
  if (s >= 60) {
    const m = s / 60;
    return Number.isInteger(m) ? `${m}m` : `${m.toFixed(1)}m`;
  }
  return `${s}s`;
}

export { FLOOR_DB, dbToFrac, ampToFrac };
