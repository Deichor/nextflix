import { useEffect, useMemo, useRef, useState } from 'react';
import { Check, ChevronLeft, ChevronRight, Type } from 'lucide-react';
import { useI18n, type TFunction } from '../lib/i18n';
import { bestPerLanguage, languageName, normLang, type AudioTrackInfo, type SubtitleItem } from '../lib/subtitles';
import {
  DEFAULT_SUB_STYLE, SUB_STYLE_OPTIONS, cycleSubtitleStyle, subStyleValueLabel, useSubtitleStyle, writeSubtitleStyle, type SubStyleKey,
} from '../lib/subtitleStyle';
import { direction, isBack } from '../lib/webos';
import { focusById, focusDefault, move, pushKeyHandler } from '../nav/spatial';
import Spinner from '../components/Spinner';

/** The panel closes by itself after this long without a key or pointer move */
const PANEL_IDLE_MS = 15000;

interface Props {
  audioTracks: AudioTrackInfo[];
  audio: number;
  onAudio: (index: number) => void;
  originalLang?: string;
  subtitles: SubtitleItem[] | null;
  subtitleId: string | null;
  onSubtitle: (id: string | null) => void;
  /** Subtitle timing in seconds (+ later, - earlier) */
  subDelay: number;
  /** Sets the timing; returns the value it took (rounded, limited) */
  onSubDelay: (seconds: number) => number;
  /** Another release carries the profile language's subtitle (the playing one lacks it): a row to switch */
  subAlt?: { busy: boolean } | null;
  onSubAlt?: () => void;
  onClose: () => void;
  /** The subtitle appearance view is open (the player shows a live preview) */
  onStyling?: (on: boolean) => void;
}

function channelLabel(t: TFunction, channels: number): string {
  if (channels === 1) return t('player.mono');
  if (channels === 2) return t('player.stereo');
  if (channels === 6) return '5.1';
  if (channels === 8) return '7.1';
  return channels > 2 ? `${channels}ch` : '';
}

const SUB_DELAY_STEP_S = 0.5;

// "+1,5" / "−0.5" / "0.0": signDisplay and Intl number options are missing on old webOS engines
function delayText(d: number, locale: string): string {
  const n = Math.abs(d).toFixed(1);
  return (d > 0 ? '+' : d < 0 ? '−' : '') + (locale === 'tr' ? n.replace('.', ',') : n);
}

// The web player's simple view: audio tracks on the left, one row per subtitle language on the right.
// "Subtitle timing" (while a subtitle is on) shifts it with Left / Right. "Subtitle appearance" at the end
// of the list swaps in the style view (Left / Right or OK change a value).
// Closes on a click outside (the backdrop), Back, Left past the audio column and after PANEL_IDLE_MS.
export default function AudioSubtitlePanel({ audioTracks, audio, onAudio, originalLang, subtitles, subtitleId, onSubtitle, subDelay, onSubDelay, subAlt, onSubAlt, onClose, onStyling }: Props) {
  const { t, locale } = useI18n();
  const ref = useRef<HTMLDivElement>(null);
  const [view, setView] = useState<'tracks' | 'style'>('tracks');
  const viewRef = useRef(view);
  viewRef.current = view;
  const style = useSubtitleStyle();
  const styleRef = useRef(style);
  styleRef.current = style;
  const delayRef = useRef({ value: subDelay, set: onSubDelay });
  delayRef.current = { value: subDelay, set: onSubDelay };
  const [delayFocused, setDelayFocused] = useState(false);
  const original = normLang(originalLang);
  const order = locale === 'tr' ? ['tr', 'en'] : ['en', 'tr'];
  const perLanguage = useMemo(() => bestPerLanguage(subtitles || [], order, locale), [subtitles, locale]); // eslint-disable-line react-hooks/exhaustive-deps
  const active = (subtitles || []).find(s => s.id === subtitleId);
  const activeLang = active ? normLang(active.language) || active.language : null;

  const change = (key: SubStyleKey, step: 1 | -1) => writeSubtitleStyle(cycleSubtitleStyle(styleRef.current, key, step));

  // First focus, and the row to land on when the view changes
  const firstView = useRef(true);
  useEffect(() => {
    onStyling?.(view === 'style');
    const raf = requestAnimationFrame(() => {
      if (!ref.current) return;
      if (firstView.current) focusDefault(ref.current);
      else if (view === 'style') focusById('style-size');
      else focusById('sub-style');
      firstView.current = false;
    });
    return () => cancelAnimationFrame(raf);
  }, [view]); // eslint-disable-line react-hooks/exhaustive-deps

  // Runs before the player's handler: Back leaves the style view, Left / Right change a value there,
  // Left past the audio column closes the panel. Other keys go on to the player (Up / Down, OK, Back).
  const closeRef = useRef(onClose);
  closeRef.current = onClose;
  useEffect(() => pushKeyHandler(e => {
    const dir = direction(e);
    if (viewRef.current === 'style') {
      if (isBack(e)) { setView('tracks'); return true; }
      if (dir === 'left' || dir === 'right') {
        const el = document.activeElement as HTMLElement | null;
        const key = el && el.getAttribute('data-style-key');
        if (key) change(key as SubStyleKey, dir === 'right' ? 1 : -1);
        return true;
      }
      return false;
    }
    // Subtitle timing row: Left / Right shift the subtitles by half a second
    if ((dir === 'left' || dir === 'right') && document.activeElement?.hasAttribute('data-sub-delay')) {
      const d = delayRef.current;
      // Held keys repeat faster than renders: count on from the value just set
      d.value = d.set(d.value + (dir === 'right' ? SUB_DELAY_STEP_S : -SUB_DELAY_STEP_S));
      return true;
    }
    if (dir === 'left') {
      if (!move('left')) closeRef.current();
      return true;
    }
    return false;
  }), []); // eslint-disable-line react-hooks/exhaustive-deps

  // Idle close: any key, click or a real pointer move restarts the clock
  useEffect(() => {
    let timer = setTimeout(() => closeRef.current(), PANEL_IDLE_MS);
    let lx = -1;
    let ly = -1;
    const bump = () => {
      clearTimeout(timer);
      timer = setTimeout(() => closeRef.current(), PANEL_IDLE_MS);
    };
    const onMove = (e: MouseEvent) => {
      if (Math.abs(e.clientX - lx) + Math.abs(e.clientY - ly) < 8) return;
      lx = e.clientX;
      ly = e.clientY;
      bump();
    };
    window.addEventListener('keydown', bump, true);
    document.addEventListener('mousedown', bump, true);
    document.addEventListener('mousemove', onMove, true);
    return () => {
      clearTimeout(timer);
      window.removeEventListener('keydown', bump, true);
      document.removeEventListener('mousedown', bump, true);
      document.removeEventListener('mousemove', onMove, true);
    };
  }, []);

  return (
    <>
      {/* Catches a click anywhere outside the panel, so it never reaches the video (play / pause) */}
      <div className="panel-backdrop" onClick={e => { e.stopPropagation(); onClose(); }} />
      <div className={`panel ${view === 'style' ? 'panel-style' : ''}`} ref={ref} data-nav-layer="panel">
        {view === 'tracks' && (
          <>
            <section className="panel-col" data-nav-group="panel-audio">
              <h3 className="panel-title">{t('player.audio')}</h3>
              <div className="panel-scroll" data-scroll-y="edge" data-scroll-pad="1">
                <div data-scroll-content>
                  {(audioTracks.length > 0 ? audioTracks : [{ index: 0, language: '', title: '', codec: '', channels: 0 }]).map((track, i) => {
                    const code = normLang(track.language);
                    const name = code ? languageName(locale, code) : t('player.audioN', { n: i + 1 });
                    const label = [name, channelLabel(t, track.channels)].filter(Boolean).join(' · ');
                    const isActive = track.index === audio;
                    return (
                      <button
                        key={track.index}
                        type="button"
                        className={`panel-row ${isActive ? 'is-active' : ''}`}
                        data-nav
                        data-nav-id={`audio-${track.index}`}
                        data-nav-default={isActive ? '' : undefined}
                        onClick={() => onAudio(track.index)}
                      >
                        <span className="panel-check">{isActive && <Check className="panel-check-icon" />}</span>
                        <span className="panel-label">
                          {label}
                          {!!code && code === original && <span className="panel-note"> [{t('player.original')}]</span>}
                        </span>
                      </button>
                    );
                  })}
                </div>
              </div>
            </section>
            <section className="panel-col" data-nav-group="panel-subs">
              <h3 className="panel-title">{t('player.subtitles')}</h3>
              <div className="panel-scroll" data-scroll-y="edge" data-scroll-pad="1">
                <div data-scroll-content>
                  <button
                    type="button"
                    className={`panel-row ${!subtitleId ? 'is-active' : ''}`}
                    data-nav
                    data-nav-id="sub-off"
                    data-nav-default={!subtitleId ? '' : undefined}
                    onClick={() => onSubtitle(null)}
                  >
                    <span className="panel-check">{!subtitleId && <Check className="panel-check-icon" />}</span>
                    <span className="panel-label">{t('player.subtitlesOff')}</span>
                  </button>
                  {subtitles === null && <p className="panel-hint"><Spinner className="spinner-small" /> {t('player.subtitlesSearching')}</p>}
                  {Array.isArray(subtitles) && subtitles.length === 0 && <p className="panel-hint">{t('player.subtitlesNone')}</p>}
                  {perLanguage.map(s => {
                    const code = normLang(s.language) || s.language;
                    const isActive = activeLang === code;
                    return (
                      <button
                        key={code}
                        type="button"
                        className={`panel-row ${isActive ? 'is-active' : ''}`}
                        data-nav
                        data-nav-id={`sub-${code}`}
                        data-nav-default={isActive ? '' : undefined}
                        onClick={() => onSubtitle(isActive && active ? active.id : s.id)}
                      >
                        <span className="panel-check">{isActive && <Check className="panel-check-icon" />}</span>
                        <span className="panel-label">
                          {normLang(s.language) ? languageName(locale, code) : s.label || s.language}
                          {s.source === 'ai' ? ` · ${t('player.subAi')}` : ''}
                        </span>
                      </button>
                    );
                  })}
                  {!!subAlt && (
                    <button
                      type="button"
                      className="panel-row"
                      data-nav
                      data-nav-id="sub-alt"
                      onClick={() => { if (!subAlt.busy) onSubAlt?.(); }}
                    >
                      <span className="panel-check">{subAlt.busy && <Spinner className="spinner-small" />}</span>
                      <span className="panel-label">{t(subAlt.busy ? 'player.subAltSwitching' : 'player.subAltRow')}</span>
                    </button>
                  )}
                  {!!subtitleId && (
                    <button
                      type="button"
                      className="panel-row style-row delay-row"
                      data-nav
                      data-nav-id="sub-delay"
                      data-sub-delay
                      onFocus={() => setDelayFocused(true)}
                      onBlur={() => setDelayFocused(false)}
                      onClick={() => onSubDelay(0)}
                    >
                      <span className="style-name">{t('player.subtitleDelay')}</span>
                      <span className="style-arrow" onClick={e => { e.stopPropagation(); onSubDelay(subDelay - SUB_DELAY_STEP_S); }}><ChevronLeft className="style-arrow-icon" /></span>
                      <span className="style-value">{t('player.seconds', { value: delayText(subDelay, locale) })}</span>
                      <span className="style-arrow" onClick={e => { e.stopPropagation(); onSubDelay(subDelay + SUB_DELAY_STEP_S); }}><ChevronRight className="style-arrow-icon" /></span>
                    </button>
                  )}
                  {!!subtitleId && delayFocused && <p className="panel-hint">{t('player.subtitleDelayHint')}</p>}
                  <button type="button" className="panel-row panel-row-link" data-nav data-nav-id="sub-style" onClick={() => setView('style')}>
                    <span className="panel-check"><Type className="panel-check-icon panel-link-icon" /></span>
                    <span className="panel-label">{t('subStyle.title')}</span>
                    <ChevronRight className="panel-row-chevron" />
                  </button>
                </div>
              </div>
            </section>
          </>
        )}
        {view === 'style' && (
          <section className="panel-col" data-nav-group="panel-style">
            <h3 className="panel-title">{t('subStyle.title')}</h3>
            {SUB_STYLE_OPTIONS.map(o => (
              <button
                key={o.key}
                type="button"
                className="panel-row style-row"
                data-nav
                data-nav-id={`style-${o.key}`}
                data-style-key={o.key}
                onClick={() => change(o.key, 1)}
              >
                <span className="style-name">{t(o.label)}</span>
                <span className="style-arrow" onClick={e => { e.stopPropagation(); change(o.key, -1); }}><ChevronLeft className="style-arrow-icon" /></span>
                <span className="style-value">{t(subStyleValueLabel(style, o.key))}</span>
                <span className="style-arrow" onClick={e => { e.stopPropagation(); change(o.key, 1); }}><ChevronRight className="style-arrow-icon" /></span>
              </button>
            ))}
            <button type="button" className="panel-row style-action" data-nav data-nav-id="style-reset" onClick={() => writeSubtitleStyle(DEFAULT_SUB_STYLE)}>
              <span className="panel-label">{t('subStyle.reset')}</span>
            </button>
            <button type="button" className="panel-row style-action" data-nav data-nav-id="style-done" onClick={() => setView('tracks')}>
              <span className="panel-label">{t('subStyle.done')}</span>
            </button>
          </section>
        )}
      </div>
    </>
  );
}
