// Seamless source switch (upgrade / sidegrade) for the torrent player: the new source is
// loaded in a hidden, muted <video> a few seconds ahead of the visible one, and the player
// swaps elements when the visible video reaches the frame where the new stream starts.

import { capsQuery, runtimeCheckQuery, type StreamMeta, type StreamSource } from '@/features/watch/api';
import { normLang, type AudioTrackInfo } from '@/features/watch/subtitles';
import { bufferedEnd } from '@/features/watch/bufferHealth';

// subtitles: a release that carries the profile's subtitle, picked by the viewer
export type SwitchKind = 'upgrade' | 'sidegrade' | 'subtitles';
// busy: another preparation runs; unavailable: the visible stream can't hand over seamlessly;
// cancelled: the player dropped it (the visible stream recovered)
export type SwitchResult = 'switched' | 'abandoned' | 'cancelled' | 'busy' | 'unavailable';

// Video stream as the probe reports it; direct = sent untouched to this browser
export interface VideoInfo {
  codec: string;
  width: number;
  height: number;
  hdr: boolean;
  direct: boolean;
}

// Everything the player needs to adopt the hidden stream as its own
export interface PreparedStream {
  kind: SwitchKind;
  source: StreamSource;
  hash: string;
  fileId: number;
  duration: number;
  tracks: AudioTrackInfo[];
  audio: number;
  video: VideoInfo | null;
  /** Requested start (the play URL's t) and the real start (seekpoint) of the hidden stream */
  t: number;
  offset: number;
  url: string;
}

export interface SwitchHost {
  apiUrl: string;
  meta(): StreamMeta | undefined;
  /** The element on screen and the spare one the new stream loads into */
  visible(): HTMLVideoElement | null;
  hidden(): HTMLVideoElement | null;
  /** Where the visible stream starts in the file, and whether that is known exactly */
  offset(): number;
  offsetExact(): boolean;
  duration(): number;
  /** The visible stream is buffering although it should play */
  stalled(): boolean;
  /** Seconds the visible video has buffered past its position */
  bufferedAhead(): number;
  currentAudio(): AudioTrackInfo | undefined;
  defaultAudio(tracks: AudioTrackInfo[]): number;
  /** Adopt the hidden stream; called while the hidden element shows the frame at the visible position */
  swap(p: PreparedStream): void;
}

// The hidden stream starts this far ahead of the visible position (more after it fell behind).
// A TorBox file needs ~4 s for its seekpoint and ~4.5 s to its first bytes (every ffmpeg seek is
// a new CDN request), so 8 s were always overtaken before the first frame.
const LEAD_S = 8;
const LEAD_DEBRID_S = 20;
// Ready: this much buffered from the switch point (less once the browser stopped fetching on its own)
const READY_AHEAD_S = 5;
const READY_MIN_S = 2;
// A sidegrade starts inside the visible buffer (this much before its end, at least MIN_LEAD_S
// ahead), so it can take over before a draining stream runs dry
const SIDE_MARGIN_S = 2;
const MIN_LEAD_S = 3;
// A weak swarm gets this long to make the hidden stream ready
const READY_TIMEOUT_MS = 60000;
const MAX_LATE_RESTARTS = 3;
const MAX_SEEK_RESTARTS = 4;
// Switch when the visible frame is within this of the hidden one
const SWITCH_WINDOW_S = 0.05;
// The visible video passed the hidden frame by more than this: catch up inside the buffer or restart
const LATE_S = 0.25;
// Files whose lengths differ by more are different cuts; the same position would not match
const DURATION_TOLERANCE_S = 3;
const NEAR_END_S = 30;
// Whatever happens (long pause, a hidden seek that never finishes), a preparation ends by then
const MAX_PREP_MS = 5 * 60 * 1000;
// The hidden video must really play (time advancing) within this before it takes over
const HANDOVER_MS = 3000;
const HANDOVER_ADVANCE_S = 0.04;

type FrameCallbackVideo = HTMLVideoElement & {
  requestVideoFrameCallback?: (cb: (now: number, meta: { mediaTime: number }) => void) => number;
  cancelVideoFrameCallback?: (handle: number) => void;
};

const covers = (el: HTMLVideoElement, t: number, ahead: number) => bufferedEnd(el.buffered, t) - t >= ahead;

async function fetchJson(url: string, signal: AbortSignal, timeoutMs: number): Promise<{ ok: boolean; data: any }> {
  const ctrl = new AbortController();
  const abort = () => ctrl.abort();
  signal.addEventListener('abort', abort);
  const timer = setTimeout(abort, timeoutMs);
  try {
    const res = await fetch(url, { signal: ctrl.signal });
    const data = await res.json().catch(() => ({}));
    return { ok: res.ok, data };
  } finally {
    clearTimeout(timer);
    signal.removeEventListener('abort', abort);
  }
}

// Audio track of the new file: the current track's language (same channel count first),
// else the player's default rules
export function matchAudioTrack(tracks: AudioTrackInfo[], current: AudioTrackInfo | undefined, fallback: () => number): number {
  const lang = normLang(current?.language);
  const same = lang ? tracks.filter(t => normLang(t.language) === lang) : [];
  if (same.length === 0) return fallback();
  return (same.find(t => t.channels === current?.channels) || same[0]).index;
}

// One preparation. The player keeps at most one and tells it about seeks (onSeek).
export class SwitchPrep {
  readonly promise: Promise<SwitchResult>;
  private finish!: (r: SwitchResult) => void;
  private done = false;
  private ctrl = new AbortController();
  private info: { duration: number; tracks: AudioTrackInfo[]; video: VideoInfo | null } | null = null;
  // offset < 0: the seekpoint has not answered yet (it runs next to the play request)
  private stream: { t: number; offset: number; url: string; audio: number; startedAt: number } | null = null;
  // How far ahead of the visible position the hidden stream starts; grows when it was overtaken
  private lead: number;
  // Why the preparation was dropped (for the console and the server log)
  private why = '';
  // Bumped whenever the hidden stream is dropped, so stale async steps stop
  private gen = 0;
  private ready = false;
  // Buffered seconds from the switch point that count as ready (less for a sidegrade on a short buffer)
  private readyAhead = READY_AHEAD_S;
  private suspended = false;
  private seekingHidden = false;
  // The hidden video was told to play and has to prove it does before the swap
  private handover: { timer: ReturnType<typeof setInterval>; until: number } | null = null;
  // Waiting for the visible video to settle after a seek before loading again
  private waitSettle = false;
  private lateRestarts = 0;
  private seekRestarts = 0;
  // Extra lead after a keyframe came out too close to the visible position
  private farLead = 0;
  private farTries = 0;
  private deadline: ReturnType<typeof setTimeout> | null = null;
  private poll: ReturnType<typeof setInterval> | null = null;
  private lifetime: ReturnType<typeof setTimeout> | null = null;
  private framePoll: ReturnType<typeof setInterval> | null = null;
  private frame: { el: FrameCallbackVideo; handle: number } | null = null;
  private unlisten: (() => void) | null = null;

  constructor(private host: SwitchHost, readonly source: StreamSource, readonly kind: SwitchKind) {
    this.promise = new Promise(resolve => { this.finish = resolve; });
    this.lead = source.debrid ? LEAD_DEBRID_S : LEAD_S;
    this.lifetime = setTimeout(() => this.abandon('abandoned', 'not switched within 5 minutes'), MAX_PREP_MS);
    this.run();
  }

  get hash() {
    return this.source.hash.toLowerCase();
  }

  /** The hidden stream is buffered and waits for the visible video to reach its start */
  get isReady() {
    return this.ready;
  }

  /** Why an abandoned preparation was dropped ('' otherwise) */
  get reason() {
    return this.why;
  }

  cancel(result: 'abandoned' | 'cancelled' = 'abandoned') {
    this.abandon(result, result === 'cancelled' ? 'cancelled by the player' : 'dropped by the player');
  }

  // The visible stream restarts or seeks to target: drop the hidden stream unless it still lies ahead
  // force: the hidden stream no longer fits (another audio track was picked)
  onSeek(target: number, force = false) {
    if (this.done || !this.info || this.waitSettle) return;
    const el = this.host.hidden();
    const s = this.stream;
    // A hidden video already playing for the swap no longer lines up
    if (el && s && !force && !this.handover) {
      const start = (s.offset >= 0 ? s.offset : s.t) + Math.max(0, el.currentTime);
      if (start > target + 1 && start - target < this.lead * 2) return;
    }
    if (++this.seekRestarts > MAX_SEEK_RESTARTS) {
      this.abandon('abandoned', `${MAX_SEEK_RESTARTS} seeks during the preparation`);
      return;
    }
    this.dropStream();
    this.waitSettle = true;
    this.armDeadline();
  }

  private async run() {
    const { apiUrl } = this.host;
    const src = this.source;
    const hash = this.hash;
    try {
      // TorrServer may have dropped the torrent since the server offered it (a TorBox source
      // plays from TorBox and never goes through TorrServer)
      if (src.magnet && !src.debrid) {
        await fetchJson(`${apiUrl}/api/torrent-stream/files?magnet=${encodeURIComponent(src.magnet)}`, this.ctrl.signal, 30000).catch(() => null);
      }
      if (this.done) return;
      const { ok, data } = await fetchJson(
        `${apiUrl}/api/torrent-stream/probe?hash=${hash}&index=${src.index}${capsQuery()}${runtimeCheckQuery(this.host.meta())}`,
        this.ctrl.signal, 30000,
      );
      if (this.done) return;
      const dur = Number(data?.duration) || 0;
      const cur = this.host.duration();
      if (!ok || data.duration_mismatch || dur <= 0 || (cur > 0 && Math.abs(dur - cur) > DURATION_TOLERANCE_S)) {
        this.abandon('abandoned', !ok ? `probe failed (${data?.error || 'no answer'})`
          : data.duration_mismatch ? 'duration does not fit the title'
            : `length ${dur.toFixed(1)} s vs ${cur.toFixed(1)} s playing`);
        return;
      }
      this.info = {
        duration: dur,
        tracks: Array.isArray(data.audio_tracks) ? data.audio_tracks : [],
        video: data.width > 0 ? { codec: data.video_codec || '', width: data.width, height: data.height || 0, hdr: !!data.hdr, direct: !!data.direct } : null,
      };
    } catch (e) {
      this.abandon('abandoned', `probe failed (${(e as Error)?.name === 'AbortError' ? 'timeout' : 'network'})`);
      return;
    }
    this.armDeadline();
    // One slow loop for everything that has no event: settling after seeks, stalls, overtaking
    this.poll = setInterval(() => this.check(), 250);
    if (this.host.stalled() || !this.host.offsetExact()) this.waitSettle = true;
    else this.startStream();
  }

  private armDeadline() {
    if (this.deadline) clearTimeout(this.deadline);
    this.deadline = setTimeout(() => {
      if (!this.ready) this.abandon('abandoned', `not ready within ${READY_TIMEOUT_MS / 1000} s`);
    }, READY_TIMEOUT_MS);
  }

  private async startStream() {
    const info = this.info;
    const v = this.host.visible();
    const el = this.host.hidden();
    if (this.done || !info || !v || !el) return;
    const gen = ++this.gen;
    let lead = this.lead + this.farLead;
    if (this.kind === 'sidegrade') {
      // The visible stream may be draining: switch inside what it still has, with a smaller ready margin
      const ahead = this.host.bufferedAhead();
      lead = Math.min(lead, Math.max(MIN_LEAD_S, ahead - SIDE_MARGIN_S));
      this.readyAhead = ahead < LEAD_S + READY_AHEAD_S ? READY_MIN_S : READY_AHEAD_S;
    }
    const visiblePos = () => this.host.offset() + v.currentTime;
    const target = visiblePos() + lead;
    if (target > info.duration - NEAR_END_S) {
      this.abandon('abandoned', 'too close to the end');
      return;
    }
    const audio = info.tracks.length > 0
      ? matchAudioTrack(info.tracks, this.host.currentAudio(), () => this.host.defaultAudio(info.tracks))
      : 0;
    const q = `hash=${this.hash}&index=${this.source.index}&t=${target.toFixed(3)}&audio=${audio}${capsQuery()}`;
    const url = `${this.host.apiUrl}/api/torrent-stream/play?${q}`;
    // The play request starts at once; its real start (seekpoint) is asked next to it, since
    // waiting for one before the other costs seconds on a TorBox file
    this.stream = { t: target, offset: -1, url, audio, startedAt: Date.now() };
    this.ready = false;
    this.suspended = false;
    this.seekingHidden = false;
    el.muted = true;
    el.preload = 'auto';
    el.src = url;
    const onCheck = () => this.check();
    const onSuspend = () => { this.suspended = true; this.check(); };
    const onSeeked = () => { this.seekingHidden = false; this.check(); };
    const onError = () => { if (el.getAttribute('src')) this.abandon('abandoned', `hidden stream error ${el.error?.code ?? ''}`.trim()); };
    const events: [string, () => void][] = [
      ['progress', onCheck], ['loadeddata', onCheck], ['canplay', onCheck], ['canplaythrough', onCheck],
      ['suspend', onSuspend], ['seeked', onSeeked], ['error', onError],
    ];
    events.forEach(([n, f]) => el.addEventListener(n, f));
    this.unlisten = () => events.forEach(([n, f]) => el.removeEventListener(n, f));

    let offset = -1;
    try {
      const { ok, data } = await fetchJson(`${this.host.apiUrl}/api/torrent-stream/seekpoint?${q}`, this.ctrl.signal, 15000);
      if (ok && typeof data.t === 'number' && data.t >= 0 && data.t <= target + 1) offset = data.t;
    } catch {
      /* handled below */
    }
    if (this.done || gen !== this.gen || !this.stream) return;
    // Without the real start the frames can't be lined up
    if (offset < 0) {
      this.abandon('abandoned', 'seekpoint failed');
      return;
    }
    // Copied video starts on the keyframe before target; with long GOPs that can be close to
    // (or behind) the visible position, so start again further ahead
    if (offset < visiblePos() + lead / 2) {
      if (++this.farTries > 2) {
        this.abandon('abandoned', 'keyframe too close to the visible position');
        return;
      }
      this.farLead += lead;
      this.dropStream();
      this.startStream();
      return;
    }
    this.stream.offset = offset;
    this.check();
  }

  // Drop the hidden stream (seek, fell behind) but keep the probe
  private dropStream() {
    this.stopHandover();
    this.gen++;
    this.stopFrameWatch();
    this.unlisten?.();
    this.unlisten = null;
    this.stream = null;
    this.ready = false;
    this.seekingHidden = false;
    const el = this.host.hidden();
    if (el && el.getAttribute('src')) {
      el.removeAttribute('src');
      el.load();
    }
  }

  private check() {
    if (this.done || this.handover || !this.info) return;
    const v = this.host.visible();
    const el = this.host.hidden();
    if (!v || !el) return;
    if (this.waitSettle) {
      if (!this.host.stalled() && this.host.offsetExact() && v.readyState >= 2) {
        this.waitSettle = false;
        this.startStream();
      }
      return;
    }
    const s = this.stream;
    // Nothing lines up before the seekpoint answered
    if (!s || s.offset < 0) return;
    if (!this.ready) {
      // The visible stream stalled first: a hidden stream that already holds this moment takes over now
      if (el.readyState >= 2) {
        this.takeOverStall();
        if (this.done || this.seekingHidden) return;
      }
      if (el.readyState >= 3) {
        const ahead = bufferedEnd(el.buffered, el.currentTime) - el.currentTime;
        if (ahead >= this.readyAhead || (ahead >= READY_MIN_S && (el.readyState >= 4 || this.suspended))) {
          this.ready = true;
          if (this.deadline) clearTimeout(this.deadline);
          this.deadline = null;
          this.startFrameWatch(v);
        }
      }
      // The visible video got past everything the hidden one has: start further ahead
      if (!this.ready && this.host.offsetExact()) {
        const pos = this.host.offset() + v.currentTime;
        const end = Math.max(0, bufferedEnd(el.buffered, el.currentTime));
        if (pos > s.offset + end + LATE_S) this.restartLate();
      }
      return;
    }
    this.tick();
  }

  private restartLate() {
    if (++this.lateRestarts > MAX_LATE_RESTARTS) {
      this.abandon('abandoned', `overtaken ${MAX_LATE_RESTARTS} times (last lead ${Math.round(this.lead)} s)`);
      return;
    }
    // Start further ahead: at least as far as this try ran without getting ready
    const took = this.stream ? (Date.now() - this.stream.startedAt) / 1000 : 0;
    const step = this.source.debrid ? LEAD_DEBRID_S : LEAD_S;
    this.lead = Math.max(this.lead + step, Math.ceil(took + READY_AHEAD_S + 2));
    this.dropStream();
    this.startStream();
  }

  private startFrameWatch(v: HTMLVideoElement) {
    const fv = v as FrameCallbackVideo;
    if (typeof fv.requestVideoFrameCallback === 'function') {
      const onFrame = (_now: number, meta: { mediaTime: number }) => {
        if (this.done || !this.frame) return;
        this.frame.handle = fv.requestVideoFrameCallback!(onFrame);
        this.tick(meta.mediaTime);
      };
      this.frame = { el: fv, handle: fv.requestVideoFrameCallback(onFrame) };
    } else {
      this.framePoll = setInterval(() => this.tick(), 40);
    }
  }

  private stopFrameWatch() {
    if (this.frame) this.frame.el.cancelVideoFrameCallback?.(this.frame.handle);
    this.frame = null;
    if (this.framePoll) clearInterval(this.framePoll);
    this.framePoll = null;
  }

  // mediaTime: the presented frame of the visible video (requestVideoFrameCallback), else its currentTime
  private tick(mediaTime?: number) {
    const v = this.host.visible();
    const el = this.host.hidden();
    const s = this.stream;
    if (this.done || this.handover || !this.ready || !s || !v || !el || this.seekingHidden || !this.host.offsetExact()) return;
    const pos = this.host.offset() + (mediaTime ?? v.currentTime);
    const rel = pos - s.offset;
    if (this.takeOverStall()) return;
    if (v.paused) return;
    // The hidden element shows its first frame at the start of its buffer
    let hiddenAt = el.currentTime;
    if (bufferedEnd(el.buffered, hiddenAt) < 0 && el.buffered.length > 0) hiddenAt = el.buffered.start(0);
    const switchAt = s.offset + hiddenAt;
    if (pos < switchAt - SWITCH_WINDOW_S) return;
    if (pos <= switchAt + LATE_S) {
      this.swap();
      return;
    }
    // Overtaken: move the hidden element ahead inside its buffer, else start further ahead
    if (covers(el, rel + 0.4, 2)) this.seekHidden(el, rel + 0.4);
    else this.restartLate();
  }

  // While the visible stream stalls (true), the hidden one takes over as soon as it holds this moment
  private takeOverStall(): boolean {
    const v = this.host.visible();
    const el = this.host.hidden();
    const s = this.stream;
    if (!v || !el || !s || !this.host.stalled()) return false;
    if (this.seekingHidden || !this.host.offsetExact() || s.offset < 0) return true;
    const rel = this.host.offset() + v.currentTime - s.offset;
    if (!covers(el, rel, 1.5)) return true;
    if (Math.abs(el.currentTime - rel) > 0.08) this.seekHidden(el, rel);
    else this.swap();
    return true;
  }

  private seekHidden(el: HTMLVideoElement, rel: number) {
    this.seekingHidden = true;
    el.currentTime = rel;
  }

  // Start the hidden video (still muted and invisible) next to the visible one and swap only once
  // its time advances: the visible stream is never paused for a hidden one that does not play.
  // Both run in real time meanwhile, so they stay on the same frame.
  private swap() {
    const s = this.stream;
    const el = this.host.hidden();
    if (this.done || this.handover || !s || !this.info || !el) return;
    this.stopFrameWatch();
    const gen = this.gen;
    const from = el.currentTime;
    const fail = () => {
      if (gen !== this.gen || this.done) return;
      this.stopHandover();
      this.abandon('abandoned', 'the hidden video did not play at the handover');
    };
    el.play().catch(fail);
    const timer = setInterval(() => {
      if (gen !== this.gen || this.done) return this.stopHandover();
      if (!el.paused && el.readyState >= 3 && el.currentTime >= from + HANDOVER_ADVANCE_S) {
        this.stopHandover();
        this.commit();
      } else if (Date.now() > (this.handover?.until ?? 0) || el.error) {
        fail();
      }
    }, 20);
    this.handover = { timer, until: Date.now() + HANDOVER_MS };
  }

  private stopHandover() {
    if (this.handover) clearInterval(this.handover.timer);
    this.handover = null;
  }

  private commit() {
    const s = this.stream;
    const info = this.info;
    if (this.done || !s || !info) return;
    this.done = true;
    this.cleanup();
    this.host.swap({
      kind: this.kind,
      source: this.source,
      hash: this.hash,
      fileId: this.source.index,
      duration: info.duration,
      tracks: info.tracks,
      audio: s.audio,
      video: info.video,
      t: s.t,
      offset: s.offset,
      url: s.url,
    });
    this.finish('switched');
  }

  private abandon(result: 'abandoned' | 'cancelled' = 'abandoned', why = '') {
    if (this.done) return;
    this.done = true;
    this.why = why;
    this.cleanup();
    const el = this.host.hidden();
    if (el && el.getAttribute('src')) {
      el.removeAttribute('src');
      el.load();
    }
    this.finish(result);
  }

  private cleanup() {
    this.stopHandover();
    this.ctrl.abort();
    this.gen++;
    this.stopFrameWatch();
    this.unlisten?.();
    this.unlisten = null;
    if (this.deadline) clearTimeout(this.deadline);
    if (this.poll) clearInterval(this.poll);
    if (this.lifetime) clearTimeout(this.lifetime);
    this.lifetime = null;
    this.deadline = null;
    this.poll = null;
  }
}
