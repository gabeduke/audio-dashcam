// Takes list.
//
// The bug that made previews feel broken: the old UI rebuilt the whole table
// with `tbody.innerHTML = ''` every 5 seconds, destroying whatever <audio>
// element was mid-playback. Nothing here ever replaces the container's
// contents wholesale — rows are keyed by filename and patched in place, the
// list is skipped entirely when the server's ETag is unchanged, and polling
// pauses outright while something is playing.

import WaveSurfer from '/vendor/wavesurfer.esm.js';

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
    // The write has landed. A failure past this point is a stale list, not a
    // failed write, and must not be thrown to a caller whose catch says
    // "could not star" — the next poll reconciles.
    this.etag = null;
    try {
      await this.refresh();
    } catch { /* next poll picks it up */ }
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
        if (row.editing) this.reorderDeferred = true;
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
      editing: false,
      metaEl: el.querySelector('.take-meta'),
      waveEl: el.querySelector('.wave'),
      playBtn: el.querySelector('.play'),
      dlEl: el.querySelector('.dl'),
      delBtn: el.querySelector('.del'),
      ws: null,
      audio: null,
      mounting: false,
      data: t,
    };

    row.playBtn.addEventListener('click', () => this.togglePlay(row));
    row.delBtn.addEventListener('click', () => this.confirmDelete(row));

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

    // The waveform can only mount once its sidecar exists.
    if (t.has_peaks && !row.ws && !row.mounting && this.isVisible(row.el)) {
      this.mountWave(row);
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
      row.waveEl.textContent = 'waveform unavailable';
      return;
    }

    // preload="none" keeps a phone from pulling the mp3 until you press play;
    // the waveform is drawn from the precomputed peaks alone.
    const audio = new Audio();
    audio.preload = 'none';
    audio.src = `/api/download?file=${encodeURIComponent(t.preview_name)}`;

    row.waveEl.classList.remove('pending');
    row.waveEl.textContent = '';

    const ws = WaveSurfer.create({
      container: row.waveEl,
      media: audio,
      peaks: peaks.data,
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
