// RFC 6381 codec strings from the init segment (ftyp + moov) of a fragmented MP4, for
// MediaSource.addSourceBuffer. Pure parsing, no DOM.

interface Box { type: string; start: number; body: number; end: number }

function boxes(d: Uint8Array, start: number, end: number): Box[] {
  const out: Box[] = [];
  let p = start;
  while (p + 8 <= end) {
    let size = u32(d, p);
    const type = String.fromCharCode(d[p + 4], d[p + 5], d[p + 6], d[p + 7]);
    let body = p + 8;
    if (size === 1) {
      if (p + 16 > end) break;
      size = u32(d, p + 8) * 0x100000000 + u32(d, p + 12);
      body = p + 16;
    } else if (size === 0) {
      size = end - p;
    }
    if (size < body - p) break;
    out.push({ type, start: p, body, end: p + size });
    p += size;
  }
  return out;
}

const u32 = (d: Uint8Array, p: number) => ((d[p] << 24) >>> 0) + (d[p + 1] << 16) + (d[p + 2] << 8) + d[p + 3];
const hex2 = (n: number) => (n < 16 ? '0' : '') + n.toString(16).toUpperCase();

/** Byte length of ftyp..moov once the whole moov arrived; 0 while it is incomplete */
export function initSegmentEnd(d: Uint8Array): number {
  for (const b of boxes(d, 0, d.length)) {
    if (b.type === 'moov') return b.end <= d.length ? b.end : 0;
  }
  return 0;
}

function child(d: Uint8Array, b: Box | undefined, type: string, skip = 0): Box | undefined {
  return b ? boxes(d, b.body + skip, Math.min(b.end, d.length)).find(c => c.type === type) : undefined;
}

export interface TrackCodec {
  kind: 'video' | 'audio';
  codec: string;
  /** A plainer string of the same family when the exact one is refused */
  fallback?: string;
}

/** Codecs of every video / audio track in the init segment */
export function parseCodecs(d: Uint8Array): TrackCodec[] {
  const moov = boxes(d, 0, d.length).find(b => b.type === 'moov');
  if (!moov) return [];
  const out: TrackCodec[] = [];
  for (const trak of boxes(d, moov.body, Math.min(moov.end, d.length)).filter(b => b.type === 'trak')) {
    // stsd is a full box: version/flags + entry count
    const stsd = child(d, child(d, child(d, child(d, trak, 'mdia'), 'minf'), 'stbl'), 'stsd');
    if (!stsd) continue;
    const entry = boxes(d, stsd.body + 8, stsd.end)[0];
    if (!entry) continue;
    const c = sampleEntryCodec(d, entry);
    if (c) out.push(c);
  }
  return out;
}

const VIDEO_ENTRY = 78; // VisualSampleEntry fields before its child boxes

function sampleEntryCodec(d: Uint8Array, e: Box): TrackCodec | null {
  const t = e.type;
  const vchild = (type: string) => boxes(d, e.body + VIDEO_ENTRY, e.end).find(b => b.type === type);
  if (t === 'avc1' || t === 'avc3') {
    const c = vchild('avcC');
    if (!c) return { kind: 'video', codec: 'avc1.640028' };
    return { kind: 'video', codec: `${t}.${hex2(d[c.body + 1])}${hex2(d[c.body + 2])}${hex2(d[c.body + 3])}`, fallback: 'avc1.640028' };
  }
  if (t === 'hvc1' || t === 'hev1') {
    const c = vchild('hvcC');
    const codec = c ? hevcCodec(t, d, c.body) : `${t}.1.6.L153.B0`;
    const main10 = c && (d[c.body + 1] & 0x1f) === 2;
    return { kind: 'video', codec, fallback: main10 ? `${t}.2.4.L153.B0` : `${t}.1.6.L153.B0` };
  }
  if (t === 'dvh1' || t === 'dvhe' || t === 'dva1' || t === 'dvav') {
    const c = vchild('dvcC') || vchild('dvvC');
    if (!c) return { kind: 'video', codec: `${t}.08.06` };
    // dv_version_major, dv_version_minor, then profile (7 bits) and level (6 bits)
    const profile = d[c.body + 2] >> 1;
    const level = ((d[c.body + 2] & 1) << 5) | (d[c.body + 3] >> 3);
    const pad = (n: number) => (n < 10 ? '0' : '') + n;
    return { kind: 'video', codec: `${t}.${pad(profile)}.${pad(level)}` };
  }
  if (t === 'mp4a') {
    const body = e.body + audioEntryLength(d, e);
    const esds = boxes(d, body, e.end).find(b => b.type === 'esds');
    const aot = esds ? esdsCodec(d, esds.body + 4, esds.end) : '';
    return { kind: 'audio', codec: aot || 'mp4a.40.2', fallback: 'mp4a.40.2' };
  }
  if (t === 'Opus') return { kind: 'audio', codec: 'opus' };
  if (t === 'fLaC') return { kind: 'audio', codec: 'flac' };
  if (t === 'ac-3') return { kind: 'audio', codec: 'ac-3' };
  if (t === 'ec-3') return { kind: 'audio', codec: 'ec-3' };
  if (t === '.mp3') return { kind: 'audio', codec: 'mp3', fallback: 'mp4a.6B' };
  return null;
}

// AudioSampleEntry: 28 bytes in version 0; QuickTime versions 1 and 2 add 16 and 36
function audioEntryLength(d: Uint8Array, e: Box): number {
  const version = (d[e.body + 8] << 8) | d[e.body + 9];
  return version === 1 ? 44 : version === 2 ? 64 : 28;
}

// ISO/IEC 14496-15 Annex E: hvc1.<space><profile>.<compat reversed>.<tier><level>[.<constraint bytes>]
function hevcCodec(type: string, d: Uint8Array, p: number): string {
  const space = d[p + 1] >> 6;
  const tier = (d[p + 1] >> 5) & 1;
  const profile = d[p + 1] & 0x1f;
  let compat = u32(d, p + 2);
  let reversed = 0;
  for (let i = 0; i < 32; i++) {
    reversed = (reversed << 1) | (compat & 1);
    compat >>>= 1;
  }
  const constraints: string[] = [];
  for (let i = 0; i < 6; i++) constraints.push(hex2(d[p + 6 + i]));
  while (constraints.length && constraints[constraints.length - 1] === '00') constraints.pop();
  const level = d[p + 12];
  const parts = [type, `${['', 'A', 'B', 'C'][space]}${profile}`, (reversed >>> 0).toString(16).toUpperCase(), `${tier ? 'H' : 'L'}${level}`];
  return parts.concat(constraints).join('.');
}

// ES_Descriptor (3) > DecoderConfigDescriptor (4): object type; > DecoderSpecificInfo (5): audio object type
function esdsCodec(d: Uint8Array, start: number, end: number): string {
  let p = start;
  const readLen = () => {
    let len = 0;
    for (let i = 0; i < 4 && p < end; i++) {
      const b = d[p++];
      len = (len << 7) | (b & 0x7f);
      if (!(b & 0x80)) break;
    }
    return len;
  };
  if (d[p++] !== 3) return '';
  readLen();
  p += 2; // ES_ID
  const flags = d[p++];
  if (flags & 0x80) p += 2;
  if (flags & 0x40) p += 1 + d[p];
  if (flags & 0x20) p += 2;
  if (d[p++] !== 4) return '';
  readLen();
  const oti = d[p];
  if (oti === 0x6b || oti === 0x69) return 'mp3';
  if (oti !== 0x40) return `mp4a.${hex2(oti)}`;
  p += 13;
  if (d[p++] !== 5) return 'mp4a.40.2';
  readLen();
  let aot = d[p] >> 3;
  if (aot === 31) aot = 32 + (((d[p] & 7) << 3) | (d[p + 1] >> 5));
  return `mp4a.40.${aot}`;
}

/** MIME types to try for addSourceBuffer, most exact first */
export function mimeCandidates(tracks: TrackCodec[]): string[] {
  const video = tracks.find(t => t.kind === 'video');
  const audio = tracks.find(t => t.kind === 'audio');
  const vs = video ? uniq([video.codec, video.fallback]) : [''];
  const as = audio ? uniq([audio.codec, audio.fallback]) : [''];
  const out: string[] = [];
  for (const v of vs) for (const a of as) {
    const codecs = [v, a].filter(Boolean).join(',');
    if (codecs) out.push(`${video ? 'video' : 'audio'}/mp4; codecs="${codecs}"`);
  }
  return out;
}

const uniq = (xs: (string | undefined)[]) => xs.filter((x, i): x is string => !!x && xs.indexOf(x) === i);

// ---- Random access points of the live stream ----

interface ScanTrack { id: number; video: boolean; timescale: number; defaultFlags: number }

function tracksOf(moov: Uint8Array): ScanTrack[] {
  const top = boxes(moov, 0, moov.length)[0];
  if (!top || top.type !== 'moov') return [];
  const kids = boxes(moov, top.body, Math.min(top.end, moov.length));
  const trex = new Map<number, number>();
  const mvex = kids.find(b => b.type === 'mvex');
  if (mvex) {
    for (const b of boxes(moov, mvex.body, mvex.end)) if (b.type === 'trex') trex.set(u32(moov, b.body + 4), u32(moov, b.body + 20));
  }
  const out: ScanTrack[] = [];
  for (const trak of kids.filter(b => b.type === 'trak')) {
    const tkhd = child(moov, trak, 'tkhd');
    const mdia = child(moov, trak, 'mdia');
    const mdhd = child(moov, mdia, 'mdhd');
    const hdlr = child(moov, mdia, 'hdlr');
    if (!tkhd || !mdhd || !hdlr) continue;
    const id = u32(moov, tkhd.body + (moov[tkhd.body] === 1 ? 20 : 12));
    const timescale = u32(moov, mdhd.body + (moov[mdhd.body] === 1 ? 20 : 12));
    const handler = String.fromCharCode(moov[hdlr.body + 8], moov[hdlr.body + 9], moov[hdlr.body + 10], moov[hdlr.body + 11]);
    if (timescale > 0) out.push({ id, video: handler === 'vide', timescale, defaultFlags: trex.get(id) || 0 });
  }
  return out;
}

const NON_SYNC = 0x10000;

/**
 * Follows the top-level boxes of the stream as it arrives and reports the decode times (s) of the
 * fragments that start with a keyframe. MSE remove() also drops everything up to the next keyframe
 * after its end, so data can only be evicted up to one of these without hitting the playhead.
 */
export class KeyframeScanner {
  private hdr: number[] = [];
  private skip = 0;
  private box: Uint8Array | null = null;
  private fill = 0;
  private track: ScanTrack | null = null;
  /** Presentation time (s) of the first sample of the first fragment; -1 until one arrived */
  firstTime = -1;
  /** Decode time (s) of the latest fragment; -1 until one arrived */
  lastTime = -1;

  push(c: Uint8Array): number[] {
    const out: number[] = [];
    let p = 0;
    while (p < c.length) {
      if (this.skip > 0) {
        const n = Math.min(this.skip, c.length - p);
        this.skip -= n;
        p += n;
        continue;
      }
      if (this.box) {
        const box = this.box;
        const n = Math.min(box.length - this.fill, c.length - p);
        box.set(c.subarray(p, p + n), this.fill);
        this.fill += n;
        p += n;
        if (this.fill === box.length) {
          this.box = null;
          this.onBox(box, out);
        }
        continue;
      }
      this.hdr.push(c[p++]);
      if (this.hdr.length < 8) continue;
      const h = this.hdr;
      let size = ((h[0] << 24) >>> 0) + (h[1] << 16) + (h[2] << 8) + h[3];
      if (size === 1) {
        if (h.length < 16) continue;
        size = (((h[8] << 24) >>> 0) + (h[9] << 16) + (h[10] << 8) + h[11]) * 0x100000000 +
          ((h[12] << 24) >>> 0) + (h[13] << 16) + (h[14] << 8) + h[15];
      }
      const type = String.fromCharCode(h[4], h[5], h[6], h[7]);
      this.hdr = [];
      if (size === 0) {
        this.skip = Infinity;
      } else if ((type === 'moof' || type === 'moov') && size <= 4 * 1024 * 1024 && size >= h.length) {
        this.box = new Uint8Array(size);
        this.box.set(h);
        this.fill = h.length;
      } else {
        this.skip = Math.max(0, size - h.length);
      }
    }
    return out;
  }

  private onBox(b: Uint8Array, out: number[]) {
    const type = String.fromCharCode(b[4], b[5], b[6], b[7]);
    if (type === 'moov') {
      const tracks = tracksOf(b);
      this.track = tracks.find(t => t.video) || tracks[0] || null;
      return;
    }
    const tr = this.track;
    if (!tr) return;
    for (const traf of boxes(b, 8, b.length).filter(x => x.type === 'traf')) {
      const kids = boxes(b, traf.body, traf.end);
      const tfhd = kids.find(x => x.type === 'tfhd');
      const tfdt = kids.find(x => x.type === 'tfdt');
      const trun = kids.find(x => x.type === 'trun');
      if (!tfhd || !tfdt || !trun || u32(b, tfhd.body + 4) !== tr.id) continue;
      const hf = u32(b, tfhd.body) & 0xffffff;
      let q = tfhd.body + 8;
      if (hf & 0x1) q += 8;
      if (hf & 0x2) q += 4;
      if (hf & 0x8) q += 4;
      if (hf & 0x10) q += 4;
      let flags = hf & 0x20 ? u32(b, q) : tr.defaultFlags;
      const rf = u32(b, trun.body) & 0xffffff;
      let r = trun.body + 8;
      if (rf & 0x1) r += 4;
      if (rf & 0x4) {
        flags = u32(b, r);
      } else if (rf & 0x400) {
        let s = r;
        if (rf & 0x100) s += 4;
        if (rf & 0x200) s += 4;
        flags = u32(b, s);
      }
      const time = b[tfdt.body] === 1 ? u32(b, tfdt.body + 4) * 0x100000000 + u32(b, tfdt.body + 8) : u32(b, tfdt.body + 4);
      this.lastTime = time / tr.timescale;
      if (this.firstTime < 0) {
        // First sample: its composition offset follows the optional duration, size and flags (signed in version 1)
        let s = r + (rf & 0x4 ? 4 : 0);
        if (rf & 0x100) s += 4;
        if (rf & 0x200) s += 4;
        if (rf & 0x400) s += 4;
        const cto = rf & 0x800 ? (b[trun.body] === 1 ? u32(b, s) | 0 : u32(b, s)) : 0;
        this.firstTime = (time + cto) / tr.timescale;
      }
      if (flags & NON_SYNC) continue;
      out.push(time / tr.timescale);
    }
  }
}
