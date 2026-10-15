import { useCallback, useEffect, useRef, useState } from 'react';
import { AlertCircle, ArrowLeft, CalendarClock, ChevronsRight, Clapperboard, MessageSquare, Pause, Play, RotateCcw, RotateCw, ServerOff, SkipForward, WifiOff } from 'lucide-react';
import Spinner from '../components/Spinner';
import { img } from '../lib/http';
import { useI18n } from '../lib/i18n';
import { KEY, direction, isBack } from '../lib/webos';
import { episodeLabel, type PlayRequest } from '../lib/stream';
import { focusById, focusDefault, move, pushKeyHandler } from '../nav/spatial';
import { PlayerEngine, type PlayerState } from './engine';
import AudioSubtitlePanel from './AudioSubtitlePanel';
import SubtitleOverlay from './SubtitleOverlay';
import PauseScreen from './PauseScreen';
import TrickPlay, { useTrickPlayPrefetch } from './TrickPlay';
import { skipPhase, useSkipSegments } from './skipSegments';

const HIDE_MS = 4500;
// Paused this long without a key: the pause screen ("You're watching") replaces the controls
const PAUSE_SCREEN_MS = 6000;
const COMMIT_MS = 800;
// Scrub mode (a second Left / Right press, or one held): no auto-commit; OK seeks, Back cancels, and it
// closes by itself after this long without a key. Playback goes on underneath.
const SCRUB_IDLE_MS = 15000;

function formatTime(s: number): string {
  if (!isFinite(s) || s < 0) s = 0;
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = Math.floor(s % 60);
  return `${h > 0 ? `${h}:` : ''}${h > 0 ? String(m).padStart(2, '0') : m}:${String(sec).padStart(2, '0')}`;
}

// Remote: OK play/pause, left/right seek 10 s (held: 30 s, then 60 s steps), up shows the buttons,
// Back closes (or closes the Audio & Subtitles panel). Media keys work everywhere.
export default function Player({ req, onClose }: { req: PlayRequest; onClose: () => void }) {
  const i18n = useI18n();
  const { t, locale } = i18n;
  const videoRef = useRef<HTMLVideoElement>(null);
  const rootRef = useRef<HTMLDivElement>(null);
  const engineRef = useRef<PlayerEngine | null>(null);
  const [s, setS] = useState<PlayerState | null>(null);
  const [visible, setVisible] = useState(true);
  // 'skip': the Skip intro / end credits buttons (bottom right) have the focus
  const [zone, setZone] = useState<'timeline' | 'buttons' | 'skip'>('timeline');
  const [panel, setPanel] = useState(false);
  const [styling, setStyling] = useState(false);
  const [preview, setPreview] = useState<number | null>(null);
  const previewRef = useRef<number | null>(null);
  const repeatRef = useRef(0);
  const commitTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const [scrubMode, setScrubMode] = useState(false);
  const scrubModeRef = useRef(false);
  const idleTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const hideTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const stateRef = useRef<PlayerState | null>(null);
  stateRef.current = s;
  const panelRef = useRef(panel);
  panelRef.current = panel;
  const zoneRef = useRef(zone);
  zoneRef.current = zone;
  const [pauseScreen, setPauseScreen] = useState(false);
  const pauseScreenRef = useRef(false);
  pauseScreenRef.current = pauseScreen;
  const lastInputRef = useRef(0);

  useEffect(() => {
    const engine = new PlayerEngine(videoRef.current!, req, locale);
    engineRef.current = engine;
    setS(engine.state);
    const off = engine.subscribe(() => setS(engine.state));
    return () => {
      off();
      engine.destroy();
      engineRef.current = null;
    };
    // one engine per player
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const reveal = useCallback(() => {
    lastInputRef.current = Date.now();
    setPauseScreen(false);
    setVisible(true);
    clearTimeout(hideTimer.current);
    hideTimer.current = setTimeout(() => {
      const st = stateRef.current;
      if (panelRef.current || scrubModeRef.current || !st || !st.playing) return;
      setVisible(false);
      // Hidden controls: OK plays/pauses again
      if (zoneRef.current === 'buttons') focusById('pl-timeline');
    }, HIDE_MS);
  }, []);

  useEffect(() => { reveal(); return () => { clearTimeout(hideTimer.current); clearTimeout(commitTimer.current); clearTimeout(idleTimer.current); }; }, [reveal]);
  // Playback (re)started: the controls fade out after a while
  useEffect(() => { if (s?.playing) reveal(); }, [s?.playing, reveal]);

  const playable = !!s && s.phase === 'ready' && s.duration > 0 && !s.fatal;
  const overlay = !s ? 'loading'
    : s.net ? 'offline'
    : s.phase === 'resolving' ? 'resolving'
      : s.phase === 'error' ? 'error'
        : s.phase === 'unreleased' ? 'unreleased'
          : s.fatal ? 'fatal'
            : s.countdown !== null && s.ended ? 'countdown'
              : '';

  // Focus follows what is on screen: an overlay's buttons, else the timeline
  useEffect(() => {
    if (panel) return;
    const raf = requestAnimationFrame(() => {
      const root = rootRef.current;
      if (!root) return;
      if (overlay) focusDefault(root);
      else if (zoneRef.current === 'timeline' || !root.contains(document.activeElement)) focusById('pl-timeline');
    });
    return () => cancelAnimationFrame(raf);
  }, [overlay, panel, playable]);

  const endScrub = useCallback(() => {
    clearTimeout(commitTimer.current);
    clearTimeout(idleTimer.current);
    scrubModeRef.current = false;
    setScrubMode(false);
    previewRef.current = null;
    setPreview(null);
  }, []);

  // The source went away (another one is looked up, or a fatal error): nothing left to seek in
  useEffect(() => { if (!playable) endScrub(); }, [playable, endScrub]);

  const commit = useCallback(() => {
    const p = previewRef.current;
    endScrub();
    if (p !== null) engineRef.current?.seekTo(p);
  }, [endScrub]);

  // Back, Up / Down or no key for SCRUB_IDLE_MS: playback goes on where it is
  const cancelScrub = useCallback(() => {
    endScrub();
    reveal();
  }, [endScrub, reveal]);

  const scrub = useCallback((sign: 1 | -1, repeat: boolean) => {
    const engine = engineRef.current;
    const st = stateRef.current;
    if (!engine || !st || st.duration <= 0) return;
    repeatRef.current = repeat ? repeatRef.current + 1 : 0;
    const n = repeatRef.current;
    const step = n < 5 ? 10 : n < 15 ? 30 : 60;
    const from = previewRef.current ?? engine.positionNow();
    const next = Math.max(0, Math.min(st.duration - 1, from + sign * step));
    // A single press seeks after COMMIT_MS; a second press within it, or a held key, starts scrub mode
    if (!scrubModeRef.current && (repeat || previewRef.current !== null)) {
      scrubModeRef.current = true;
      setScrubMode(true);
    }
    previewRef.current = next;
    setPreview(next);
    clearTimeout(commitTimer.current);
    clearTimeout(idleTimer.current);
    if (scrubModeRef.current) idleTimer.current = setTimeout(cancelScrub, SCRUB_IDLE_MS);
    else commitTimer.current = setTimeout(commit, COMMIT_MS);
  }, [commit, cancelScrub]);

  const closePanel = useCallback(() => {
    setPanel(false);
    setStyling(false);
    reveal();
    requestAnimationFrame(() => focusById('pl-audio'));
  }, [reveal]);

  // All keys go to the player while it is open
  useEffect(() => pushKeyHandler(e => {
    const engine = engineRef.current;
    const st = stateRef.current;
    if (!engine || !st) return true;
    const code = e.keyCode;
    const canPlay = st.phase === 'ready' && st.duration > 0 && !st.fatal;
    const woke = pauseScreenRef.current;
    reveal();
    // The first key on the pause screen only brings the controls back (media keys still act)
    if (woke && code !== KEY.STOP && code !== KEY.PLAY && code !== KEY.PAUSE && code !== KEY.PLAY_PAUSE &&
      code !== KEY.FAST_FORWARD && code !== KEY.REWIND) return true;

    if (code === KEY.STOP) { onClose(); return true; }
    if (code === KEY.PLAY) { engine.play(); return true; }
    if (code === KEY.PAUSE) { engine.pause(); return true; }
    if (code === KEY.PLAY_PAUSE) { engine.togglePlay(); return true; }
    if (code === KEY.FAST_FORWARD || code === KEY.REWIND) {
      if (canPlay) scrub(code === KEY.FAST_FORWARD ? 1 : -1, e.repeat);
      return true;
    }
    if (scrubModeRef.current) {
      if (isBack(e)) { cancelScrub(); return true; }
      if (code === KEY.ENTER) { commit(); return true; }
      const d = direction(e);
      if (d === 'up' || d === 'down') { cancelScrub(); return true; }
    }
    if (isBack(e)) {
      if (panelRef.current) closePanel();
      else onClose();
      return true;
    }
    const dir = direction(e);
    const onTimeline = !panelRef.current && canPlay && st.countdown === null && zoneRef.current === 'timeline';
    // On a lone Skip button Left / Right still seek
    if (!onTimeline && !panelRef.current && canPlay && st.countdown === null && zoneRef.current === 'skip' && (dir === 'left' || dir === 'right')) {
      focusById('pl-timeline');
      scrub(dir === 'right' ? 1 : -1, e.repeat);
      return true;
    }
    if (onTimeline) {
      if (dir === 'left' || dir === 'right') scrub(dir === 'right' ? 1 : -1, e.repeat);
      else if (dir === 'up') move('up');
      else if (code === KEY.ENTER) {
        if (previewRef.current !== null) commit();
        else engine.togglePlay();
      }
      return true;
    }
    if (dir) {
      move(dir);
      return true;
    }
    if (code === KEY.ENTER) {
      const el = document.activeElement as HTMLElement | null;
      if (el && el.hasAttribute('data-nav')) el.click();
      return true;
    }
    return true;
  }), [onClose, reveal, scrub, commit, cancelScrub, closePanel]);

  useTrickPlayPrefetch(
    () => engineRef.current?.currentFile() ?? null,
    () => videoRef.current,
    () => engineRef.current?.positionNow() ?? 0,
    s?.duration ?? 0,
    !!s && s.phase === 'ready' && s.playing,
  );

  // Skip intro / end credits (skipSegments.ts)
  const skipSegs = useSkipSegments(s?.meta, playable ? engineRef.current?.currentFile() ?? null : null, s?.duration ?? 0);
  const skip = s && playable && preview === null ? skipPhase(skipSegs, s.position, s.duration) : null;
  // During the credits: a countdown card, or a Next button with autoplay off or after "Watch credits"
  const creditsCard = !!s && !!s.next && !s.ended && (s.countdown !== null || skip === 'credits');
  const skipShown = skip === 'intro' && !overlay ? 'intro' : creditsCard && !overlay ? 'credits' : null;
  const creditsDismissed = useRef(false);
  useEffect(() => {
    if (skip !== 'credits') {
      creditsDismissed.current = false;
      // Seeked back out of the credits (or away from the end): no next episode countdown
      if (!s?.ended) engineRef.current?.stopCountdown();
      return;
    }
    if (!creditsDismissed.current && s?.playing) engineRef.current?.creditsStarted();
  }, [skip, s?.playing, s?.ended]);
  // The Skip / Next button takes the focus when it appears and gives it back when it goes
  useEffect(() => {
    if (panel) return;
    const raf = requestAnimationFrame(() => {
      if (skipShown) focusById('pl-skip');
      else if (zoneRef.current === 'skip' && !overlay) focusById('pl-timeline');
    });
    return () => cancelAnimationFrame(raf);
  }, [skipShown, panel, overlay]);

  // Paused by the viewer with nothing else on screen: the pause screen after PAUSE_SCREEN_MS without a key
  const idlePaused = playable && !overlay && !!s && !s.playing && !s.buffering && !s.ended && s.countdown === null &&
    !panel && preview === null && !skipShown;
  useEffect(() => {
    if (!idlePaused) {
      setPauseScreen(false);
      return;
    }
    lastInputRef.current = Date.now();
    const timer = setInterval(() => {
      if (Date.now() - lastInputRef.current >= PAUSE_SCREEN_MS) setPauseScreen(true);
    }, 500);
    return () => clearInterval(timer);
  }, [idlePaused]);

  const engine = engineRef.current;
  const subtitleTime = useCallback(() => engineRef.current?.subtitleTime() ?? 0, []);
  if (!s) {
    return (
      <div className="player" ref={rootRef} data-nav-layer="player">
        <video ref={videoRef} className="player-video" playsInline />
      </div>
    );
  }

  const m = s.meta;
  const subtitle = m.media_type === 'tv' ? [episodeLabel(locale, m.season, m.episode), m.episode_title].filter(Boolean).join(' · ') : '';
  const shown = preview ?? s.position;
  const pct = s.duration > 0 ? Math.min(100, (shown / s.duration) * 100) : 0;
  const controls = playable && !pauseScreen && (visible || !s.playing || panel || preview !== null);
  const thumbFile = preview !== null ? engine?.currentFile() ?? null : null;
  const nextLabel = s.next ? episodeLabel(locale, s.next.season, s.next.episode) : '';
  const errorText = (e: { key?: Parameters<typeof t>[0]; text?: string } | null) => (e ? (e.key ? t(e.key) : e.text || '') : '');
  const releaseDate = s.unreleased
    ? s.unreleased.status === 'in_theaters' ? s.unreleased.digital_date : s.unreleased.release_date || s.unreleased.digital_date
    : null;

  return (
    <div className={`player ${controls ? 'show-controls' : ''} ${preview !== null && thumbFile ? 'is-scrubbing' : ''} ${styling ? 'is-styling' : ''}`} ref={rootRef} data-nav-layer="player" onMouseMove={reveal}>
      <video ref={videoRef} className="player-video" playsInline onClick={() => engine?.togglePlay()} />

      {playable && ((s.subId && s.cues.length > 0) || styling) && (
        <SubtitleOverlay
          cues={s.subId ? s.cues : []}
          time={subtitleTime}
          raised={!!controls && !styling}
          sample={styling ? t('subStyle.sample') : undefined}
          previewing={styling}
        />
      )}

      {playable && <PauseScreen meta={m} shown={pauseScreen} />}

      {(overlay === 'resolving' || overlay === 'loading' || overlay === 'error' || overlay === 'unreleased' || overlay === 'offline') && (
        <div className="player-cover">
          {!!m.backdrop_path && <img className="player-cover-img" src={img(m.backdrop_path, 'w1280')} alt="" />}
          <div className="player-cover-shade" />
          <div className="player-cover-body">
            <h1 className="player-cover-title">{m.title}</h1>
            {!!subtitle && <p className="player-cover-sub">{subtitle}</p>}
            {overlay === 'resolving' && s.resolveElapsed >= 300 && (
              <>
                <Spinner />
                <p className="player-status">{t(s.stage)}</p>
                <p className="player-status-sub">{s.resolveElapsed >= 30000 ? t('player.stillSearching') : ''}</p>
              </>
            )}
            {overlay === 'error' && (
              <>
                <AlertCircle className="player-alert" />
                <p className="player-status">{t(m.media_type === 'tv' ? 'player.couldNotStartEpisode' : 'player.couldNotStartTitle')}</p>
                <p className="player-status-sub">{errorText(s.error)}</p>
                <div className="player-dialog-buttons">
                  <button type="button" className="btn btn-primary" data-nav data-nav-default onClick={() => engine?.retry()}>{t('player.retry')}</button>
                  <button type="button" className="btn btn-secondary" data-nav onClick={onClose}>{t('common.close')}</button>
                </div>
              </>
            )}
            {overlay === 'offline' && (
              <>
                {s.net === 'offline' ? <WifiOff className="player-alert player-alert-net" /> : <ServerOff className="player-alert player-alert-net" />}
                <p className="player-status">{t(s.net === 'offline' ? 'net.offlineTitle' : 'net.unreachableTitle')}</p>
                <p className="player-status-sub player-net-text">{t(s.net === 'offline' ? 'net.offlineText' : 'net.unreachableText')}</p>
                <p className="player-net-wait"><Spinner className="spinner-small" /> {t('net.waiting')}</p>
                <div className="player-dialog-buttons">
                  <button type="button" className="btn btn-primary" data-nav data-nav-default onClick={() => engine?.retry()}>{t('player.retry')}</button>
                  <button type="button" className="btn btn-secondary" data-nav onClick={onClose}>{t('common.close')}</button>
                </div>
              </>
            )}
            {overlay === 'unreleased' && (
              <>
                <CalendarClock className="player-alert" />
                <p className="player-status">{t(m.media_type === 'tv' ? 'availability.episodeNotReleased' : 'availability.titleNotReleased')}</p>
                {!!releaseDate && <p className="player-status-sub">{t(s.unreleased?.status === 'in_theaters' ? 'availability.inTheatersUntil' : 'availability.comingOn', { date: i18n.date(releaseDate) })}</p>}
                <div className="player-dialog-buttons">
                  <button type="button" className="btn btn-primary" data-nav data-nav-default onClick={onClose}>{t('common.close')}</button>
                </div>
              </>
            )}
          </div>
        </div>
      )}

      {overlay === '' && s.phase === 'ready' && s.buffering && !s.ended && (
        <div className="player-center">
          <Spinner />
          <p className="player-status">{t(s.reconnecting ? 'player.reconnecting' : s.duration > 0 ? 'player.buffering' : s.status)}</p>
          {s.holding && !s.reconnecting && <p className="player-status-sub">{t('player.smoothBuffering')}</p>}
        </div>
      )}

      {overlay === 'fatal' && (
        <div className="player-center player-dialog">
          <AlertCircle className="player-alert" />
          <p className="player-status">{errorText(s.fatal)}</p>
          <div className="player-dialog-buttons">
            <button type="button" className="btn btn-primary" data-nav data-nav-default onClick={() => engine?.retry()}>{t('player.retry')}</button>
            <button type="button" className="btn btn-secondary" data-nav onClick={onClose}>{t('common.close')}</button>
          </div>
        </div>
      )}

      {overlay === 'countdown' && (
        <div className="player-center player-dialog">
          <p className="player-kicker">{t('player.nextEpisode')}</p>
          <p className="player-status">{t('player.nextStartsIn', { label: nextLabel, n: Math.max(0, s.countdown || 0) })}</p>
          <div className="player-dialog-buttons">
            <button type="button" className="btn btn-primary" data-nav data-nav-default onClick={() => engine?.playNext()}>
              <Play className="btn-icon btn-icon-fill" /> {t('player.playNow')}
            </button>
            <button type="button" className="btn btn-secondary" data-nav onClick={() => engine?.stopCountdown()}>{t('common.cancel')}</button>
          </div>
        </div>
      )}

      {skipShown === 'intro' && !panel && (
        <div className="player-skip">
          <button type="button" className="btn btn-skip" data-nav data-nav-id="pl-skip" onFocus={() => setZone('skip')}
            onClick={() => { const i = skipSegs?.intro; if (i) engine?.seekTo(i.end, 'skip-intro'); }}>
            <ChevronsRight className="btn-icon" /> {t('player.skipIntro')}
          </button>
        </div>
      )}

      {skipShown === 'credits' && !panel && (
        <div className="player-skip">
          {s.countdown !== null && (
            <button type="button" className="btn btn-secondary" data-nav data-nav-id="pl-skip-credits" onFocus={() => setZone('skip')}
              onClick={() => {
                creditsDismissed.current = true;
                engine?.stopCountdown();
                requestAnimationFrame(() => focusById('pl-skip'));
              }}>
              {t('player.watchCredits')}
            </button>
          )}
          <button type="button" className="btn btn-next-fill" data-nav data-nav-id="pl-skip" onFocus={() => setZone('skip')} onClick={() => engine?.playNext({ fromCredits: true })}>
            {s.countdown !== null && <span className="btn-next-fill-bar" style={{ transform: `scaleX(${(5 - s.countdown) / 5})` }} />}
            <SkipForward className="btn-icon btn-icon-fill" /> <span className="btn-next-fill-label">{t('player.nextEpisode')}</span>
          </button>
        </div>
      )}

      {!!s.toast && <div className="player-toast">{t(s.toast)}</div>}
      {s.aiTranslating && !s.toast && <div className="player-toast"><Spinner className="spinner-small" /> {t('player.aiTranslating')}</div>}

      <div className={`player-top ${controls || overlay ? 'is-visible' : ''}`}>
        <button type="button" className="player-back" data-nav data-nav-id="pl-close" onClick={onClose} aria-label={t('player.back')}>
          <ArrowLeft className="player-back-icon" />
        </button>
        {!overlay && (
          <div className="player-heading">
            <div className="player-title">{m.title}</div>
            {!!subtitle && <div className="player-subtitle">{subtitle}</div>}
          </div>
        )}
        {!!s.quality && !overlay && (
          <div className="player-badges">
            <span className="badge">{s.quality.label}</span>
            {s.quality.dv ? <span className="badge">Dolby Vision</span> : s.quality.hdr ? <span className="badge">HDR</span> : null}
            {!!s.audioBadge && <span className="badge">{s.audioBadge}</span>}
            {s.debrid && (
              <span className="badge">
                <svg viewBox="0 0 24 24" aria-hidden="true"
                  style={{ width: '1.1em', height: '1.1em', fill: '#e5e5e5', verticalAlign: '-0.2em', marginRight: '0.3em' }}>
                  <path d="M13.5 2 4 13.5h6.5L9 22l10-12h-6.5z" />
                </svg>
                TorBox
              </span>
            )}
            {s.cinema && (
              <span className="badge">
                <Clapperboard aria-hidden="true"
                  style={{ width: '1.1em', height: '1.1em', color: '#e5e5e5', verticalAlign: '-0.2em', marginRight: '0.3em' }} />
                {t('cinema.badge')}
              </span>
            )}
          </div>
        )}
      </div>

      {playable && (
        <div className={`player-bottom ${controls ? 'is-visible' : ''}`}>
          {preview !== null && thumbFile && (
            <TrickPlay
              hash={thumbFile.hash}
              index={thumbFile.index}
              target={preview}
              duration={s.duration}
              aspect={videoRef.current && videoRef.current.videoHeight ? videoRef.current.videoWidth / videoRef.current.videoHeight : 16 / 9}
            />
          )}
          {preview !== null && scrubMode && <div className="trickplay-hint">{t('player.scrubHint')}</div>}
          <div className="player-buttons" data-nav-group="pl-buttons">
            <button type="button" className="pbtn" data-nav data-nav-id="pl-playpause" data-nav-default onFocus={() => setZone('buttons')} onClick={() => engine?.togglePlay()}>
              {s.playing ? <Pause className="pbtn-icon" /> : <Play className="pbtn-icon btn-icon-fill" />}
              <span className="pbtn-label">{t(s.playing ? 'player.pause' : 'player.play')}</span>
            </button>
            <button type="button" className="pbtn" data-nav data-nav-id="pl-back10" onFocus={() => setZone('buttons')} onClick={() => engine?.seekTo(engine.positionNow() - 10)}>
              <RotateCcw className="pbtn-icon" />
              <span className="pbtn-label">{t('player.back10')}</span>
            </button>
            <button type="button" className="pbtn" data-nav data-nav-id="pl-fwd10" onFocus={() => setZone('buttons')} onClick={() => engine?.seekTo(engine.positionNow() + 10)}>
              <RotateCw className="pbtn-icon" />
              <span className="pbtn-label">{t('player.forward10')}</span>
            </button>
            {!!s.next && (
              <button type="button" className="pbtn" data-nav data-nav-id="pl-next" onFocus={() => setZone('buttons')} onClick={() => engine?.playNext()}>
                <SkipForward className="pbtn-icon" />
                <span className="pbtn-label">{t('player.nextEpisode')} {nextLabel}</span>
              </button>
            )}
            <button type="button" className="pbtn" data-nav data-nav-id="pl-audio" onFocus={() => setZone('buttons')} onClick={() => setPanel(true)}>
              <MessageSquare className="pbtn-icon" />
              <span className="pbtn-label">{t('player.audioSubtitles')}</span>
            </button>
          </div>
          <div
            className="timeline"
            data-nav
            data-nav-id="pl-timeline"
            tabIndex={-1}
            onFocus={() => setZone('timeline')}
            onClick={e => {
              const r = e.currentTarget.getBoundingClientRect();
              engine?.seekTo(((e.clientX - r.left) / r.width) * s.duration);
            }}
          >
            <div className="timeline-bar">
              <div className="timeline-fill" style={{ width: `${pct}%` }} />
              <div className="timeline-thumb" style={{ left: `${pct}%` }} />
              {preview !== null && <div className="timeline-preview" style={{ left: `${pct}%` }}>{formatTime(preview)}</div>}
            </div>
            <div className="timeline-times">
              <span>{formatTime(shown)}</span>
              <span>-{formatTime(Math.max(0, s.duration - shown))}</span>
            </div>
          </div>
        </div>
      )}

      {panel && playable && (
        <AudioSubtitlePanel
          audioTracks={s.audioTracks}
          audio={s.audio}
          onAudio={i => engine?.setAudio(i)}
          originalLang={m.original_language}
          subtitles={s.subs}
          subtitleId={s.subId}
          onSubtitle={id => engine?.setSubtitle(id)}
          subDelay={s.subDelay}
          onSubDelay={d => engine?.setSubDelay(d) ?? 0}
          subAlt={s.subAlt}
          onSubAlt={() => engine?.switchSubAlt()}
          onClose={closePanel}
          onStyling={setStyling}
        />
      )}
    </div>
  );
}
