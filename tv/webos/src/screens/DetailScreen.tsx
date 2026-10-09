import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { CalendarClock, Clapperboard, Heart, Play, ThumbsDown, ThumbsUp } from 'lucide-react';
import DoubleThumbsUp from '../components/DoubleThumbsUp';
import Row, { CardImage } from '../components/Row';
import Spinner from '../components/Spinner';
import { img, invalidate, scoped } from '../lib/http';
import { useI18n, type MessageKey } from '../lib/i18n';
import { getProfileId } from '../lib/profile';
import { useQuery } from '../lib/useQuery';
import { useEntry, useEntryState, useFocusRestore, useRouter } from '../nav/router';
import { usePlayer } from '../nav/player';
import { focusById } from '../nav/spatial';
import {
  details, isPlaceholderEpisodeName, matchPercent, movieLogo, related, seasonEpisodes, yearOf,
  type Details, type EpisodeInfo, type MediaType,
} from '../lib/catalog';
import {
  episodeLabel, findProgress, getAvailability, getRating, isFinished, nextEpisodeOf, prefetchResolve, progressPercent,
  progressToRequest, putRating, releaseTitle, titleProgress, unavailableLabel,
  type Availability, type PlayRequest, type Rating, type RatingValue, type ResolveParams, type StreamMeta, type StreamProgress,
} from '../lib/stream';

// Seconds a warmed source survives leaving the page (web: LEAVE_GRACE_S)
const LEAVE_GRACE_S = 60;

interface Props {
  id: number;
  type: MediaType;
  autoplay?: boolean;
}

function metaOf(d: Details): StreamMeta {
  return {
    tmdb_id: d.id,
    media_type: d.type,
    title: d.title,
    poster_path: d.poster,
    backdrop_path: d.backdrop,
    year: yearOf(d.date),
    runtime: d.type === 'movie' ? d.runtime || undefined : undefined,
    original_language: d.originalLanguage,
  };
}

const isFuture = (date?: string) => !!date && new Date(`${date}T00:00:00`).getTime() > Date.now();

export default function DetailScreen({ id, type, autoplay }: Props) {
  const i18n = useI18n();
  const { t, locale } = i18n;
  const router = useRouter();
  const player = usePlayer();
  const entry = useEntry();
  const pid = getProfileId() ?? 0;
  const [progressReload, setProgressReload] = useState(0);

  const [detReload, setDetReload] = useState(0);
  const det = useQuery(scoped(`detail-${type}-${id}`), () => details(id, type), undefined, detReload);
  const d = det.data;
  const logo = useQuery(d && d.type === 'movie' ? scoped(`logo-${id}`) : null, () => movieLogo(id, locale));
  const rel = useQuery(d ? scoped(`related-${type}-${id}`) : null, () => related(id, type, yearOf(d?.date)));
  const progress = useQuery(d ? scoped(`progress-${type}-${id}`) : null, () => titleProgress(pid, type, id), 0, progressReload);
  const entries = progress.data;
  const progressReady = !!entries || progress.failed;

  // ---- What Play starts: resume, the episode after a finished one, or the beginning ----
  const plan = useMemo(() => {
    if (!d || !progressReady) return null;
    const list = entries || [];
    if (d.type === 'movie') {
      const p = findProgress(list);
      return { resume: p && !isFinished(p) && p.position > 0 ? p : null, ep: null as null | { season: number; episode: number }, saved: !!p?.hash };
    }
    const latest = list[0] || null;
    let resume: StreamProgress | null = null;
    let next: { season: number; episode: number } | null = null;
    if (latest && isFinished(latest)) next = nextEpisodeOf(d.seasons, latest.season, latest.episode);
    else if (latest) resume = latest;
    const ep = resume ? { season: resume.season, episode: resume.episode } : next || (d.seasons[0] ? { season: d.seasons[0].season_number, episode: 1 } : null);
    return { resume, ep, saved: !!(ep && findProgress(list, ep.season, ep.episode)?.hash) };
  }, [d, entries, progressReady]);

  // ---- Warm-up on open (the intent to watch), released after a grace when the page is left ----
  const warmed = useRef<ResolveParams | null>(null);
  useEffect(() => {
    if (!d || !plan) return;
    const target: ResolveParams | null = d.type === 'movie'
      ? { type: 'movie', title: d.title, tmdb_id: d.id, year: yearOf(d.date), runtime: d.runtime || undefined }
      : plan.ep ? { type: 'tv', title: d.title, tmdb_id: d.id, season: plan.ep.season, episode: plan.ep.episode } : null;
    if (!target || plan.saved) return;
    const prev = warmed.current;
    if (prev && JSON.stringify(prev) === JSON.stringify(target)) return;
    if (prev) releaseTitle(prev, LEAVE_GRACE_S);
    warmed.current = target;
    prefetchResolve(target);
  }, [d, plan]);
  useEffect(() => () => { if (warmed.current) releaseTitle(warmed.current, LEAVE_GRACE_S); }, []);

  // ---- Availability of what Play starts ----
  const [availability, setAvailability] = useState<Availability | null>(null);
  const availKey = d && plan ? `${d.type}-${d.id}-${plan.ep?.season ?? 0}-${plan.ep?.episode ?? 0}` : '';
  useEffect(() => {
    if (!d || !plan) return;
    let cancelled = false;
    getAvailability(d.type, d.id, plan.ep?.season, plan.ep?.episode).then(a => { if (!cancelled) setAvailability(a); });
    return () => { cancelled = true; };
    // availKey identifies the target
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [availKey]);
  const unavailable = unavailableLabel(availability, locale, i18n.date);

  // ---- Rating and favorite ----
  const [rating, setRating] = useState<Rating>({ rating: null, favorite: false });
  useEffect(() => {
    if (!pid) return;
    let cancelled = false;
    getRating(pid, type, id).then(r => { if (!cancelled) setRating(r); });
    return () => { cancelled = true; };
  }, [pid, type, id]);

  const saveRating = (patch: Partial<Rating>) => {
    if (!d || !pid) return;
    const before = rating;
    setRating({ ...rating, ...patch });
    putRating(pid, { type: d.type, id: d.id, title: d.title, poster: d.poster, backdrop: d.backdrop }, patch).then(r => {
      if (r) setRating(r);
      else setRating(before);
      invalidate('|favs');
      invalidate('|recs');
    });
  };
  const rate = (value: RatingValue) => saveRating({ rating: rating.rating === value ? null : value });

  // ---- Playback ----
  const afterPlay = useCallback(() => {
    invalidate('|cw');
    setProgressReload(n => n + 1);
  }, []);

  const open = useCallback((req: PlayRequest) => {
    warmed.current = null;
    player.open({ ...req, seasons: d && d.seasons.length ? d.seasons : undefined }, afterPlay);
  }, [player, d, afterPlay]);

  const playMain = useCallback(() => {
    if (!d || !plan) return;
    const meta = metaOf(d);
    if (plan.resume?.hash) {
      const req = progressToRequest(plan.resume);
      open({ ...req, meta: { ...req.meta, ...meta, season: req.meta.season, episode: req.meta.episode, episode_title: req.meta.episode_title } });
      return;
    }
    if (d.type === 'tv') {
      const ep = plan.ep || { season: 1, episode: 1 };
      const saved = entries ? findProgress(entries, ep.season, ep.episode) : null;
      open({ meta: { ...meta, season: ep.season, episode: ep.episode, episode_title: saved?.episode_title || undefined } });
      return;
    }
    open({ meta });
  }, [d, plan, entries, open]);

  const playEpisode = (ep: EpisodeInfo) => {
    if (!d) return;
    const saved = entries ? findProgress(entries, ep.season_number, ep.episode_number) : null;
    const meta = { ...metaOf(d), season: ep.season_number, episode: ep.episode_number };
    if (saved?.hash) {
      const req = progressToRequest(saved);
      open({ ...req, meta: { ...meta, episode_title: saved.episode_title || req.meta.episode_title } });
    } else {
      open({ meta: { ...meta, episode_title: ep.name && !isPlaceholderEpisodeName(ep) ? ep.name : undefined } });
    }
  };

  // Billboard "Play" opens this page with autoplay (once, not again after Back from the player)
  useEffect(() => {
    if (!autoplay || entry.state.autoplayed || !plan || unavailable) return;
    if (availability === null && d) {
      // wait a moment for the availability answer, so an unreleased title shows its date instead
      const timer = setTimeout(() => { entry.state.autoplayed = true; playMain(); }, 800);
      return () => clearTimeout(timer);
    }
    entry.state.autoplayed = true;
    playMain();
  }, [autoplay, plan, availability, unavailable, d, entry, playMain]);

  // ---- Seasons and episodes ----
  const [season, setSeason] = useEntryState<number>('season', 0);
  const activeSeason = d?.seasons.find(s => s.season_number === (season || plan?.ep?.season)) || d?.seasons[0];
  const episodes = useQuery(
    d && activeSeason ? scoped(`season-${d.id}-${activeSeason.season_number}`) : null,
    () => seasonEpisodes(id, activeSeason!),
    6 * 60 * 60 * 1000,
  );
  const seasonTimer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(seasonTimer.current), []);

  const [caption, setCaption] = useState('');
  // "Watch the cinema recording anyway": the first press warns, the second plays
  const [cinemaAsk, setCinemaAsk] = useState(false);

  useFocusRestore(!!d || det.failed, d ? 'detail-play' : 'detail-retry');
  // A retry that succeeds lands on Play
  const hadFailed = useRef(false);
  useEffect(() => {
    if (det.failed) hadFailed.current = true;
    else if (d && hadFailed.current) {
      hadFailed.current = false;
      requestAnimationFrame(() => focusById('detail-play'));
    }
  }, [det.failed, d]);

  if (!d) {
    return (
      <div className="screen detail-screen">
        {det.failed ? (
          <div className="empty center">
            <p className="empty-title">{t('detail.notFound')}</p>
            <p>{t('detail.loadFailed')}</p>
            <button type="button" className="btn btn-primary" data-nav data-nav-id="detail-retry" data-nav-default onClick={() => setDetReload(n => n + 1)}>
              {t('common.retry')}
            </button>
            <button type="button" className="btn btn-secondary" data-nav data-nav-id="detail-back" onClick={() => router.back()}>
              {t('player.back')}
            </button>
          </div>
        ) : <Spinner className="center" />}
      </div>
    );
  }

  const facts: string[] = [];
  const year = yearOf(d.date);
  if (year) facts.push(String(year));
  if (d.type === 'movie' && d.runtime > 0) facts.push(i18n.duration(d.runtime));
  if (d.type === 'tv' && d.numberOfSeasons > 0) facts.push(t('detail.seasons', { count: d.numberOfSeasons }));
  const match = matchPercent(d.vote);
  const resume = plan?.resume;
  const resumeLabel = resume && d.type === 'tv' ? episodeLabel(locale, resume.season, resume.episode) : '';
  const nextLabel = !resume && d.type === 'tv' && plan?.ep && entries && entries.length > 0 ? episodeLabel(locale, plan.ep.season, plan.ep.episode) : '';
  const left = resume ? Math.max(1, Math.round((resume.duration - resume.position) / 60)) : 0;

  const actions: { key: string; label: MessageKey; active: boolean; onClick: () => void; icon: JSX.Element }[] = [
    {
      key: 'favorite', label: rating.favorite ? 'rating.unfavorite' : 'rating.favorite', active: rating.favorite,
      onClick: () => saveRating({ favorite: !rating.favorite }),
      icon: <Heart className={`btn-icon ${rating.favorite ? 'btn-icon-fill' : ''}`} />,
    },
    { key: 'dislike', label: 'rating.dislike', active: rating.rating === 'dislike', onClick: () => rate('dislike'), icon: <ThumbsDown className="btn-icon" /> },
    { key: 'like', label: 'rating.like', active: rating.rating === 'like', onClick: () => rate('like'), icon: <ThumbsUp className="btn-icon" /> },
    {
      key: 'love', label: 'rating.love', active: rating.rating === 'love', onClick: () => rate('love'),
      icon: <DoubleThumbsUp className="btn-icon" />,
    },
  ];

  return (
    <div className="screen detail-screen">
      <div className="detail-bg">
        {!!d.backdrop && <img className="detail-bg-img" src={img(d.backdrop, 'w1280')} alt="" />}
        <div className="detail-shade" />
      </div>
      <div className="detail-scroll" data-scroll-y="edge" data-scroll-pad="4">
        <div data-scroll-content>
          <section className="detail-top" data-scroll-anchor>
            {logo.data ? (
              <img className="detail-logo" src={img(logo.data, 'w500')} alt={d.title} />
            ) : (
              <h1 className="detail-title">{d.title}</h1>
            )}
            <div className="detail-facts">
              {!!match && <span className="match">{t('detail.match', { n: match })}</span>}
              {facts.map((f, i) => <span key={i}>{f}</span>)}
            </div>
            {!!d.tagline && <p className="detail-tagline">{d.tagline}</p>}
            <p className="detail-overview">{d.overview || t('detail.noDescription')}</p>
            <p className="detail-credits">
              {d.cast.length > 0 && <span><b>{t('detail.cast')}:</b> {d.cast.join(', ')}</span>}
              {d.genres.length > 0 && <span><b>{t('detail.genres')}:</b> {d.genres.join(', ')}</span>}
              {d.directors.length > 0 && <span><b>{t('detail.director')}:</b> {d.directors.join(', ')}</span>}
              {d.creators.length > 0 && <span><b>{t('detail.createdBy')}:</b> {d.creators.join(', ')}</span>}
            </p>
            {!!resume && (
              <div className="detail-resume">
                <span className="progress-bar"><span style={{ width: `${progressPercent(resume)}%` }} /></span>
                <span>{[resumeLabel, t('player.timeLeft', { time: i18n.duration(left) })].filter(Boolean).join(' · ')}</span>
              </div>
            )}
            <div className="detail-actions" data-nav-group="detail-actions">
              {unavailable ? (
                <button type="button" className="btn btn-disabled" data-nav data-nav-id="detail-play" onFocus={() => setCaption('')}>
                  <CalendarClock className="btn-icon" /> {unavailable}
                </button>
              ) : null}
              {d.type === 'movie' && availability?.status === 'in_theaters' && (
                <button
                  type="button"
                  className="btn btn-secondary"
                  data-nav
                  data-nav-id="detail-cinema"
                  onFocus={() => setCaption(cinemaAsk ? t('cinema.confirm') : '')}
                  onBlur={() => setCinemaAsk(false)}
                  onClick={() => {
                    if (!cinemaAsk) {
                      setCinemaAsk(true);
                      setCaption(t('cinema.confirm'));
                      return;
                    }
                    setCinemaAsk(false);
                    open({ meta: { ...metaOf(d), cinema: true } });
                  }}
                >
                  <Clapperboard className="btn-icon" /> {t(cinemaAsk ? 'cinema.play' : 'cinema.watchAnyway')}
                </button>
              )}
              {!unavailable && (
                <button
                  type="button"
                  className="btn btn-primary"
                  data-nav
                  data-nav-id="detail-play"
                  onFocus={() => setCaption('')}
                  onClick={playMain}
                >
                  <Play className="btn-icon btn-icon-fill" />
                  {t(resume ? 'player.resume' : 'player.play')}
                  {!!(resumeLabel || nextLabel) && <span className="btn-detail">{resumeLabel || nextLabel}</span>}
                </button>
              )}
              {actions.map(a => (
                <button
                  key={a.key}
                  type="button"
                  className={`btn-round ${a.active ? 'is-active' : ''}`}
                  data-nav
                  data-nav-id={`detail-${a.key}`}
                  onFocus={() => setCaption(t(a.label))}
                  onClick={a.onClick}
                >
                  {a.icon}
                </button>
              ))}
            </div>
            <div className="action-caption">{caption}</div>
          </section>

          {d.type === 'tv' && d.seasons.length > 0 && (
            <section className="detail-episodes" data-scroll-anchor>
              <h2 className="row-title">{t('detail.episodes')}</h2>
              <div className="season-tabs" data-scroll-x="edge" data-scroll-pad="2">
                <div className="season-track" data-scroll-content data-nav-group="seasons">
                  {d.seasons.map(s => (
                    <button
                      key={s.season_number}
                      type="button"
                      className={`chip ${activeSeason?.season_number === s.season_number ? 'is-active' : ''}`}
                      data-nav
                      data-nav-id={`season-${s.season_number}`}
                      data-nav-default={activeSeason?.season_number === s.season_number ? '' : undefined}
                      onFocus={() => {
                        clearTimeout(seasonTimer.current);
                        seasonTimer.current = setTimeout(() => setSeason(s.season_number), 300);
                      }}
                      onClick={() => setSeason(s.season_number)}
                    >
                      {t('detail.season', { n: s.season_number })}
                    </button>
                  ))}
                </div>
              </div>
              <div className="row-scroller" data-scroll-x="start" data-scroll-pad="8">
                <div key={activeSeason?.season_number} className="row-track" data-scroll-content data-nav-group={`episodes-${activeSeason?.season_number}`}>
                  {!episodes.data && [0, 1, 2, 3].map(i => <div key={i} className="episode-card card-skeleton" />)}
                  {episodes.data && episodes.data.map(ep => {
                    const p = entries ? findProgress(entries, ep.season_number, ep.episode_number) : null;
                    const future = isFuture(ep.air_date);
                    return (
                      <button
                        key={ep.episode_number}
                        type="button"
                        className="episode-card"
                        data-nav
                        data-nav-id={`ep-${ep.season_number}-${ep.episode_number}`}
                        onClick={() => playEpisode(ep)}
                      >
                        <span className="episode-still">
                          <CardImage src={img(ep.still_path || d.backdrop, 'w300')} />
                          {!!p && p.position > 0 && <span className="card-progress"><span style={{ width: `${progressPercent(p)}%` }} /></span>}
                        </span>
                        <span className="episode-name">{isPlaceholderEpisodeName(ep) ? ep.name : `${ep.episode_number}. ${ep.name}`}</span>
                        <span className="episode-meta">
                          {future ? t('availability.airsOn', { date: i18n.date(ep.air_date) }) : ep.runtime > 0 ? i18n.duration(ep.runtime) : ''}
                        </span>
                        {!!ep.overview && <span className="episode-overview">{ep.overview}</span>}
                      </button>
                    );
                  })}
                </div>
              </div>
            </section>
          )}

          {!!rel.data && rel.data.length > 0 && (
            <Row
              row={{ id: 'related', title: t('detail.moreLikeThis'), variant: 'backdrop', items: rel.data }}
              onSelectTitle={item => router.push({ name: 'detail', id: item.id, type: item.type })}
            />
          )}
          <div className="rows-end" />
        </div>
      </div>
    </div>
  );
}
