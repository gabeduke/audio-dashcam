import { test } from 'node:test';
import assert from 'node:assert/strict';
import { fmtRegionText, looksLikeMP3, canShareFiles, shareOrDownload } from './share.js';

test('region text is tenths with an en dash, or whole take', () => {
  assert.equal(fmtRegionText({ start: 48000 * 12.34, end: 48000 * 41.87 }, 48000), '0:12.3 – 0:41.8');
  assert.equal(fmtRegionText({ start: 0, end: 48000 * 75 }, 48000), '0:00.0 – 1:15.0');
  assert.equal(fmtRegionText(null, 48000), 'whole take');
});

test('looksLikeMP3 accepts ID3 and frame-sync starts, rejects short or other bytes', () => {
  const big = (head) => { const b = new Uint8Array(2048); b.set(head); return b; };
  assert.equal(looksLikeMP3(big([0x49, 0x44, 0x33])), true);        // "ID3"
  assert.equal(looksLikeMP3(big([0xff, 0xfb, 0x90])), true);        // MPEG-1 layer III sync
  assert.equal(looksLikeMP3(big([0xff, 0xe0])), true);              // minimal sync
  assert.equal(looksLikeMP3(big([0x52, 0x49, 0x46, 0x46])), false); // "RIFF"
  assert.equal(looksLikeMP3(new Uint8Array([0x49, 0x44, 0x33])), false); // too short
  assert.equal(looksLikeMP3(null), false);
});

// --- the share sheet, stubbed --------------------------------------------
// share.js reads its globals at call time, so a browser is a few properties
// on globalThis rather than a DOM.
function stub(name, value) {
  const prev = Object.getOwnPropertyDescriptor(globalThis, name);
  Object.defineProperty(globalThis, name, { value, configurable: true, writable: true });
  return () => {
    if (prev) Object.defineProperty(globalThis, name, prev);
    else delete globalThis[name];
  };
}

function fakeDocument() {
  const clicks = [];
  const doc = {
    clicks,
    createElement: () => ({
      click() { clicks.push({ href: this.href, download: this.download }); },
      remove() {},
    }),
    body: { appendChild() {} },
  };
  return doc;
}

function browser({ secure = true, canShare = () => true, share = async () => {} } = {}) {
  const doc = fakeDocument();
  const undo = [
    stub('isSecureContext', secure),
    stub('navigator', { canShare, share }),
    stub('document', doc),
  ];
  return { doc, restore: () => { for (const u of undo.reverse()) u(); } };
}

test('canShareFiles needs a secure context, canShare and share', () => {
  let b = browser({ secure: false });
  try { assert.equal(canShareFiles(), false); } finally { b.restore(); }

  b = browser({ canShare: () => false });
  try { assert.equal(canShareFiles(), false); } finally { b.restore(); }

  b = browser({ share: null }); // null, not undefined: undefined takes the default
  try { assert.equal(canShareFiles(), false); } finally { b.restore(); }

  b = browser({ canShare: () => { throw new Error('nope'); } });
  try { assert.equal(canShareFiles(), false); } finally { b.restore(); }

  b = browser();
  try { assert.equal(canShareFiles(), true); } finally { b.restore(); }
});

test('shareOrDownload shares when it can, and says what it did', async () => {
  const shared = [];
  const b = browser({ share: async (data) => { shared.push(data); } });
  try {
    const blob = new Blob([new Uint8Array(2048)], { type: 'audio/mpeg' });
    assert.equal(await shareOrDownload(blob, 'jam 0.05-0.12.mp3', 'jam 0.05-0.12'), 'shared');
    assert.equal(shared.length, 1);
    assert.equal(shared[0].title, 'jam 0.05-0.12');
    assert.equal(shared[0].files[0].name, 'jam 0.05-0.12.mp3');
    assert.equal(shared[0].files[0].type, 'audio/mpeg');
    assert.equal(b.doc.clicks.length, 0);
  } finally { b.restore(); }
});

test('a dismissed share sheet is cancelled, and downloads nothing', async () => {
  const abort = Object.assign(new Error('share canceled'), { name: 'AbortError' });
  const b = browser({ share: async () => { throw abort; } });
  try {
    const blob = new Blob([new Uint8Array(2048)], { type: 'audio/mpeg' });
    assert.equal(await shareOrDownload(blob, 'jam.mp3', 'jam'), 'cancelled');
    assert.equal(b.doc.clicks.length, 0);
  } finally { b.restore(); }
});

test('any other share failure falls back to one download', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] }); // the revoke timer, not a real 10s wait
  const b = browser({ share: async () => { throw new Error('no transport'); } });
  try {
    const blob = new Blob([new Uint8Array(2048)], { type: 'audio/mpeg' });
    assert.equal(await shareOrDownload(blob, 'jam 0.05-0.12.mp3', 'jam'), 'downloaded');
    assert.equal(b.doc.clicks.length, 1);
    assert.equal(b.doc.clicks[0].download, 'jam 0.05-0.12.mp3');
    assert.match(b.doc.clicks[0].href, /^blob:/);
  } finally { b.restore(); }
});

test('a browser that cannot share files downloads without asking', async (t) => {
  t.mock.timers.enable({ apis: ['setTimeout'] });
  const b = browser({ canShare: () => false, share: async () => { throw new Error('should not be called'); } });
  try {
    const blob = new Blob([new Uint8Array(2048)], { type: 'audio/mpeg' });
    assert.equal(await shareOrDownload(blob, 'jam.mp3', 'jam'), 'downloaded');
    assert.equal(b.doc.clicks.length, 1);
    assert.equal(b.doc.clicks[0].download, 'jam.mp3');
  } finally { b.restore(); }
});
