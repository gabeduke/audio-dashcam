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
