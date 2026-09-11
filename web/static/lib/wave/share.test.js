import { test } from 'node:test';
import assert from 'node:assert/strict';
import { fmtRegionText } from './share.js';

test('region text is tenths with an en dash, or whole take', () => {
  assert.equal(fmtRegionText({ start: 48000 * 12.34, end: 48000 * 41.87 }, 48000), '0:12.3 – 0:41.8');
  assert.equal(fmtRegionText({ start: 0, end: 48000 * 75 }, 48000), '0:00.0 – 1:15.0');
  assert.equal(fmtRegionText(null, 48000), 'whole take');
});
