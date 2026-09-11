// web/static/lib/wave/share.js
// What the action row says about the region, and (Task 6) how the region
// leaves the phone.
function tenths(frame, sr) {
  const t = Math.floor((frame / sr) * 10) / 10;
  const m = Math.floor(t / 60);
  const s = (t - m * 60).toFixed(1).padStart(4, '0');
  return `${m}:${s}`;
}

export function fmtRegionText(region, sampleRate) {
  if (!region) return 'whole take';
  return `${tenths(region.start, sampleRate)} – ${tenths(region.end, sampleRate)}`;
}

// A render that failed halfway is still a 200 with a short or empty body --
// the headers went out before ffmpeg spoke. So the body is sniffed rather
// than trusted: an ID3 tag or an MPEG frame sync, and enough of it to be a
// take rather than an error page.
export function looksLikeMP3(bytes) {
  if (!bytes || bytes.length <= 1024) return false;
  if (bytes[0] === 0x49 && bytes[1] === 0x44 && bytes[2] === 0x33) return true; // "ID3"
  return bytes[0] === 0xff && (bytes[1] & 0xe0) === 0xe0;                       // frame sync
}

// The share sheet needs a secure context and a browser that shares files.
// Probed with a tiny File so the button can say "Download" from the start on
// the plain LAN address instead of surprising the user on tap.
export function canShareFiles() {
  try {
    if (!globalThis.isSecureContext) return false;
    const nav = globalThis.navigator;
    // canShare without share is not a thing any browser ships, but calling a
    // missing share() would throw where returning false just downloads.
    if (!nav || typeof nav.canShare !== 'function' || typeof nav.share !== 'function') return false;
    const probe = new File([new Uint8Array(4)], 'probe.mp3', { type: 'audio/mpeg' });
    return nav.canShare({ files: [probe] }) === true;
  } catch { return false; }
}

function download(blob, filename) {
  const url = URL.createObjectURL(blob);
  const a = document.createElement('a');
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  a.remove();
  // Revoking straight away cancels the download in some browsers; ten seconds
  // is longer than any of them takes to read the blob.
  setTimeout(() => URL.revokeObjectURL(url), 10000);
}

// Hands the MP3 to the share sheet where there is one, and otherwise (or when
// the sheet fails for any reason other than the user waving it away) saves it.
// The File is built inside the branch that needs it so a browser without the
// constructor still downloads.
export async function shareOrDownload(blob, filename, title) {
  if (canShareFiles()) {
    try {
      const file = new File([blob], filename, { type: 'audio/mpeg' });
      await navigator.share({ files: [file], title });
      return 'shared';
    } catch (e) {
      // A sheet the user dismissed is not a failure and must stay silent.
      if (e && e.name === 'AbortError') return 'cancelled';
      // Anything else: fall through to a download, once.
    }
  }
  download(blob, filename);
  return 'downloaded';
}
