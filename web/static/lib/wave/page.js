// web/static/lib/wave/page.js
// The waveform page: it owns the take's editable state (region, flags, grid,
// cursor), hands it to the view to paint, and persists changes through the
// sidecar API. The view and the clock own no take state of their own.
import { TileCache } from './tiles.js';
import { WaveView } from './view.js';
import { Overview } from './overview.js';
import { Clock } from './clock.js';
import { fmtRegionText, looksLikeMP3, canShareFiles, shareOrDownload } from './share.js';
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
  $('overview-canvas').hidden = true;
}

// Mirrors the server's render filename sanitiser (internal/audio/render.go).
// The server names every successful render through Content-Disposition; this
// only has to cover a response that somehow arrives without one.
function safeStem(base) {
  return base.replace(/[^\p{L}\p{N} \-_.]/gu, '').trim() || 'take';
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
  const tiles = new TileCache({
    file, totalFrames: total, filePeaks,
    onChange: () => view.draw(),
    // A 404 mid-session means the take was deleted under us. TileCache has
    // already stopped fetching; all that is left is to say so. The back link
    // in the top bar is always there, so the error block is the whole UI.
    onGone: () => fail('That take is gone.'),
  });
  // Declared before the view because WaveView's constructor resizes, which
  // fits, which emits 'viewChange' -- synchronously, before this line has
  // finished. `let overview = null` makes that early redraw a no-op; a `const`
  // assigned afterwards would throw on the temporal dead zone instead.
  let overview = null;
  function redraw() { view.draw(); if (overview) overview.draw(); }
  const view = new WaveView({ canvas, tiles, totalFrames: total, sampleRate: sr, getState: () => state, emit });
  overview = new Overview({
    canvas: $('overview-canvas'), filePeaks, totalFrames: total,
    getState: () => state, getView: () => view.view,
    emit: (ev, p) => {
      if (ev === 'panTo') view.panTo(p.start);
      else if (ev === 'centerOn') view.centerOn(p.frame);
      else if (ev === 'fitAll') view.fitAll();
    },
  });
  // A take whose preview has not landed yet has no URL to play: say so once
  // rather than fetching '/api/download?file=undefined' on the first tap.
  const previewUrl = take.preview_name
    ? `/api/download?file=${encodeURIComponent(take.preview_name)}`
    : '';
  const clock = new Clock({
    previewUrl,
    sampleRate: sr, file,
    onTick: (frame) => { state.cursor = frame; updateReadout(); redraw(); },
    onError: (m) => toast(m, 'bad'),
    onEnded: () => { $('play').textContent = 'Play'; },
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
  // The loop is implicit now: a region is a loop. setLoop decodes a slice over
  // the network, and when the slice cannot be fetched it *resolves* after
  // quietly clearing its own loop and falling back to the preview -- it says so
  // through onError, so there is nothing left here to report or to toggle.
  let loopTimer = 0;
  async function applyLoop(region) {
    // A direct call is the authority on what should be armed, so it cancels a
    // debounced one still in flight rather than letting it land afterwards.
    clearTimeout(loopTimer);
    // Re-arming what is already armed costs a fetch and a decode and, worse,
    // restarts the phrase under whoever is playing along to it.
    if (region && clock.loop && clock.loop.start === region.start && clock.loop.end === region.end) return;
    if (!region && !clock.loop) return;
    try {
      await clock.setLoop(region);
    } catch (e) {
      toast(`Could not loop: ${e.message}`, 'bad');
    }
  }
  // Dragging an edge and holding a nudge both emit a *final* region many times
  // over; only the one the hand settles on is worth a slice. Reads state.region
  // when it fires, not the region it was handed, so the last edit wins.
  function scheduleLoop() {
    clearTimeout(loopTimer);
    loopTimer = setTimeout(() => applyLoop(state.region), 300);
  }

  // --- view events --------------------------------------------------------
  function emit(ev, p) {
    switch (ev) {
      case 'seek':
        clock.seek(p.frame);
        // A region always loops. Seeking out of the slice makes the clock drop
        // its loop (clock.js seek()), which would leave the band, the text, the
        // x and the nudges all claiming a region that no longer plays as one.
        // The cursor stays where it was tapped; Play picks the loop back up.
        if (state.region && !clock.loop) applyLoop(state.region);
        state.cursor = p.frame; updateReadout(); redraw(); break;
      case 'addFlag':
        if (state.flags.some((f) => f.frame === p.frame)) break;
        state.flags.push({ frame: p.frame, label: '' });
        state.flags.sort((a, b) => a.frame - b.frame);
        saveFlags(); redraw(); break;
      case 'selectFlag': openSheet(p.flag); break;
      case 'regionChange':
        state.region = p.region; updateActionRow(); redraw();
        if (p.final) { saveRegion(); scheduleLoop(); }
        break;
      case 'downbeatChange':
        // The readout is bars and beats *counted from the downbeat*, so moving
        // the downbeat changes it even though the cursor has not moved.
        state.grid.downbeat = p.frame; updateReadout(); view.draw();
        if (p.final) saveDownbeat();
        break;
      // The first viewChange arrives from inside `new WaveView`, before the
      // overview exists; the guard is what makes that first one harmless.
      case 'viewChange': if (overview) overview.draw(); break;
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
    saveFlags(); redraw();
  });

  // --- transport ----------------------------------------------------------
  $('play').addEventListener('click', async () => {
    if (clock.playing) clock.pause();
    else await clock.play();
    // play() can fail (no preview yet, autoplay refused) and resolve anyway,
    // so the label follows the clock rather than what we asked it to do.
    $('play').textContent = clock.playing ? 'Pause' : 'Play';
  });
  // --- region -------------------------------------------------------------
  const nudgeFrames = () => (state.grid.bpm ? Math.round(framesPerBeat(state.grid)) : Math.round(sr * 0.01));
  function setRegion(r, final = true) { emit('regionChange', { region: clampRegion(r, total, minLen), final }); }
  function clearRegion() {
    state.region = null;
    updateActionRow();
    saveRegion();
    applyLoop(null);
    redraw();
  }
  $('region-clear').addEventListener('click', clearRegion);
  $('region-delete').addEventListener('click', clearRegion);
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
  function updateActionRow() {
    const r = state.region;
    $('region-text').textContent = fmtRegionText(r, sr);
    $('region-clear').hidden = !r;
    $('export').disabled = !r;
    $('region-delete').disabled = !r;
    for (const id of ['start-dec', 'start-inc', 'end-dec', 'end-inc']) $(id).disabled = !r;
  }

  $('downbeat-reset').addEventListener('click', () => {
    state.grid.downbeat = 0;
    patch({ downbeat_frame: null }).catch((e) => toast(`Could not reset downbeat: ${e.message}`, 'bad'));
    updateReadout();
    redraw();
  });

  // The disclosure remembers itself: someone who works with the nudges wants
  // them open on the next take too.
  const fine = $('fine');
  try { fine.open = localStorage.getItem('wave.fine') === '1'; } catch {}
  fine.addEventListener('toggle', () => { try { localStorage.setItem('wave.fine', fine.open ? '1' : '0'); } catch {} });

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

  // --- share --------------------------------------------------------------
  // The region (or the whole take) rendered to an MP3 and handed to the phone's
  // share sheet. Nothing is saved here: a share is a stream, not a cut.
  const shareBtn = $('share');
  // Decided once, up front: a browser that cannot hand a file to a share sheet
  // says "Download" from the start rather than surprising the user on tap.
  const shareLabel = canShareFiles() ? 'Share' : 'Download';
  shareBtn.textContent = shareLabel;
  shareBtn.addEventListener('click', async () => {
    const from = state.region ? state.region.start : 0;
    const to = state.region ? state.region.end : total;
    shareBtn.disabled = true;
    shareBtn.textContent = 'Rendering…';
    try {
      const res = await fetch(`/api/render?file=${encodeURIComponent(file)}&from=${from}&to=${to}`);
      if (!res.ok) {
        const b = await res.json().catch(() => ({}));
        throw new Error(b.error || `status ${res.status}`);
      }
      // The server names the file; this only has to survive a missing header.
      const m = /filename="([^"]+)"/.exec(res.headers.get('Content-Disposition') || '');
      const filename = m ? m[1] : `${safeStem(take.label || file.replace(/\.wav$/, ''))}.mp3`;
      const blob = await res.blob();
      // A render that died mid-stream is still a 200 -- the headers left before
      // ffmpeg did. Sniff the body rather than trust the status.
      const head = new Uint8Array(await blob.slice(0, 2048).arrayBuffer());
      if (!looksLikeMP3(head) || blob.size <= 1024) throw new Error('render failed, try again');
      const result = await shareOrDownload(blob, filename, filename.replace(/\.mp3$/, ''));
      // Only worth saying when the button promised a share sheet and the sheet
      // was not what happened; a plain Download button is its own message.
      if (result === 'downloaded' && shareLabel === 'Share') toast('Shared as a download');
    } catch (e) {
      toast(`${shareLabel} failed: ${e.message}`, 'bad');
    } finally {
      shareBtn.disabled = false;
      shareBtn.textContent = shareLabel;
    }
  });

  // --- keyboard -----------------------------------------------------------
  document.addEventListener('keydown', (e) => {
    // Typing a flag's name must never be read as transport shortcuts.
    const tag = e.target && e.target.tagName;
    if (tag === 'INPUT' || tag === 'TEXTAREA' || (e.target && e.target.isContentEditable)) return;
    switch (e.key) {
      case ' ': e.preventDefault(); $('play').click(); break;
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
      case '+': case '=': view.zoomTo(view.view.fpp / 2, view.view.width / 2); break;
      case '-': view.zoomTo(view.view.fpp * 2, view.view.width / 2); break;
      case '0': view.fitAll(); break;
      case 'ArrowLeft': view.panTo(view.view.start - view.view.width * view.view.fpp * 0.2); break;
      case 'ArrowRight': view.panTo(view.view.start + view.view.width * view.view.fpp * 0.2); break;
    }
  });

  // --- readout ------------------------------------------------------------
  function updateReadout() {
    $('pos-bar').textContent = barBeat(state.cursor, state.grid);
    $('pos-time').textContent = fmtTime(state.cursor, sr);
  }

  updateActionRow();
  updateReadout();
  view.fitAll();
  // A region is a loop, so a take reopened with one saved comes back looping
  // without anyone having to arm it again.
  if (state.region) applyLoop(state.region);
  // Two hooks, because neither alone covers a phone: pagehide is the one that
  // fires on navigation, and visibilitychange is the only one that reliably
  // fires when the app is switched away from or the screen locks.
  document.addEventListener('visibilitychange', () => { if (document.visibilityState === 'hidden') flushRegion(); });
  // flushRegion is the only thing that has to outlive the page; a pending loop
  // does not -- cancel it so it cannot arm a clock that has just been destroyed.
  window.addEventListener('pagehide', () => { clearTimeout(loopTimer); flushRegion(); clock.destroy(); tiles.stop(); view.destroy(); overview.destroy(); });
}

main().catch((e) => fail(e.message || String(e)));
