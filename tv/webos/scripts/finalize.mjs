// Adds what webOS needs next to the Vite output: appinfo.json (version from package.json)
// and the launcher icons, drawn here so no binary files live in the repo.
import { readFileSync, writeFileSync } from 'node:fs';
import { deflateSync } from 'node:zlib';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const dist = join(root, 'dist');

const pkg = JSON.parse(readFileSync(join(root, 'package.json'), 'utf8'));
const appinfo = JSON.parse(readFileSync(join(root, 'appinfo.json'), 'utf8'));
appinfo.version = pkg.version;
writeFileSync(join(dist, 'appinfo.json'), JSON.stringify(appinfo, null, 2) + '\n');

const CRC_TABLE = Array.from({ length: 256 }, (_, n) => {
  let c = n;
  for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
  return c >>> 0;
});

function crc32(buf) {
  let c = 0xffffffff;
  for (const b of buf) c = CRC_TABLE[(c ^ b) & 0xff] ^ (c >>> 8);
  return (c ^ 0xffffffff) >>> 0;
}

function chunk(type, data) {
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length);
  const body = Buffer.concat([Buffer.from(type, 'ascii'), data]);
  const crc = Buffer.alloc(4);
  crc.writeUInt32BE(crc32(body));
  return Buffer.concat([len, body, crc]);
}

function png(size, pixel) {
  const raw = Buffer.alloc(size * (size * 4 + 1));
  for (let y = 0; y < size; y++) {
    const row = y * (size * 4 + 1);
    raw[row] = 0;
    for (let x = 0; x < size; x++) raw.set(pixel(x, y), row + 1 + x * 4);
  }
  const ihdr = Buffer.alloc(13);
  ihdr.writeUInt32BE(size, 0);
  ihdr.writeUInt32BE(size, 4);
  ihdr.set([8, 6, 0, 0, 0], 8);
  return Buffer.concat([
    Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a]),
    chunk('IHDR', ihdr),
    chunk('IDAT', deflateSync(raw)),
    chunk('IEND', Buffer.alloc(0)),
  ]);
}

// Netflix-style "N" (two bars and a diagonal) in unit coordinates
function inN(u, v) {
  const left = 0.27, right = 0.73, bar = 0.15, top = 0.14, bottom = 0.86;
  if (v < top || v > bottom || u < left || u > right) return 0;
  if (u <= left + bar || u >= right - bar) return u <= left + bar ? 1 : 2;
  const t = (v - top) / (bottom - top);
  const center = left + bar / 2 + t * (right - left - bar);
  return Math.abs(u - center) <= bar * 0.62 ? 3 : 0;
}

function icon(size) {
  const ss = 4;
  return png(size, (x, y) => {
    let hits = 0;
    let shade = 0;
    for (let i = 0; i < ss; i++) {
      for (let j = 0; j < ss; j++) {
        const part = inN((x + (i + 0.5) / ss) / size, (y + (j + 0.5) / ss) / size);
        if (part) {
          hits++;
          if (part === 3) shade++;
        }
      }
    }
    const a = hits / (ss * ss);
    // the diagonal is a brighter red, like the logo's ribbon
    const red = shade > hits / 2 ? [229, 9, 20] : [177, 6, 15];
    const bg = [20, 20, 20];
    return [0, 1, 2].map(k => Math.round(bg[k] * (1 - a) + red[k] * a)).concat(255);
  });
}

writeFileSync(join(dist, 'icon.png'), icon(80));
writeFileSync(join(dist, 'largeIcon.png'), icon(130));
console.log(`finalize: appinfo ${appinfo.id} ${appinfo.version}, icons written`);
