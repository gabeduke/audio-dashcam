// Takes list.
//
// The bug that made previews feel broken: the old UI rebuilt the whole table
// with `tbody.innerHTML = ''` every 5 seconds, destroying whatever <audio>
// element was mid-playback. Nothing here ever replaces the container's
// contents wholesale — rows are keyed by filename and patched in place, the
// list is skipped entirely when the server's ETag is unchanged, and polling
// pauses outright while something is playing.

import WaveSurfer from '/vendor/wavesurfer.esm.js';
import { ampToFrac } from '/lib/meter.js';

const fmtTime = (s) => {
  if (!isFinite(s) || s <= 0) return '0:00';
  const m = Math.floor(s / 60);
  const r = Math.round(s % 60);
  return `${m}:${String(r === 60 ? 59 : r).padStart(2, '0')}`;
};

const fmtSize = (mb) => (mb >= 1024 ? `${(mb / 1024).toFixed(1)} GB` : `${mb.toFixed(1)} MB`);

export class TakesList {
  constructor(container, emptyEl, { onToast, onConfirm }) {
    this.container = container;
    this.emptyEl = emptyEl;
    this.onToast = onToast;
    this.onConfirm = onConfirm;

    /** @type {Map<string, object>} name -> row state */
    this.rows = new Map();
    this.etag = null;
    this.playing = null; // name of the currently playing take
    this.fresh = null;
    this.reorderDeferred = false; // a render held back a move; see render()

    this.io = new IntersectionObserver(
      (entries) => {
        for (const e of entries) {
          if (!e.isIntersecting) continue;
          const name = e.target.dataset.name;
          const row = this.rows.get(name);
          if (row) this.mountWave(row);
          this.io.unobserve(e.target);
        }
      },
      { rootMargin: '160px' }
    );
  }

  /** True while audio is playing, so the caller can hold off polling. */
  isPlaying() {
    return this.playing !== null;
  }

  markFresh(name) {
    this.fresh = name;
  }

  // Same shape as confirmDelete below: drop the ETag and re-fetch. Metadata
  // writes are rare and user-initiated, the server owns ordering, and starring
  // reorders the list.
  async patchTake(name, patch) {
    const res = await fetch(`/api/take?file=${encodeURIComponent(name)}`, {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(patch),
    });
    if (!res.ok) {
      const msg = await res.json().then((j) => j.error).catch(() => res.statusText);
      throw new Error(msg);
    }
    const body = await res.json().catch(() => ({}));
    // The write has landed. A failure past this point is a stale list, not a
    // failed write, and must not be thrown to a caller whose catch says
    // "could not star" — the next poll reconciles.
    this.etag = null;
    try {
      await this.refresh();
    } catch { /* next poll picks it up */ }
    return body;
  }

  async refresh() {
    const headers = {};
    if (this.etag) headers['If-None-Match'] = this.etag;

    const res = await fetch('/api/jams', { headers, cache: 'no-store' });
    if (res.status === 304) return;
    if (!res.ok) throw new Error(`takes: HTTP ${res.status}`);

    this.etag = res.headers.get('ETag');
    const takes = await res.json();
    this.render(Array.isArray(takes) ? takes : []);
  }

  render(takes) {
    const seen = new Set();
    this.reorderDeferred = false;

    takes.forEach((t, i) => {
      seen.add(t.name);
      let row = this.rows.get(t.name);
      if (!row) {
        row = this.createRow(t);
        this.rows.set(t.name, row);
      }
      this.updateRow(row, t);

      // Keep DOM order matching server order without touching other nodes.
      // A row being renamed is left where it is. Moving a node blurs any
      // focused input inside it, and the blur handler commits — so a poll
      // that reordered the list would silently save half a name the user
      // never confirmed. The move is deferred to endEdit instead.
      const at = this.container.children[i];
      if (at !== row.el) {
        if (row.editing || row.editingBpm) this.reorderDeferred = true;
        else this.container.insertBefore(row.el, at ?? null);
      }
    });

    for (const [name, row] of this.rows) {
      if (seen.has(name)) continue;
      this.destroyRow(row);
      this.rows.delete(name);
    }

    const any = takes.length > 0;
    this.emptyEl.hidden = any;
  }

  createRow(t) {
    const el = document.createElement('article');
    el.className = 'take';
    el.dataset.name = t.name;
    el.innerHTML = `
      <div class="take-head">
        <button class="star" type="button" aria-pressed="false" aria-label="Star this take">★</button>
        <button class="take-name" type="button" title="Rename"></button>
        <input class="take-name-input" type="text" maxlength="120" hidden>
        <button class="take-bpm" type="button" title="Set the tempo"></button>
        <input class="take-bpm-input" type="text" inputmode="decimal" maxlength="7" hidden>
        <span class="take-meta"></span>
      </div>
      <div class="wave pending">waveform pending…</div>
      <div class="take-actions">
        <button class="icon-btn play" type="button">Play</button>
        <a class="icon-btn dl" download>WAV</a>
        <button class="icon-btn danger del" type="button">Delete</button>
      </div>`;

    const row = {
      name: t.name,
      el,
      nameEl: el.querySelector('.take-name'),
      nameInput: el.querySelector('.take-name-input'),
      starBtn: el.querySelector('.star'),
      bpmEl: el.querySelector('.take-bpm'),
      bpmInput: el.querySelector('.take-bpm-input'),
      editingBpm: false,
      editing: false,
      metaEl: el.querySelector('.take-meta'),
      waveEl: el.querySelector('.wave'),
      playBtn: el.querySelector('.play'),
      dlEl: el.querySelector('.dl'),
      delBtn: el.querySelector('.del'),
      ws: null,
      audio: null,
      mounting: false,
      waveUnavailable: false,
      data: t,
    };

    row.playBtn.addEventListener('click', () => this.togglePlay(row));
    row.delBtn.addEventListener('click', () => this.confirmDelete(row));

    // Attached once, here, rather than in mountWave: mountWave can run more
    // than once for a row (it's guarded, but callers don't know that), and a
    // second listener would turn one dblclick into two flags. row.waveEl is
    // the same element across a wave's whole life, mounted or not.
    //
    // This is a *double*-click, not a click: WaveSurfer's own click handler
    // seeks the playhead and never calls stopPropagation, so a single click
    // here would both seek and permanently write a flag -- there would be no
    // way left to scrub a take without marking it. WaveSurfer emits dblclick
    // but never treats it as a seek, so the two gestures coexist without
    // stepping on each other. Do not "simplify" this back to click.
    row.waveEl.addEventListener('dblclick', (e) => {
      if (e.target.classList.contains('take-flag')) return; // removal handled by the tick itself
      if (!row.ws) return; // no mounted waveform to flag against (pending/unavailable placeholders)
      const t = row.data;
      const duration = t.duration_seconds || 0;
      if (!duration) return; // a take whose sidecar is still landing has no frame axis yet
      const r = row.waveEl.getBoundingClientRect();
      const frac = Math.min(Math.max((e.clientX - r.left) / r.width, 0), 0.999999);
      const frame = Math.floor(frac * duration * (t.sample_rate || 48000));
      this.setFlags(row, [...(t.flags || []), { frame }]);
    });

    row.starBtn.addEventListener('click', async () => {
      const next = !row.data.starred;
      row.starBtn.disabled = true;
      try {
        await this.patchTake(row.name, { starred: next });
      } catch (err) {
        this.onToast?.(`Could not star: ${err.message}`, 'bad');
      } finally {
        row.starBtn.disabled = false;
      }
    });

    const beginEdit = () => {
      row.editing = true;
      row.nameInput.value = row.data.label || '';
      row.nameInput.placeholder = row.data.name.replace(/^jam_|\.wav$/g, '');
      row.nameEl.hidden = true;
      row.nameInput.hidden = false;
      row.nameInput.focus();
      row.nameInput.select();
    };

    const endEdit = async (commit) => {
      if (!row.editing) return;
      row.editing = false;
      row.nameInput.hidden = true;
      row.nameEl.hidden = false;

      const label = commit ? row.nameInput.value.trim() : null;
      if (commit && label !== (row.data.label || '')) {
        try {
          // patchTake re-fetches, which also flushes any deferred reorder.
          await this.patchTake(row.name, { label });
          return;
        } catch (err) {
          this.onToast?.(`Could not rename: ${err.message}`, 'bad');
        }
      }

      // Nothing was written, so nothing has re-rendered: put the list back in
      // server order if this edit held a move back.
      if (this.reorderDeferred) {
        this.etag = null;
        try {
          await this.refresh();
        } catch { /* next poll picks it up */ }
      }
    };

    row.nameEl.addEventListener('click', beginEdit);
    row.nameInput.addEventListener('blur', () => endEdit(true));
    row.nameInput.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') {
        e.preventDefault();
        row.nameInput.blur();
      } else if (e.key === 'Escape') {
        e.preventDefault();
        endEdit(false);
      }
    });

    const beginBpmEdit = () => {
      row.editingBpm = true;
      row.bpmInput.value = row.data.bpm == null ? '' : String(row.data.bpm);
      row.bpmInput.placeholder = 'bpm';
      row.bpmEl.hidden = true;
      row.bpmInput.hidden = false;
      row.bpmInput.focus();
      row.bpmInput.select();
    };

    const endBpmEdit = async (commit) => {
      if (!row.editingBpm) return;
      row.editingBpm = false;
      row.bpmInput.hidden = true;
      row.bpmEl.hidden = false;
      if (!commit) return;

      const raw = row.bpmInput.value.trim();
      // Empty clears the field. null is what the server reads as "clear";
      // sending 0 would store a tempo no take can have.
      const next = raw === '' ? null : Number(raw);
      if (next !== null && !Number.isFinite(next)) {
        this.onToast?.('Tempo must be a number', 'bad');
        return;
      }
      const before = row.data.bpm ?? null;
      if (next === before) return;

      try {
        await this.patchTake(row.name, { bpm: next });
      } catch (err) {
        this.onToast?.(`Could not set tempo: ${err.message}`, 'bad');
      }
    };

    row.bpmEl.addEventListener('click', beginBpmEdit);
    row.bpmInput.addEventListener('blur', () => endBpmEdit(true));
    row.bpmInput.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') {
        e.preventDefault();
        row.bpmInput.blur();
      } else if (e.key === 'Escape') {
        e.preventDefault();
        endBpmEdit(false);
      }
    });

    if (this.fresh === t.name) {
      el.classList.add('fresh');
      this.fresh = null;
      setTimeout(() => el.classList.remove('fresh'), 1400);
    }

    this.io.observe(el);
    return row;
  }

  updateRow(row, t) {
    row.data = t;
    row.starBtn.setAttribute('aria-pressed', t.starred ? 'true' : 'false');
    row.starBtn.classList.toggle('on', !!t.starred);
    // A take with no label still needs something to show, and the timestamp is
    // the only thing it has. Skip while the user is mid-edit so a poll cannot
    // overwrite what they are typing.
    if (!row.editing) {
      const stamp = t.name.replace(/^jam_|\.wav$/g, '');
      row.nameEl.textContent = t.label || stamp;
      row.nameEl.classList.toggle('unlabelled', !t.label);
    }
    // Skip while the user is mid-edit so a poll cannot overwrite what they are
    // typing -- the same reason the label is guarded above.
    if (!row.editingBpm) {
      row.bpmEl.textContent = t.bpm == null ? '+ bpm' : `${t.bpm.toFixed(1)} bpm`;
      row.bpmEl.classList.toggle('unset', t.bpm == null);
    }
    row.metaEl.textContent = `${fmtTime(t.duration_seconds)} · ${fmtSize(t.size_mb)}`;
    row.dlEl.href = `/api/download?file=${encodeURIComponent(t.name)}&dl=1`;

    const ready = t.has_preview;
    row.playBtn.disabled = !ready;
    if (!ready) {
      row.playBtn.textContent = 'Encoding…';
    } else if (row.ws && this.playing === row.name) {
      row.playBtn.textContent = 'Pause';
    } else {
      row.playBtn.textContent = 'Play';
    }

    // The waveform can only mount once its sidecar exists. row.waveUnavailable
    // stops this from retrying forever once mountWave has already settled on
    // "no peaks data" -- see mountWave's failure path below.
    if (t.has_peaks && !row.ws && !row.mounting && !row.waveUnavailable && this.isVisible(row.el)) {
      this.mountWave(row);
    }

    this.renderFlags(row);
  }

  // Draws every flag on this take as a tick over the waveform, at
  // frame / (duration * sample_rate) of the container's width. Runs from
  // updateRow, so every refresh redraws the layer from row.data rather than
  // patching it incrementally -- there is never a stale tick left behind.
  renderFlags(row) {
    const t = row.data;
    let layer = row.waveEl.querySelector('.take-flags');
    if (!layer) {
      layer = document.createElement('div');
      layer.className = 'take-flags';
      row.waveEl.appendChild(layer);
    }
    layer.textContent = '';

    // duration_seconds or sample_rate can be absent or zero while a take's
    // sidecar is still being written; without a frame axis there is nowhere
    // sane to draw a tick, so skip the take rather than divide by zero.
    const totalFrames = (t.duration_seconds || 0) * (t.sample_rate || 48000);
    if (!totalFrames) return;

    for (const f of t.flags || []) {
      const tick = document.createElement('div');
      tick.className = 'take-flag';
      tick.style.left = `${((f.frame / totalFrames) * 100).toFixed(3)}%`;
      tick.title = f.label ? `${f.label} — click to rename` : 'click to name';
      if (f.label) {
        const chip = document.createElement('span');
        chip.className = 'take-flag-label';
        chip.textContent = f.label;
        tick.appendChild(chip);
      }
      tick.addEventListener('click', (e) => {
        e.stopPropagation(); // otherwise the wave's own click handler reads this as a new flag
        this.editFlag(row, f, tick);
      });
      layer.appendChild(tick);
    }
  }

  // Opens an inline editor on a tick: a text input for the flag's label and
  // a remove button. Enter or blur commits, Escape cancels. The editor lives
  // inside the tick so it is positioned at the flag; only one is open at a
  // time because any refresh redraws the whole layer.
  editFlag(row, flag, tick) {
    if (tick.querySelector('.take-flag-edit')) return;
    const t = row.data;
    const box = document.createElement('div');
    box.className = 'take-flag-edit';
    // Flip the editor to the left of the tick near the right edge so it
    // stays inside the waveform.
    if (parseFloat(tick.style.left) > 65) box.classList.add('flip');
    box.innerHTML = `<input type="text" maxlength="120" placeholder="name this moment">
      <button type="button" class="take-flag-rm" title="Remove flag" aria-label="Remove flag">×</button>`;
    const input = box.querySelector('input');
    const rm = box.querySelector('.take-flag-rm');
    input.value = flag.label || '';
    let done = false;
    const finish = (commit) => {
      if (done) return;
      done = true;
      const label = input.value.trim();
      box.remove();
      if (commit && label !== (flag.label || '')) {
        this.setFlags(row, (t.flags || []).map((x) => (x.frame === flag.frame ? { frame: x.frame, label } : x)));
      }
    };
    input.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') { e.preventDefault(); finish(true); }
      else if (e.key === 'Escape') { e.preventDefault(); finish(false); }
    });
    input.addEventListener('blur', () => setTimeout(() => finish(true), 0));
    // The remove button must beat the input's blur (which would commit and
    // trigger a refresh that removes the button before it can be clicked).
    rm.addEventListener('mousedown', (e) => e.preventDefault());
    rm.addEventListener('click', (e) => {
      e.stopPropagation();
      done = true;
      box.remove();
      this.setFlags(row, (t.flags || []).filter((x) => x.frame !== flag.frame));
    });
    box.addEventListener('click', (e) => e.stopPropagation());
    box.addEventListener('dblclick', (e) => e.stopPropagation());
    tick.appendChild(box);
    input.focus();
    input.select();
  }

  async setFlags(row, flags) {
    try {
      // patchTake refreshes the list, which calls updateRow -> renderFlags
      // for every row, so the ticks redraw from the server's own answer
      // rather than from what was just clicked.
      const body = await this.patchTake(row.name, { flags });
      if (body.cue_error) {
        // The sidecar -- the source of truth -- saved fine; only the WAV's
        // cue chunk, a derived export, failed to update. Worth a toast, not a
        // thrown error that would read as the flag edit itself having failed.
        this.onToast?.(body.cue_error, 'bad');
      }
    } catch (e) {
      this.onToast?.(`Could not update flags: ${e.message}`, 'bad');
    }
  }

  isVisible(el) {
    const r = el.getBoundingClientRect();
    return r.bottom > -200 && r.top < window.innerHeight + 200;
  }

  async mountWave(row) {
    if (row.ws || row.mounting) return;
    const t = row.data;
    if (!t.has_peaks || !t.has_preview) return;
    row.mounting = true;

    let peaks = null;
    try {
      const res = await fetch(`/api/peaks?file=${encodeURIComponent(t.name)}`);
      if (res.ok) peaks = await res.json();
    } catch { /* fall through to a plain player */ }

    if (!peaks?.data?.length) {
      row.mounting = false;
      // Settle here rather than retry: without waveUnavailable, row.ws stays
      // unset, so the next updateRow calls mountWave again, and its async
      // continuation wipes the flag layer renderFlags just redrew -- an
      // indefinite flicker rather than a one-time failure.
      row.waveUnavailable = true;
      row.waveEl.textContent = 'waveform unavailable';
      this.renderFlags(row); // textContent above just wiped the flag layer
      return;
    }

    // preload="none" keeps a phone from pulling the mp3 until you press play;
    // the waveform is drawn from the precomputed peaks alone.
    const audio = new Audio();
    audio.preload = 'none';
    audio.src = `/api/download?file=${encodeURIComponent(t.preview_name)}`;

    row.waveEl.classList.remove('pending');
    row.waveEl.textContent = '';

    // Recode to the dB scale the meters and the ribbon use. The stored peaks
    // are linear amplitude, and this was the only level display in the app
    // still drawing them that way -- a MAIN-bus take at -25 dBFS rendered as a
    // one-pixel band here while the same signal filled 57% of the ribbon.
    //
    // Display-only, so peaks.json stays linear for trim and offline analysis,
    // and every take already on disk renders correctly without regeneration.
    // normalize stays false on purpose: the point is an absolute scale, so two
    // takes at the same level look the same. Normalising would make a whisper
    // and a full band identical.
    const shaped = peaks.data.map((chan) => Float32Array.from(chan, ampToFrac));

    const ws = WaveSurfer.create({
      container: row.waveEl,
      media: audio,
      peaks: shaped,
      duration: peaks.duration || t.duration_seconds,
      height: 48,
      waveColor: '#2c5f52',
      progressColor: '#34d399',
      cursorColor: '#eef2f8',
      cursorWidth: 1,
      barWidth: 2,
      barGap: 1,
      barRadius: 2,
      normalize: false,
      dragToSeek: true,
    });

    ws.on('play', () => {
      this.stopOthers(row.name);
      this.playing = row.name;
      row.playBtn.textContent = 'Pause';
    });
    ws.on('pause', () => {
      if (this.playing === row.name) this.playing = null;
      row.playBtn.textContent = 'Play';
    });
    ws.on('finish', () => {
      if (this.playing === row.name) this.playing = null;
      row.playBtn.textContent = 'Play';
    });
    ws.on('error', () => {
      this.onToast?.('Preview failed to load', 'bad');
      if (this.playing === row.name) this.playing = null;
      row.playBtn.textContent = 'Play';
    });

    // row.waveEl.textContent was just cleared to give WaveSurfer an empty
    // container, which also erased any flag layer an earlier updateRow had
    // drawn into the "pending" placeholder. Redraw it now that the container
    // holds the wave, or existing flags would stay invisible until the next
    // poll.
    this.renderFlags(row);

    row.ws = ws;
    row.audio = audio;
    row.mounting = false;
  }

  stopOthers(except) {
    for (const [name, row] of this.rows) {
      if (name !== except && row.ws?.isPlaying()) row.ws.pause();
    }
  }

  async togglePlay(row) {
    if (!row.ws) {
      await this.mountWave(row);
      if (!row.ws) return;
    }
    try {
      await row.ws.playPause();
    } catch (e) {
      this.onToast?.(`Playback blocked: ${e.message}`, 'bad');
    }
  }

  async confirmDelete(row) {
    const ok = await this.onConfirm?.(row.data.name);
    if (!ok) return;
    try {
      const res = await fetch(`/api/delete?file=${encodeURIComponent(row.data.name)}`, {
        method: 'DELETE',
      });
      if (!res.ok) throw new Error(`HTTP ${res.status}`);
      this.etag = null; // force the next refresh to re-render
      await this.refresh();
      this.onToast?.('Deleted');
    } catch (e) {
      this.onToast?.(`Delete failed: ${e.message}`, 'bad');
    }
  }

  destroyRow(row) {
    if (this.playing === row.name) this.playing = null;
    try { row.ws?.destroy(); } catch {}
    this.io.unobserve(row.el);
    row.el.remove();
  }
}
