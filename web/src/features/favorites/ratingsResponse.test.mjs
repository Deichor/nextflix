import { test } from 'node:test';
import assert from 'node:assert/strict';
import { parseRatings } from './ratingsResponse.ts';

const json = (body, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

test('a server error is not read as "no ratings"', async () => {
  await assert.rejects(parseRatings(json({ error: 'db down' }, 500)));
  await assert.rejects(parseRatings(new Response('Bad Gateway', { status: 502 })));
});

test('a list answer is returned as is, anything else as empty', async () => {
  const rows = [{ media_type: 'movie', tmdb_id: 1, rating: 'love', favorite: true }];
  assert.deepEqual(await parseRatings(json(rows)), rows);
  assert.deepEqual(await parseRatings(json(null)), []);
  assert.deepEqual(await parseRatings(json({})), []);
});
