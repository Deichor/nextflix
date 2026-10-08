import { useEffect, useMemo, useRef } from 'react';
import { Check } from 'lucide-react';
import { useI18n, type TFunction } from '../lib/i18n';
import { bestPerLanguage, languageName, normLang, type AudioTrackInfo, type SubtitleItem } from '../lib/subtitles';
import { focusDefault } from '../nav/spatial';
import Spinner from '../components/Spinner';

interface Props {
  audioTracks: AudioTrackInfo[];
  audio: number;
  onAudio: (index: number) => void;
  originalLang?: string;
  subtitles: SubtitleItem[] | null;
  subtitleId: string | null;
  onSubtitle: (id: string | null) => void;
  /** Another release carries the profile language's subtitle (the playing one lacks it): a row to switch */
  subAlt?: { busy: boolean } | null;
  onSubAlt?: () => void;
}

function channelLabel(t: TFunction, channels: number): string {
  if (channels === 1) return t('player.mono');
  if (channels === 2) return t('player.stereo');
  if (channels === 6) return '5.1';
  if (channels === 8) return '7.1';
  return channels > 2 ? `${channels}ch` : '';
}

// The web player's simple view: audio tracks on the left, one row per subtitle language on the right
export default function AudioSubtitlePanel({ audioTracks, audio, onAudio, originalLang, subtitles, subtitleId, onSubtitle, subAlt, onSubAlt }: Props) {
  const { t, locale } = useI18n();
  const ref = useRef<HTMLDivElement>(null);
  const original = normLang(originalLang);
  const order = locale === 'tr' ? ['tr', 'en'] : ['en', 'tr'];
  const perLanguage = useMemo(() => bestPerLanguage(subtitles || [], order, locale), [subtitles, locale]); // eslint-disable-line react-hooks/exhaustive-deps
  const active = (subtitles || []).find(s => s.id === subtitleId);
  const activeLang = active ? normLang(active.language) || active.language : null;

  useEffect(() => {
    const raf = requestAnimationFrame(() => { if (ref.current) focusDefault(ref.current); });
    return () => cancelAnimationFrame(raf);
  }, []);

  return (
    <div className="panel" ref={ref} data-nav-layer="panel">
      <section className="panel-col" data-nav-group="panel-audio">
        <h3 className="panel-title">{t('player.audio')}</h3>
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
      </section>
      <section className="panel-col" data-nav-group="panel-subs">
        <h3 className="panel-title">{t('player.subtitles')}</h3>
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
              onClick={() => onSubtitle(s.id)}
            >
              <span className="panel-check">{isActive && <Check className="panel-check-icon" />}</span>
              <span className="panel-label">{normLang(s.language) ? languageName(locale, code) : s.label || s.language}</span>
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
      </section>
    </div>
  );
}
