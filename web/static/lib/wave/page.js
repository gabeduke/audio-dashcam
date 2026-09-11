// web/static/lib/wave/page.js
// The waveform page: it owns the take's editable state (region, flags, grid,
// cursor), hands it to the view to paint, and persists changes through the
// sidecar API. The view and the clock own no take state of their own.
import { TileCache } from './tiles.js';
import { WaveView } from './view.js';
import { Clock } from './clock.js';
import { barBeat, fmtTime, framesPerBeat, clampRegion } from './geometry.js';

const $ = (id) => document.getElementById(id);
const file = new URLSearchParams(location.search).get('file');

function toast(msg, kind = 'ok', ms = 4000) {
  const t = document.createElement('div');
  t.className = `toast ${kind}`;
  t.textContent = msg;
  $('toasts').appendChild(t);
  setTimeout(() => t.remove(), ms);
}

function fail(msg) {
  $('wave-error').textContent = msg;
  $('wave-error').hidden = false;
  $('wave-canvas').hidden = true;
}

async function main() {
  if (!file) return fail('No take given.');
  const [jamsRes, peaksRes] = await Promise.all([
    fetch('/api/jams'),
    fetch(`/api/peaks?file=${encodeURIComponent(file)}`),
  ]);
  if (!jamsRes.ok) return fail('Could not load takes.');
  const take = (await jamsRes.json()).find((t) => t.name === file);
  if (!take) return fail('That take is gone.');
  if (!peaksRes.ok) return fail('This take has no waveform yet. Try again in a moment.');
  const filePeaks = await peaksRes.json();

  const sr = take.sample_rate || 48000;
  const total = Math.round(take.duration_seconds * sr);
  const minLen = Math.floor(sr * 3 / 1000) * 2 + 1;

  // --- state (the page owns it; the view reads it each draw) -------------
  const state = {
    region: take.trim ? { start: take.trim.start_frame, end: take.trim.end_frame } : null,
    flags: (take.flags || []).map((f) => ({ frame: f.frame, label: f.label || '' })),
    grid: { bpm: take.bpm || null, sampleRate: sr, downbeat: take.downbeat_frame || 0 },
    cursor: 0,
    selectedFlag: null,
  };

  $('wave-name').textContent = take.label || file.replace(/\.wav$/, '');
  $('wave-bpm').textContent = take.bpm ? `${take.bpm} BPM` : '';

  // --- pieces --------------------------------------------------------------
  const canvas = $('wave-canvas');
  const tiles = new TileCache({ file, totalFrames: total, filePeaks, onChange: () => view.draw() });
  const view = new WaveView({ canvas, tiles, totalFrames: total, sampleRate: sr, getState: () => state, emit });
  // A take whose preview has not landed yet has no URL to play: say so once
  // rather than fetching '/api/download?file=undefined' on the first tap.
  const previewUrl = take.preview_name
    ? `/api/download?file=${encodeURIComponent(take.preview_name)}`
    : '';
  const clock = new Clock({
    previewUrl,
    sampleRate: sr, file,
    onTick: (frame) => { state.cursor = frame; updateReadout(); view.draw(); },
    onError: (m) => toast(m, 'bad'),
  });

  // --- sidecar patches ----------------------------------------------------
  async function patch(body) {
    const res = await fetch(`/api/take?file=${encodeURIComponent(file)}`, {
      method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body),
    });
    if (!res.ok) {
      const b = await res.json().catch(() => ({}));
      throw new Error(b.error || `status ${res.status}`);
    }
    return res.json();
  }
  let regionTimer = 0;
  let pendingTrim = null;   // the body the debounced save will send, if any
  function saveRegion() {
    clearTimeout(regionTimer);
    pendingTrim = { trim: state.region ? { start_frame: state.region.start, end_frame: state.region.end } : null };
    regionTimer = setTimeout(() => {
      const body = pendingTrim;
      pendingTrim = null;
      patch(body).catch((e) => toast(`Could not save region: ${e.message}`, 'bad'));
    }, 300);
  }
  // A region dragged and then navigated away from within the debounce window
  // would otherwise be lost. sendBeacon cannot PATCH, so this is a keepalive
  // fetch: it outlives the document.
  function flushRegion() {
    if (!pendingTrim) return;
    clearTimeout(regionTimer);
    const body = pendingTrim;
    pendingTrim = null;
    fetch(`/api/take?file=${encodeURIComponent(file)}`, {
      method: 'PATCH', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(body), keepalive: true,
    }).catch(() => {});
  }
  function saveDownbeat() {
    patch({ downbeat_frame: state.grid.downbeat }).catch((e) => toast(`Could not save downbeat: ${e.message}`, 'bad'));
  }
  function saveFlags() {
    patch({ flags: state.flags }).then((b) => {
      if (b.cue_error) toast(b.cue_error, 'bad');
    }).catch((e) => toast(`Could not save flags: ${e.message}`, 'bad'));
  }
  // setLoop decodes a slice over the network. It can reject, and -- worse for
  // the button -- when the slice cannot be fetched it *resolves* after
  // quietly clearing its own loop and falling back to the preview. So the
  // only honest answer to "is a loop armed?" is clock.loop, read after the
  // await; the button follows that, never the request we made.
  function syncLoopButton() {
    loopOn = !!clock.loop;
    $('loop').setAttribute('aria-pressed', String(loopOn));
  }
  async function applyLoop(region) {
    try {
      await clock.setLoop(region);
    } catch (e) {
      toast(`Could not loop: ${e.message}`, 'bad');
    }
    syncLoopButton();
  }

  // --- view events --------------------------------------------------------
  function emit(ev, p) {
    switch (ev) {
      case 'seek': clock.seek(p.frame); state.cursor = p.frame; updateReadout(); view.draw(); break;
      case 'addFlag':
        if (state.flags.some((f) => f.frame === p.frame)) break;
        state.flags.push({ frame: p.frame, label: '' });
        state.flags.sort((a, b) => a.frame - b.frame);
        saveFlags(); view.draw(); break;
      case 'selectFlag': openSheet(p.flag); break;
      case 'regionChange':
        state.region = p.region; updateRegionRow(); view.draw();
        if (p.final) { saveRegion(); if (loopOn) applyLoop(state.region); }
        break;
      case 'downbeatChange':
        state.grid.downbeat = p.frame; view.draw();
        if (p.final) saveDownbeat();
        break;
      case 'viewChange': break;
    }
  }

  // --- flag sheet ---------------------------------------------------------
  const sheet = $('flag-sheet');
  function openSheet(flag) {
    state.selectedFlag = flag;
    $('flag-label').value = flag.label;
    sheet.hidden = false;
    $('flag-label').focus();
    view.draw();
  }
  function closeSheet(commit) {
    const f = state.selectedFlag;
    if (!f) return;
    if (commit) {
      const label = $('flag-label').value.trim();
      if (label !== f.label) { f.label = label; saveFlags(); }
    }
    state.selectedFlag = null;
    sheet.hidden = true;
    view.draw();
  }
  $('flag-done').addEventListener('click', () => closeSheet(true));
  $('flag-label').addEventListener('keydown', (e) => {
    if (e.key === 'Enter') closeSheet(true);
    if (e.key === 'Escape') closeSheet(false);
  });
  $('flag-delete').addEventListener('click', () => {
    const f = state.selectedFlag;
    if (!f) return;
    state.flags = state.flags.filter((x) => x.frame !== f.frame);
    state.selectedFlag = null;
    sheet.hidden = true;
    saveFlags(); view.draw();
  });

  // --- transport ----------------------------------------------------------
  let loopOn = false;
  $('play').addEventListener('click', async () => {
    if (clock.playing) { clock.pause(); $('play').textContent = 'Play'; }
    else { await clock.play(); $('play').textContent = 'Pause'; }
  });
  $('loop').addEventListener('click', async () => {
    if (!state.region) { toast('Set a region first'); return; }
    const next = !loopOn;
    // aria-pressed flips only after the loop is really armed (applyLoop reads
    // clock.loop), so a slice that failed to load leaves the button off.
    await applyLoop(next ? state.region : null);
    if (loopOn && !clock.playing) { await clock.play(); $('play').textContent = 'Pause'; }
  });

  // --- region row ---------------------------------------------------------
  const nudgeFrames = () => (state.grid.bpm ? Math.round(framesPerBeat(state.grid)) : Math.round(sr * 0.01));
  function setRegion(r, final = true) { emit('regionChange', { region: clampRegion(r, total, minLen), final }); }
  function clearRegion() {
    state.region = null;
    updateRegionRow();
    saveRegion();
    if (loopOn) applyLoop(null);   // applyLoop turns the button off with the loop
    view.draw();
  }
  $('region').addEventListener('click', () => {
    if (state.region) { clearRegion(); return; }
    const half = state.grid.bpm ? Math.round(framesPerBeat(state.grid) * 2) : Math.round(view.view.width * view.view.fpp / 2);
    setRegion({ start: state.cursor - half, end: state.cursor + half });
  });
  const nudge = (edge, sign) => () => {
    if (!state.region) return;
    const r = { ...state.region };
    r[edge] += sign * nudgeFrames();
    if (edge === 'start') r.start = Math.min(r.start, r.end - minLen);
    else r.end = Math.max(r.end, r.start + minLen);
    setRegion(r);
  };
  for (const [id, edge, sign] of [['start-dec', 'start', -1], ['start-inc', 'start', 1], ['end-dec', 'end', -1], ['end-inc', 'end', 1]]) {
    const b = $(id);
    const step = nudge(edge, sign);
    let hold = 0, rep = 0, repeated = false;
    // A press-and-hold repeats; the click that follows the release would
    // otherwise land one extra step on top of the repeats, so swallow it.
    b.addEventListener('click', () => { if (repeated) { repeated = false; return; } step(); });
    b.addEventListener('pointerdown', () => {
      repeated = false;
      hold = setTimeout(() => { repeated = true; rep = setInterval(step, 120); }, 500);
    });
    for (const ev of ['pointerup', 'pointercancel', 'pointerleave']) {
      b.addEventListener(ev, () => {
        clearTimeout(hold); clearInterval(rep);
        // Cleared on release, not on the next click: a release that slid off
        // the button fires no click, and a stale flag would then eat the
        // user's next tap.
        setTimeout(() => { repeated = false; }, 0);
      });
    }
  }
  function updateRegionRow() {
    const r = state.region;
    $('region').textContent = r ? 'Clear' : 'Region';
    $('export').disabled = !r;
    $('region-start').textContent = r ? fmtTime(r.start, sr) : '—';
    $('region-end').textContent = r ? fmtTime(r.end, sr) : '—';
    for (const id of ['start-dec', 'start-inc', 'end-dec', 'end-inc']) $(id).disabled = !r;
  }

  // --- export -------------------------------------------------------------
  $('export').addEventListener('click', async () => {
    if (!state.region) return;
    const btn = $('export');
    btn.disabled = true;
    try {
      const res = await fetch(`/api/cut?file=${encodeURIComponent(file)}`, {
        method: 'POST', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ start_frame: state.region.start, end_frame: state.region.end, label: '' }),
      });
      const body = await res.json().catch(() => ({}));
      if (!res.ok) throw new Error(body.error || `status ${res.status}`);
      // Built as nodes, not innerHTML: the name comes from the server and is
      // never markup here, whatever it contains.
      const t = document.createElement('div');
      t.className = 'toast ok';
      t.append('Saved as ');
      const a = document.createElement('a');
      a.href = `/wave.html?file=${encodeURIComponent(body.name)}`;
      a.textContent = body.name;
      t.appendChild(a);
      $('toasts').appendChild(t);
      setTimeout(() => t.remove(), 8000);
    } catch (e) {
      toast(`Export failed: ${e.message}`, 'bad');
    } finally {
      btn.disabled = !state.region;
    }
  });

  // --- zoom buttons & keyboard -------------------------------------------
  $('zoom-in').addEventListener('click', () => view.zoomTo(view.view.fpp / 2, view.view.width / 2));
  $('zoom-out').addEventListener('click', () => view.zoomTo(view.view.fpp * 2, view.view.width / 2));
  $('zoom-fit').addEventListener('click', () => view.fitAll());
  document.addEventListener('keydown', (e) => {
    // Typing a flag's name must never be read as transport shortcuts.
    const tag = e.target && e.target.tagName;
    if (tag === 'INPUT' || tag === 'TEXTAREA' || (e.target && e.target.isContentEditable)) return;
    switch (e.key) {
      case ' ': e.preventDefault(); $('play').click(); break;
      case 'l': case 'L': $('loop').click(); break;
      case 'f': case 'F': emit('addFlag', { frame: state.cursor }); break;
      // '[' sets the region start at the cursor, ']' the end; either one on
      // its own creates a region, so the pair works in either order.
      case '[':
        setRegion({
          start: state.cursor,
          end: state.region ? Math.max(state.region.end, state.cursor + minLen) : state.cursor + nudgeFrames() * 4,
        });
        break;
      case ']':
        setRegion({
          start: state.region ? Math.min(state.region.start, state.cursor - minLen) : state.cursor - nudgeFrames() * 4,
          end: state.cursor,
        });
        break;
      case '+': case '=': $('zoom-in').click(); break;
      case '-': $('zoom-out').click(); break;
      case 'ArrowLeft': view.panTo(view.view.start - view.view.width * view.view.fpp * 0.2); break;
      case 'ArrowRight': view.panTo(view.view.start + view.view.width * view.view.fpp * 0.2); break;
    }
  });

  // --- readout ------------------------------------------------------------
  function updateReadout() {
    $('pos-bar').textContent = barBeat(state.cursor, state.grid);
    $('pos-time').textContent = fmtTime(state.cursor, sr);
  }

  updateRegionRow();
  updateReadout();
  view.fitAll();
  // Two hooks, because neither alone covers a phone: pagehide is the one that
  // fires on navigation, and visibilitychange is the only one that reliably
  // fires when the app is switched away from or the screen locks.
  document.addEventListener('visibilitychange', () => { if (document.visibilityState === 'hidden') flushRegion(); });
  window.addEventListener('pagehide', () => { flushRegion(); clock.destroy(); tiles.stop(); view.destroy(); });
}

main().catch((e) => fail(e.message || String(e)));
