import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readdirSync, readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';

const dir = fileURLToPath(new URL('.', import.meta.url));

test('watch code imports through the @/ alias, never relative paths', () => {
  const relative = readdirSync(dir, { recursive: true })
    .filter(f => /\.tsx?$/.test(f))
    .filter(f => /from\s+['"]\.\.?\//.test(readFileSync(dir + f, 'utf8')));
  assert.deepEqual(relative, []);
});
