// Run in a node:24 container: node --experimental-strip-types --test src/lib/profileEdit.test.mjs
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { changedFields } from './profileEdit.ts';

const opened = { name: 'Emre', avatar: 'smile', color: '#E50914', language: 'tr' };

test('nothing changed sends nothing', () => {
  assert.deepEqual(changedFields(opened, { ...opened }), {});
});

test('only the changed fields are sent', () => {
  assert.deepEqual(changedFields(opened, { ...opened, avatar: 'robot' }), { avatar: 'robot' });
  assert.deepEqual(
    changedFields(opened, { ...opened, name: 'Deniz', language: 'en' }),
    { name: 'Deniz', language: 'en' },
  );
});
