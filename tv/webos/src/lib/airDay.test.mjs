// Run in a node:24 container: node --test src/lib/airDay.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { formatAirDay, parseLocalDay } from './airDay.ts';

// Thursday 22 October 2026, late evening local time
const now = new Date(2026, 9, 22, 23, 30);

test('YYYY-MM-DD is a local date, not UTC', () => {
  const d = parseLocalDay('2026-10-29');
  assert.deepEqual([d.getFullYear(), d.getMonth(), d.getDate(), d.getHours()], [2026, 9, 29, 0]);
  assert.equal(parseLocalDay('2026-10'), null);
  assert.equal(parseLocalDay(''), null);
});

test('within the next 6 days: the weekday', () => {
  assert.equal(formatAirDay('en-US', '2026-10-22', now), 'Thursday');
  assert.equal(formatAirDay('en-US', '2026-10-23', now), 'Friday');
  assert.equal(formatAirDay('en-US', '2026-10-28', now), 'Wednesday');
  assert.equal(formatAirDay('tr-TR', '2026-10-24', now), 'Cumartesi');
});

test('a week or more away (or past): day and month', () => {
  assert.equal(formatAirDay('en-US', '2026-10-29', now), 'October 29');
  assert.equal(formatAirDay('tr-TR', '2026-10-29', now), '29 Ekim');
  assert.equal(formatAirDay('en-US', '2026-10-21', now), 'October 21');
});

test('a date that does not parse: empty', () => {
  assert.equal(formatAirDay('en-US', 'soon', now), '');
});
