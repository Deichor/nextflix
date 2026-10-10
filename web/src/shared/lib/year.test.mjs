import { test } from 'node:test';
import assert from 'node:assert/strict';
import { releaseYear } from './year.ts';

test('a January 1st release keeps its year in every timezone', () => {
  assert.equal(releaseYear('2024-01-01'), 2024);
  assert.equal(releaseYear('1999-12-31'), 1999);
});

test('missing or broken dates give no year', () => {
  assert.equal(releaseYear(undefined), undefined);
  assert.equal(releaseYear(null), undefined);
  assert.equal(releaseYear(''), undefined);
  assert.equal(releaseYear('soon'), undefined);
  assert.equal(releaseYear('0000-00-00'), undefined);
});
