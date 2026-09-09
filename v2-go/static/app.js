// Audio Dashcam — app entry.

import { connectLive } from '/lib/live.js';
import { Visualizer, Meters, FLOOR_DB } from '/lib/meter.js';
import { TakesList } from '/lib/takes.js';
import { initWakeLock } from '/lib/wakelock.js';

const $ = (id) => document.getElementById(id);

const el = {
  awake: $('awake'),
  healthDot: $('health-dot'),
  healthText: $('health-text'),
  vizWrap: $('viz-wrap'),
  viz: $('viz'),
  meters: $('meters'),
  chanGrid: $('chan-grid'),
  chanSaving: $('chan-saving'),
  chanDevice: $('chan-device'),
  statBuffered: $('stat-buffered'),
  statDisk: $('stat-disk'),
  statXruns: $('stat-xruns'),
  durSeg: $('dur-seg'),
  captureBtn: $('capture-btn'),
  lastSaved: $('last-saved'),
  takes: $('takes'),
  takesEmpty: $('takes-empty'),
  toasts: $('toasts'),
  confirm: $('confirm'),
  confirmName: $('confirm-name'),
};

// ---------------------------------------------------------------- toasts

function toast(msg, kind = 'ok', ms = 4000) {
  const t = document.createElement('div');
  t.className = `toast ${kind}`;
  t.textContent = msg;
  el.toasts.appendChild(t);
  setTimeout(() => {
    t.style.transition = 'opacity 240ms';
    t.style.opacity = '0';
    setTimeout(() => t.remove(), 260);
  }, ms);
}

function confirmDelete(name) {
  return new Promise((resolve) => {
    if (typeof el.confirm.showModal !== 'function') {
      resolve(window.confirm(`Delete ${name}?`));
      return;
    }
    el.confirmName.textContent = name;
    el.confirm.returnValue = 'cancel';
    const done = () => {
      el.confirm.removeEventListener('close', done);
      resolve(el.confirm.returnValue === 'delete');
    };
    el.confirm.addEventListener('close', done);
    el.confirm.showModal();
  });
}

// ---------------------------------------------------------------- state

let status = null;
let viz = null;
let mainMeters = null;
let chanMeters = null;
let selSeconds = 30;
// Last capture error already surfaced, so a 2-second poll does not re-toast the
// same failure forever. Cleared on recovery, so a repeat failure toasts again.
let lastCaptureError = '';

const takes = new TakesList(el.takes, el.takesEmpty, {
  onToast: toast,
  onConfirm: confirmDelete,
});

// ---------------------------------------------------------------- status

function buildDurations(ringSeconds) {
  const opts = [];
  if (ringSeconds >= 30) opts.push({ s: 30, label: '30s' });
  if (ringSeconds >= 120) opts.push({ s: 120, label: '2m' });
  if (ringSeconds >= 420) opts.push({ s: 420, label: '7m' });
  opts.push({ s: 0, label: `Full ${fmtDur(ringSeconds)}` });

  // Prefer 30s; otherwise the shortest available.
  if (!opts.some((o) => o.s === selSeconds)) selSeconds = opts[0].s;

  el.durSeg.textContent = '';
  for (const o of opts) {
    const b = document.createElement('button');
    b.type = 'button';
    b.textContent = o.label;
    b.dataset.seconds = String(o.s);
    b.setAttribute('aria-pressed', String(o.s === selSeconds));
    b.addEventListener('click', () => {
      selSeconds = o.s;
      for (const other of el.durSeg.children) {
        other.setAttribute('aria-pressed', String(Number(other.dataset.seconds) === selSeconds));
      }
    });
    el.durSeg.appendChild(b);
  }
}

function fmtDur(s) {
  if (s >= 60) {
    const m = s / 60;
    return Number.isInteger(m) ? `${m}m` : `${m.toFixed(1)}m`;
  }
  return `${s}s`;
}

function buildChannelStrip(channels, saveChannels) {
  const labels = Array.from({ length: channels }, (_, i) => String(i + 1));
  const selected = saveChannels.map((c) => c - 1);
  el.chanGrid.textContent = '';
  chanMeters = new Meters(el.chanGrid, { labels, selected });
}

function applyStatus(s) {
  const first = status === null;
  const shapeChanged =
    first ||
    s.ring_seconds !== status.ring_seconds ||
    s.channels !== status.channels ||
    String(s.save_channels) !== String(status.save_channels);
  status = s;

  if (shapeChanged) {
    buildDurations(s.ring_seconds);
    buildChannelStrip(s.channels, s.save_channels);

    const sel = s.save_channels.map((c) => c - 1);
    el.meters?.replaceChildren();
    mainMeters = new Meters(el.meters, {
      labels: sel.length > 1 ? ['L', 'R'] : ['M'],
      selected: [],
    });
    viz?.setChannels(sel);

    el.chanSaving.textContent = s.save_channels.join(' & ');
    el.chanDevice.textContent = s.device || 'no device';
  }

  const healthy = s.capture_healthy;
  el.healthDot.className = `dot ${healthy ? 'ok' : 'bad'}`;

  // The bar is narrow and device errors are long ("Illegal combination of I/O
  // devices" and friends), so it carries a short status only. The full text
  // goes to a toast, which has the width for it, and to the title for a hover.
  const detail = healthy ? '' : (s.last_error || '');
  el.healthText.textContent = healthy ? 'recording' : (detail ? 'capture error' : 'no capture');
  el.healthText.title = detail;

  if (detail && detail !== lastCaptureError) toast(detail, 'bad', 8000);
  lastCaptureError = detail;

  el.vizWrap.classList.toggle('stale', !healthy);

  el.statBuffered.textContent = fmtDur(Math.round(s.buffered_seconds));
  el.statBuffered.className = s.buffered_seconds < 5 ? 'v warn' : 'v';

  const free = s.disk_free_gb;
  el.statDisk.textContent = `${free.toFixed(1)} GB`;
  el.statDisk.className = free < s.min_free_gb ? 'v bad' : free < s.min_free_gb * 3 ? 'v warn' : 'v';

  el.statXruns.textContent = String(s.xruns);
  el.statXruns.className = s.xruns > 0 ? 'v warn' : 'v';

  el.lastSaved.textContent = s.last_saved || 'none';

  el.captureBtn.disabled = !healthy || s.saving;
  if (s.saving) el.captureBtn.textContent = 'Saving…';
  else if (!healthy) el.captureBtn.textContent = 'No input';
  else el.captureBtn.textContent = 'Capture';

  // Channel strip reads its levels from /api/status so it stays live even
  // before the websocket has delivered a frame.
  if (chanMeters && s.channel_rms) {
    chanMeters.update(s.channel_rms, s.channel_rms, null);
  }
}

async function pollStatus() {
  try {
    const res = await fetch('/api/status', { cache: 'no-store' });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    applyStatus(await res.json());
  } catch {
    el.healthDot.className = 'dot bad';
    el.healthText.textContent = 'server unreachable';
    el.captureBtn.disabled = true;
  }
}

async function pollTakes(force = false) {
  // Never disturb the list while something is playing — this is what used to
  // kill playback every few seconds.
  if (!force && takes.isPlaying()) return;
  try {
    await takes.refresh();
  } catch {
    /* transient; the next tick retries */
  }
}

// ---------------------------------------------------------------- capture

async function capture() {
  const btn = el.captureBtn;
  btn.disabled = true;
  btn.textContent = 'Saving…';

  try {
    const res = await fetch(`/api/trigger?seconds=${selSeconds}`, { method: 'POST' });
    const body = await res.json().catch(() => ({}));

    if (!res.ok) {
      const msg = body.error || `HTTP ${res.status}`;
      toast(res.status === 507 ? `Disk full — ${msg}` : `Capture failed: ${msg}`, 'bad', 7000);
      return;
    }

    takes.markFresh(body.name);
    toast(`Saved ${body.name}`, 'ok');
    await pollTakes(true);
    await pollStatus();
  } catch (e) {
    toast(`Capture failed: ${e.message}`, 'bad', 7000);
  } finally {
    btn.textContent = 'Capture';
    btn.disabled = false;
  }
}

el.captureBtn.addEventListener('click', capture);

// ---------------------------------------------------------------- live

viz = new Visualizer(el.viz, { channels: [2, 3] });

connectLive({
  onFrame: (f) => {
    viz.push(f);
    if (!mainMeters || !status) return;
    const sel = status.save_channels.map((c) => c - 1);
    const last = f.bins?.[f.bins.length - 1];
    const rms = sel.map((c) => last?.rms?.[c] ?? FLOOR_DB);
    const peak = sel.map((c) => f.peak?.[c] ?? FLOOR_DB);
    const clip = sel.map((c) => f.clip?.[c] ?? false);
    mainMeters.update(rms, peak, clip);
    if (chanMeters && last?.rms) chanMeters.update(last.rms, f.peak, f.clip);
  },
});

// ---------------------------------------------------------------- loops

pollStatus().then(() => pollTakes(true));
setInterval(pollStatus, 2000);
setInterval(() => pollTakes(), 5000);

document.addEventListener('visibilitychange', () => {
  if (!document.hidden) {
    pollStatus();
    pollTakes();
  }
});

// Keep the screen awake while docked on a charger. Charging-only, so a phone
// on battery is untouched. Like the service worker below, this needs a secure
// context, so it engages on the tailnet HTTPS name and not over plain HTTP.
initWakeLock({
  onChange: (held) => {
    el.awake.hidden = !held;
  },
});

// A service worker cannot register over plain HTTP, which is how this device
// is reached on the LAN. Registering only in a secure context means the app
// picks it up automatically if TLS is ever added, without failing noisily now.
if ('serviceWorker' in navigator && window.isSecureContext) {
  navigator.serviceWorker.register('/sw.js').catch(() => {});
}
