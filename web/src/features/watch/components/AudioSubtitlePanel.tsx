"use client";

import React, { useEffect, useMemo, useRef, useState } from 'react';
import { Check, ChevronLeft, Loader2, Minus, Plus, Repeat, ScanEye, SlidersHorizontal, Sparkles } from 'lucide-react';
import { useFormat, useLocale, useT, type Locale, type TFunction } from '@/shared/i18n';
import { AudioTrackInfo, SubtitleItem, languageName, normLang } from '@/features/watch/subtitles';
import mobileCss from '@/features/watch/components/mobilePlayer.module.css';

interface AudioSubtitlePanelProps {
  open: boolean;
  onClose: (opts?: { refocus?: boolean }) => void;
  /** The toggle button: clicks on it are not "outside" */
  anchorRef: React.RefObject<HTMLElement | null>;
  audioTracks: AudioTrackInfo[];
  audio: number;
  onAudio: (index: number) => void;
  originalLang?: string;
  /** null while the list is loading; undefined when subtitles are not available for this file */
  subtitles: SubtitleItem[] | null | undefined;
  subtitleId: string | null;
  /** Subtitles whose file could not be loaded this session (greyed) */
  failedSubtitles?: ReadonlySet<string>;
  onSubtitle: (id: string | null) => void;
  delay: number;
  onDelay: (seconds: number) => void;
  /** Another release of the title carries a subtitle in lang (the playing one lacks it): a row to switch */
  subAlt?: { lang: string; busy: boolean; onSwitch: () => void } | null;
  /** Touch layout: a bottom sheet (swipe down to close) instead of the popover */
  sheet?: boolean;
  /** Anime only: the Anime4K upscaling switch and, while it is on, the extra (A+A) switch; note says why one is off */
  upscale?: UpscaleSwitch & { extra: UpscaleSwitch };
}

interface UpscaleSwitch {
  on: boolean;
  /** Why it is off (shown as a warning) */
  note: string;
  /** Replaces the default hint (the picked mode, why extra does nothing here) */
  hint?: string;
  onToggle: () => void;
}

const SOURCE_KEYS = { torrent: 'player.subSourceTorrent', embedded: 'player.subSourceEmbedded' } as const;
const SOURCE_NAMES: Record<string, string> = { opensubtitles: 'OpenSubtitles', subdl: 'SubDL', turkcealtyazi: 'Türkçe Altyazı' };

function sourceLabel(t: TFunction, source: string): string {
  if (source in SOURCE_KEYS) return t(SOURCE_KEYS[source as keyof typeof SOURCE_KEYS]);
  return SOURCE_NAMES[source] || source;
}

function channelLabel(t: TFunction, channels: number): string {
  if (channels === 1) return t('player.mono');
  if (channels === 2) return t('player.stereo');
  if (channels === 6) return '5.1';
  if (channels === 8) return '7.1';
  return channels > 2 ? `${channels}ch` : '';
}

// Words that only repeat what the label already says ("English", "AC3 5.1", "Stereo")
const NOISE = /\b(stereo|mono|surround|audio|track|dolby|digital|plus|atmos|truehd|ac-?3|e-?ac-?3|ddp?|dts(-hd)?|ma|aac|opus|flac|mp3|lpcm|pcm|\d+(\.\d)?(ch)?|kbps|khz|original|default)\b/gi;

function trackNote(track: AudioTrackInfo, names: string[]): string {
  const title = (track.title || '').trim();
  if (!title) return '';
  let rest = title.toLowerCase().replace(NOISE, ' ');
  for (const n of names) if (n) rest = rest.split(n.toLowerCase()).join(' ');
  return /[a-zçğıöşü]{2,}/i.test(rest.replace(/[^a-zçğıöşü]+/gi, ' ').trim()) ? title : '';
}

export function audioTrackLabel(t: TFunction, locale: Locale, track: AudioTrackInfo, position: number) {
  const code = normLang(track.language);
  const name = code ? languageName(locale, code) : t('player.audioN', { n: position + 1 });
  const main = [name, channelLabel(t, track.channels)].filter(Boolean).join(' · ');
  const note = trackNote(track, [name, code, languageName('en', code), track.language]);
  return { main, note, code };
}

const rowBase =
  'group flex w-full items-center gap-3 rounded-md px-3 py-2.5 text-left text-base transition-colors focus:outline-none focus-visible:bg-white/15 md:px-4 md:py-3 md:text-lg';

function Row({
  active, onClick, col, children,
}: { active: boolean; onClick: () => void; col: 'audio' | 'subs'; children: React.ReactNode }) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={active}
      data-col={col}
      data-active={active || undefined}
      onClick={onClick}
      className={`${rowBase} ${active ? 'font-semibold text-white' : 'text-[#b3b3b3] hover:bg-white/10 hover:text-white'}`}
    >
      <span className="flex w-6 shrink-0 justify-center">
        {active && <Check className="h-5 w-5 text-[#E50914]" strokeWidth={3} />}
      </span>
      <span className="min-w-0 flex-1">{children}</span>
    </button>
  );
}

// An on/off row with a hint line under the label (wraps, never cut)
function SwitchRow({
  icon, label, hint, warn, on, onToggle,
}: { icon: React.ReactNode; label: string; hint: string; warn: boolean; on: boolean; onToggle: () => void }) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={on}
      data-col="audio"
      onClick={onToggle}
      className={`${rowBase} ${on ? 'text-white' : 'text-[#b3b3b3] hover:bg-white/10 hover:text-white'}`}
    >
      <span className="flex w-6 shrink-0 justify-center self-start pt-0.5">{icon}</span>
      <span className="min-w-0 flex-1">
        <span className="block">{label}</span>
        <span className={`mt-0.5 block text-xs font-normal leading-snug md:text-sm ${warn ? 'text-[#e5a00d]' : 'text-[#8c8c8c]'}`}>{hint}</span>
      </span>
      <span className={`relative h-5 w-9 shrink-0 rounded-full transition-colors ${on ? 'bg-[#E50914]' : 'bg-white/25'}`}>
        <span className={`absolute top-0.5 h-4 w-4 rounded-full bg-white transition-all ${on ? 'left-[18px]' : 'left-0.5'}`} />
      </span>
    </button>
  );
}

// Netflix-style "Audio & Subtitles" panel: two columns, red check on the active row,
// arrow keys move inside / between the columns, Esc or a click outside closes it.
export default function AudioSubtitlePanel({
  open, onClose, anchorRef, audioTracks, audio, onAudio, originalLang, subtitles, subtitleId, failedSubtitles, onSubtitle, delay, onDelay, subAlt, sheet = false, upscale,
}: AudioSubtitlePanelProps) {
  const t = useT();
  const f = useFormat();
  const locale = useLocale();
  const panelRef = useRef<HTMLDivElement>(null);
  const original = normLang(originalLang);
  // Simple view lists one row per language (its best subtitle); versions and timing live in the details view
  const [details, setDetails] = useState(false);
  useEffect(() => { if (!open) setDetails(false); }, [open]);
  // Bottom sheet drag (handle)
  const [dragY, setDragY] = useState(0);
  const [dragging, setDragging] = useState(false);
  const dragRef = useRef<{ id: number; y: number; at: number; moved: boolean } | null>(null);
  useEffect(() => { if (!open) { setDragY(0); setDragging(false); } }, [open]);
  // A subtitle that cannot be downloaded now (quota used up, failed) is the language's row only when nothing else is left
  const unusable = (s: SubtitleItem) => !!s.quota || !!failedSubtitles?.has(s.id);
  const bestPerLanguage = useMemo(() => {
    const best = new Map<string, SubtitleItem>();
    for (const s of subtitles || []) {
      const code = normLang(s.language) || s.language;
      const cur = best.get(code);
      if (!cur || (unusable(cur) && !unusable(s))) best.set(code, s);
    }
    return Array.from(best.values());
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [subtitles, failedSubtitles]);
  const activeSub = (subtitles || []).find(s => s.id === subtitleId) || null;
  const activeLang = activeSub ? normLang(activeSub.language) || activeSub.language : null;

  // Focus the active audio row when the panel opens
  useEffect(() => {
    if (!open) return;
    const el = panelRef.current;
    const target = el?.querySelector<HTMLElement>('[data-col="audio"][data-active]')
      || el?.querySelector<HTMLElement>('[data-col="subs"][data-active]')
      || el?.querySelector<HTMLElement>('[data-col]');
    target?.focus({ preventScroll: true });
  }, [open]);

  // Keys go to the panel first (capture), so the player does not seek or close underneath it
  useEffect(() => {
    if (!open) return;
    const onKey = (e: KeyboardEvent) => {
      const el = panelRef.current;
      if (!el) return;
      if (e.key === 'Escape') {
        e.preventDefault();
        e.stopPropagation();
        onClose({ refocus: true });
        return;
      }
      if (!['ArrowUp', 'ArrowDown', 'ArrowLeft', 'ArrowRight', ' ', 'Enter'].includes(e.key)) return;
      e.stopPropagation();
      if (e.key === ' ' || e.key === 'Enter') {
        // Space / Enter activate the focused row (default button behaviour); never play/pause
        if (!el.contains(document.activeElement)) e.preventDefault();
        return;
      }
      e.preventDefault();
      const focused = document.activeElement as HTMLElement | null;
      const col = (focused && el.contains(focused) && focused.dataset.col) || 'audio';
      const rows = (c: string) => Array.from(el.querySelectorAll<HTMLElement>(`[data-col="${c}"]`));
      if (e.key === 'ArrowUp' || e.key === 'ArrowDown') {
        const list = rows(col);
        if (list.length === 0) return;
        const i = focused ? list.indexOf(focused) : -1;
        const next = i < 0 ? 0 : Math.min(list.length - 1, Math.max(0, i + (e.key === 'ArrowDown' ? 1 : -1)));
        list[next]?.focus();
      } else {
        const other = rows(e.key === 'ArrowLeft' ? 'audio' : 'subs');
        (other.find(r => r.dataset.active) || other[0])?.focus();
      }
    };
    const onDown = (e: PointerEvent) => {
      const target = e.target as Node;
      if (panelRef.current?.contains(target) || anchorRef.current?.contains(target)) return;
      onClose();
    };
    window.addEventListener('keydown', onKey, true);
    document.addEventListener('pointerdown', onDown, true);
    return () => {
      window.removeEventListener('keydown', onKey, true);
      document.removeEventListener('pointerdown', onDown, true);
    };
  }, [open, onClose, anchorRef]);

  if (!open) return null;

  const delayText = delay === 0 ? f.number(0, { minimumFractionDigits: 1 }) : f.number(delay, { minimumFractionDigits: 1, signDisplay: 'always' });

  const panel = (
    <div
      ref={panelRef}
      role="dialog"
      aria-label={t('player.audioSubtitles')}
      onClick={e => e.stopPropagation()}
      className={sheet
        ? `absolute inset-x-0 bottom-0 z-40 mx-auto flex w-full max-w-[760px] flex-col overflow-hidden rounded-t-2xl bg-[#181818] text-white shadow-[0_-8px_30px_rgba(0,0,0,0.6)] ${mobileCss.sheet}`
        : 'absolute bottom-24 right-3 z-30 flex max-h-[min(72vh,600px)] w-[min(780px,calc(100vw-1.5rem))] flex-col overflow-hidden rounded-lg border border-white/10 bg-[#141414]/95 text-white shadow-[0_12px_40px_rgba(0,0,0,0.7)] backdrop-blur-md md:right-6 md:bottom-28'}
      style={sheet ? { transform: dragY ? `translateY(${dragY}px)` : undefined, transition: dragging ? 'none' : 'transform 180ms ease-out' } : undefined}
    >
      {/* Bottom sheet: drag the handle down (or tap it) to close */}
      {sheet && (
        <button
          type="button"
          aria-label={t('common.close')}
          className="flex w-full shrink-0 justify-center pb-2 pt-3 touch-none focus:outline-none"
          onPointerDown={e => {
            e.currentTarget.setPointerCapture(e.pointerId);
            dragRef.current = { id: e.pointerId, y: e.clientY, at: Date.now(), moved: false };
            setDragging(true);
          }}
          onPointerMove={e => {
            const d = dragRef.current;
            if (!d || d.id !== e.pointerId) return;
            const dy = Math.max(0, e.clientY - d.y);
            if (dy > 4) d.moved = true;
            setDragY(dy);
          }}
          onPointerUp={e => {
            const d = dragRef.current;
            setDragging(false);
            if (!d || d.id !== e.pointerId) return;
            const dy = e.clientY - d.y;
            // Far enough, or a quick flick
            if (dy > 80 || (dy > 20 && dy / Math.max(1, Date.now() - d.at) > 0.5)) onClose();
            else setDragY(0);
          }}
          onPointerCancel={() => {
            dragRef.current = null;
            setDragging(false);
            setDragY(0);
          }}
          onClick={() => {
            const d = dragRef.current;
            dragRef.current = null;
            if (!d?.moved) onClose();
          }}
        >
          <span className="h-1.5 w-10 rounded-full bg-white/35" />
        </button>
      )}
      <div data-scroll className="grid min-h-0 flex-1 grid-cols-1 overflow-y-auto overscroll-contain sm:grid-cols-[minmax(0,0.9fr)_minmax(0,1.1fr)] sm:overflow-hidden">
        {/* Audio */}
        <section data-scroll className="min-h-0 p-3 sm:overflow-y-auto sm:overscroll-contain md:p-4" role="radiogroup" aria-label={t('player.audio')}>
          <h3 className="px-3 pb-2 pt-1 text-xl font-bold md:px-4 md:text-2xl">{t('player.audio')}</h3>
          {audioTracks.length === 0 ? (
            <Row active col="audio" onClick={() => {}}>{t('player.audioN', { n: 1 })}</Row>
          ) : (
            audioTracks.map((track, i) => {
              const { main, note, code } = audioTrackLabel(t, locale, track, i);
              return (
                <Row key={track.index} col="audio" active={track.index === audio} onClick={() => onAudio(track.index)}>
                  <span className="block truncate">
                    {main}
                    {!!code && code === original && <span className="ml-1.5 text-sm font-normal text-[#8c8c8c]">[{t('player.original')}]</span>}
                  </span>
                  {!!note && <span className="block truncate text-xs font-normal text-[#8c8c8c] md:text-sm">{note}</span>}
                </Row>
              );
            })
          )}
          {!!upscale && (
            <div className="mt-3 border-t border-white/10 pt-3">
              <h4 className="px-3 pb-1 text-sm font-semibold text-[#b3b3b3] md:px-4">{t('player.picture')}</h4>
              <SwitchRow
                icon={<ScanEye className="h-5 w-5" />}
                label={t('player.upscale')}
                hint={upscale.note || upscale.hint || t('player.upscaleHint')}
                warn={!!upscale.note}
                on={upscale.on}
                onToggle={upscale.onToggle}
              />
              {upscale.on && (
                <SwitchRow
                  icon={<Sparkles className="h-5 w-5" />}
                  label={t('player.upscaleExtra')}
                  hint={upscale.extra.note || upscale.extra.hint || t('player.upscaleExtraHint')}
                  warn={!!upscale.extra.note}
                  on={upscale.extra.on}
                  onToggle={upscale.extra.onToggle}
                />
              )}
            </div>
          )}
        </section>

        {/* Subtitles */}
        <section data-scroll className="min-h-0 border-t border-white/10 p-3 sm:overflow-y-auto sm:overscroll-contain sm:border-l sm:border-t-0 md:p-4" role="radiogroup" aria-label={t('player.subtitles')}>
          {details ? (
            <>
              <button
                type="button"
                data-col="subs"
                onClick={() => setDetails(false)}
                className="mb-1 flex items-center gap-1.5 rounded px-2 py-1.5 text-xl font-bold hover:bg-white/10 focus:outline-none focus-visible:bg-white/15 md:text-2xl"
              >
                <ChevronLeft className="h-6 w-6" /> {t('player.subtitleSettings')}
              </button>
              <p className="px-3 pb-1 text-sm text-[#8c8c8c] md:px-4">{t('player.subtitleVersions')}</p>
          {(subtitles || []).map((s, i, all) => {
            const code = normLang(s.language);
            const name = code ? languageName(locale, code) : s.label || s.language;
            const tags = [s.hi ? t('player.subtitlesHi') : '', s.forced ? t('player.subtitlesForced') : ''].filter(Boolean);
            const newGroup = i > 0 && normLang(all[i - 1].language) !== code;
            const hint = s.quota ? t('player.subQuotaHint') : failedSubtitles?.has(s.id) ? t('player.subFailedHint') : '';
            return (
              <div key={s.id} className={`${newGroup ? 'mt-1 border-t border-white/5 pt-1' : ''} ${hint ? 'opacity-50' : ''}`} title={hint || undefined}>
                <Row col="subs" active={s.id === subtitleId} onClick={() => onSubtitle(s.id)}>
                  <span className="flex items-center gap-2">
                    <span className="truncate">{name}{tags.length > 0 && ` (${tags.join(', ')})`}</span>
                    {s.synced && (
                      <span className="shrink-0 rounded-sm border border-[#46d369]/70 px-1 text-[10px] font-bold uppercase leading-4 tracking-wide text-[#46d369]">
                        {t('player.subtitlesSynced')}
                      </span>
                    )}
                  </span>
                  <span className="block truncate text-xs font-normal text-[#8c8c8c] md:text-sm">
                    {[sourceLabel(t, s.source), s.release].filter(Boolean).join(' · ')}
                  </span>
                  {!!hint && <span className="block truncate text-xs font-normal text-[#e5a00d] md:text-sm">{hint}</span>}
                </Row>
              </div>
            );
          })}

          {!!subtitleId && (
            <div className="mt-3 border-t border-white/10 px-3 pt-3 md:px-4">
              <p className="mb-2 text-sm text-[#b3b3b3]">{t('player.subtitleDelay')}</p>
              <div className="flex items-center gap-2">
                <button
                  type="button"
                  onClick={() => onDelay(Math.round((delay - 0.5) * 10) / 10)}
                  className="flex h-9 w-9 items-center justify-center rounded-full border border-white/30 hover:border-white hover:bg-white/10 focus:outline-none focus-visible:border-white"
                  aria-label={t('player.subtitleEarlier')}
                  title={t('player.subtitleEarlier')}
                >
                  <Minus className="h-4 w-4" />
                </button>
                <span className="min-w-[4.5rem] text-center text-base font-semibold tabular-nums">{t('player.seconds', { value: delayText })}</span>
                <button
                  type="button"
                  onClick={() => onDelay(Math.round((delay + 0.5) * 10) / 10)}
                  className="flex h-9 w-9 items-center justify-center rounded-full border border-white/30 hover:border-white hover:bg-white/10 focus:outline-none focus-visible:border-white"
                  aria-label={t('player.subtitleLater')}
                  title={t('player.subtitleLater')}
                >
                  <Plus className="h-4 w-4" />
                </button>
                {delay !== 0 && (
                  <button
                    type="button"
                    onClick={() => onDelay(0)}
                    className="ml-1 rounded px-2.5 py-1.5 text-sm text-[#b3b3b3] hover:bg-white/10 hover:text-white focus:outline-none focus-visible:bg-white/15"
                  >
                    {t('player.subtitleDelayReset')}
                  </button>
                )}
              </div>
            </div>
          )}
            </>
          ) : (
            <>
              <h3 className="px-3 pb-2 pt-1 text-xl font-bold md:px-4 md:text-2xl">{t('player.subtitles')}</h3>
              <Row col="subs" active={!subtitleId} onClick={() => onSubtitle(null)}>{t('player.subtitlesOff')}</Row>
              {subtitles === null && (
                <p className="flex items-center gap-2 px-3 py-3 text-sm text-[#8c8c8c] md:px-4">
                  <Loader2 className="h-4 w-4 animate-spin" /> {t('player.subtitlesSearching')}
                </p>
              )}
              {Array.isArray(subtitles) && subtitles.length === 0 && (
                <p className="px-3 py-3 text-sm text-[#8c8c8c] md:px-4">{t('player.subtitlesNone')}</p>
              )}
              {bestPerLanguage.map(s => {
                const code = normLang(s.language) || s.language;
                const name = normLang(s.language) ? languageName(locale, code) : s.label || s.language;
                return (
                  <Row key={code} col="subs" active={activeLang === code} onClick={() => onSubtitle(s.id)}>
                    <span className="block truncate">{name}</span>
                  </Row>
                );
              })}
              {!!subAlt && (
                <Row col="subs" active={false} onClick={() => { if (!subAlt.busy) subAlt.onSwitch(); }}>
                  <span className="flex items-center gap-2">
                    {subAlt.busy ? <Loader2 className="h-4 w-4 shrink-0 animate-spin" /> : <Repeat className="h-4 w-4 shrink-0" />}
                    <span className="truncate">
                      {subAlt.busy ? t('player.subAltSwitching') : t('player.subAltRow', { lang: languageName(locale, normLang(subAlt.lang)) })}
                    </span>
                  </span>
                </Row>
              )}
              {bestPerLanguage.length > 0 && (
                <button
                  type="button"
                  data-col="subs"
                  onClick={() => setDetails(true)}
                  className="mt-3 flex w-full items-center gap-2 rounded border-t border-white/10 px-3 pb-1.5 pt-3 text-left text-sm text-[#b3b3b3] hover:text-white focus:outline-none focus-visible:text-white md:px-4"
                >
                  <SlidersHorizontal className="h-4 w-4" /> {t('player.subtitleSettings')}
                </button>
              )}
            </>
          )}
        </section>
      </div>
    </div>
  );

  if (!sheet) return panel;
  return (
    <>
      {/* Dims the video; a tap on it closes the sheet (the outside-tap rule above) */}
      <div className="absolute inset-0 z-[35] bg-black/50" aria-hidden="true" />
      {panel}
    </>
  );
}
