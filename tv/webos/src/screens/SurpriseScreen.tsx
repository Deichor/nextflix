import { useEffect, useRef, useState } from 'react';
import { Info, Play, Shuffle } from 'lucide-react';
import Spinner from '../components/Spinner';
import { matchPercent, yearOf } from '../lib/catalog';
import { genreName, reasonText, surprise, type SurprisePick } from '../lib/discover';
import { img } from '../lib/http';
import { useI18n } from '../lib/i18n';
import { useEntryState, useFocusRestore, useRouter } from '../nav/router';

// "Surprise me": one title the profile is likely to enjoy, full screen; "Something else" asks again
export default function SurpriseScreen() {
  const { t, locale } = useI18n();
  const router = useRouter();
  // Kept on the entry, so Back from the detail page shows the same pick
  const [pick, setPick] = useEntryState<SurprisePick | null>('pick', null);
  const [loading, setLoading] = useState(!pick);
  const [failed, setFailed] = useState(false);
  const [logoFailed, setLogoFailed] = useState(false);
  const busy = useRef(false);
  const ctrlRef = useRef<AbortController | null>(null);

  const next = () => {
    if (busy.current) return;
    busy.current = true;
    const ctrl = new AbortController();
    ctrlRef.current = ctrl;
    setLoading(true);
    setFailed(false);
    surprise(ctrl.signal)
      .then(p => {
        if (ctrl.signal.aborted) return;
        if (p) {
          setPick(p);
          setLogoFailed(false);
        } else {
          setFailed(true);
        }
      })
      .catch(() => { if (!ctrl.signal.aborted) setFailed(true); })
      .finally(() => {
        busy.current = false;
        if (!ctrl.signal.aborted) setLoading(false);
      });
  };

  useEffect(() => {
    if (!pick) next();
    return () => ctrlRef.current?.abort();
    // first mount only
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  useFocusRestore(!!pick || failed, pick ? 'surprise-play' : 'surprise-retry');

  const title = pick?.title;
  const facts: string[] = [];
  if (title) {
    const match = matchPercent(title.vote);
    if (match) facts.push(t('detail.match', { n: match }));
    const year = yearOf(title.date);
    if (year) facts.push(String(year));
    facts.push(t(title.type === 'tv' ? 'common.tvShows' : 'common.movies'));
    const genres = title.genreIds.map(id => genreName(t, id)).filter(Boolean).slice(0, 3);
    if (genres.length) facts.push(genres.join(' • '));
  }

  return (
    <div className="screen surprise-screen">
      {!!title?.backdrop && <img key={title.backdrop} className="surprise-bg" src={img(title.backdrop, 'w1280')} alt="" />}
      <div className="surprise-shade" />
      <div className="surprise-body">
        <div className="surprise-kicker">{t('surprise.heading')}</div>
        {!pick && !failed && <Spinner />}
        {!pick && failed && (
          <div className="surprise-empty">
            <p>{t('surprise.error')}</p>
            <div className="surprise-buttons" data-nav-group="surprise-actions">
              <button type="button" className="btn btn-primary" data-nav data-nav-id="surprise-retry" onClick={next}>
                {t('common.retry')}
              </button>
            </div>
          </div>
        )}
        {!!pick && !!title && (
          <>
            {!!pick.logo && !logoFailed ? (
              <img className="surprise-logo" src={img(pick.logo, 'w500')} alt={title.title} onError={() => setLogoFailed(true)} />
            ) : (
              <h1 className="surprise-title">{title.title}</h1>
            )}
            <div className="surprise-reason">
              <Shuffle className="surprise-reason-icon" />
              {reasonText(t, locale, pick)}
            </div>
            <div className="hero-facts">{facts.map((f, i) => <span key={i}>{f}</span>)}</div>
            {!!title.overview && <p className="surprise-overview">{title.overview}</p>}
            <div className="surprise-buttons" data-nav-group="surprise-actions">
              <button
                type="button"
                className="btn btn-primary"
                data-nav
                data-nav-id="surprise-play"
                data-nav-default=""
                onClick={() => router.push({ name: 'detail', id: title.id, type: title.type, autoplay: true })}
              >
                <Play className="btn-icon btn-icon-fill" /> {t('player.play')}
              </button>
              <button
                type="button"
                className="btn btn-secondary"
                data-nav
                data-nav-id="surprise-details"
                onClick={() => router.push({ name: 'detail', id: title.id, type: title.type })}
              >
                <Info className="btn-icon" /> {t('surprise.details')}
              </button>
              <button type="button" className="btn btn-secondary" data-nav data-nav-id="surprise-next" onClick={next}>
                <Shuffle className="btn-icon" /> {t('surprise.next')}
              </button>
            </div>
            {loading && <Spinner className="surprise-spinner" />}
          </>
        )}
      </div>
    </div>
  );
}
