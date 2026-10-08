"use client";

import React, { useState, useEffect, useRef, useMemo, useCallback } from 'react';
import { useParams, useRouter, useSearchParams } from 'next/navigation';
import { usePageTitle } from '@/shared/hooks/usePageTitle';
import { ExternalLink, Volume2, VolumeX } from 'lucide-react';
import { getApiUrl } from '@/shared/lib/api';
import RedLoader from '@/shared/ui/RedLoader';
import TorrentStreamPlayer from '@/features/watch/components/TorrentStreamPlayer';
import WatchButton, { formatRemaining } from '@/features/watch/components/WatchButton';
import SeasonEpisodes from '@/features/watch/components/SeasonEpisodes';
import { useActiveProfile } from '@/shared/lib/profile';
import {
  PlayRequest,
  StreamMeta,
  StreamProgress,
  findProgress,
  getTitleProgress,
  isFinished,
  nextEpisodeOf,
  normalizeSeasons,
  progressPercent,
  progressToRequest,
  episodeLabel,
} from '@/features/watch/api';
import NetflixPage from '@/features/shell/PageShell';
import {
  InfoLine,
  MetaLine,
  MoreLikeThis,
  MyListButton,
  RoundButton,
  formatRuntime,
} from '@/features/catalog/components/DetailParts';
import { NfTitle, fetchLogo, fetchRelated, imageUrl } from '@/features/catalog/titles';

interface Person {
  id: number;
  name: string;
  character?: string;
  job?: string;
  department?: string;
  profile_path: string;
  order?: number;
}

interface VideoItem {
  id: string;
  key: string;
  name: string;
  site: string;
  type: string;
  official: boolean;
  published_at: string;
}

interface SeasonSummary {
  id: number;
  name: string;
  overview: string;
  poster_path: string;
  season_number: number;
  episode_count: number;
  air_date: string;
}

// Movie and TV details mapped to one shape (TV name/first_air_date -> title/release_date)
interface UnifiedMediaDetails {
  id: number;
  title: string;
  original_title: string;
  overview: string;
  release_date: string;
  poster_path: string;
  backdrop_path: string;
  vote_average: number;
  vote_count: number;
  genres: Array<{ id: number; name: string }>;
  production_countries?: Array<{ iso_3166_1: string; name: string }>;
  spoken_languages?: Array<{ iso_639_1: string; name: string; english_name?: string }>;
  tagline?: string;
  status?: string;
  adult?: boolean;
  homepage?: string;
  videos?: { results: VideoItem[] };
  credits?: { cast: Person[]; crew: Person[] };
  media_type: 'movie' | 'tv';
  runtime?: number;
  budget?: number;
  revenue?: number;
  number_of_episodes?: number;
  number_of_seasons?: number;
  networks?: Array<{ id: number; name: string; logo_path: string }>;
  created_by?: Array<{ id: number; name: string }>;
  seasons?: SeasonSummary[];
}

function toUnified(responseData: any): UnifiedMediaDetails {
  if (responseData?.media_type && responseData?.data) {
    const d = responseData.data;
    if (responseData.media_type === 'tv') {
      return {
        ...d,
        media_type: 'tv',
        title: d.name,
        original_title: d.original_name,
        release_date: d.first_air_date,
      };
    }
    return { ...d, media_type: 'movie' };
  }
  // Older backends returned the movie object directly
  return { ...responseData, media_type: 'movie' };
}

function findBestTrailer(videos: VideoItem[]) {
  const yt = videos.filter(v => v.site === 'YouTube');
  return (
    yt.find(v => v.official && v.type === 'Trailer') ||
    yt.find(v => v.type === 'Trailer') ||
    yt.find(v => v.type === 'Teaser') ||
    yt[0] ||
    null
  );
}

function formatDate(dateString?: string) {
  if (!dateString) return '';
  const d = new Date(dateString);
  return isNaN(d.getTime()) ? '' : d.toLocaleDateString('en-US', { year: 'numeric', month: 'long', day: 'numeric' });
}

function formatCurrency(amount?: number) {
  if (!amount) return '';
  return new Intl.NumberFormat('en-US', { style: 'currency', currency: 'USD', maximumFractionDigits: 0 }).format(amount);
}

const TMDBMoviePage: React.FC = () => {
  const params = useParams();
  const router = useRouter();
  const searchParams = useSearchParams();
  const videoRef = useRef<HTMLIFrameElement>(null);
  const heroRef = useRef<HTMLDivElement>(null);
  const episodesRef = useRef<HTMLDivElement>(null);

  const [mediaDetails, setMediaDetails] = useState<UnifiedMediaDetails | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [trailerKey, setTrailerKey] = useState<string | null>(null);
  const [isPlaying, setIsPlaying] = useState(false);
  const [isMuted, setIsMuted] = useState(true);
  const [isVideoLoaded, setIsVideoLoaded] = useState(false);
  const [logoUrl, setLogoUrl] = useState<string | null>(null);
  const [logoFailed, setLogoFailed] = useState(false);
  const [related, setRelated] = useState<NfTitle[]>([]);
  // Streaming playback + saved progress of the active profile
  const activeProfile = useActiveProfile();
  const [playRequest, setPlayRequest] = useState<PlayRequest | null>(null);
  const [savedProgress, setSavedProgress] = useState<StreamProgress | null>(null);
  const [nextUp, setNextUp] = useState<{ season: number; episode: number } | null>(null);
  const [progressRefresh, setProgressRefresh] = useState(0);
  const autoPlayHandled = useRef(false);

  const movieId = params?.id as string;
  const typeParam = searchParams?.get('type') || '';
  usePageTitle(mediaDetails?.title || 'Details');

  useEffect(() => {
    if (!movieId) return;
    let cancelled = false;
    // The page component is reused when moving between titles
    autoPlayHandled.current = false;
    setPlayRequest(null);
    setRelated([]);
    window.scrollTo({ top: 0 });
    (async () => {
      try {
        setLoading(true);
        const mediaType = typeParam;
        const url = `${getApiUrl()}/api/tmdb-movie/${movieId}${mediaType ? `?type=${mediaType}` : ''}`;
        const response = await fetch(url);
        if (!response.ok) throw new Error(`HTTP error! status: ${response.status}`);
        const unified = toUnified(await response.json());
        if (cancelled) return;
        setMediaDetails(unified);
        setError(null);

        const trailer = findBestTrailer(unified.videos?.results || []);
        setTrailerKey(trailer?.key || null);
        setIsPlaying(!!trailer?.key);
        setIsVideoLoaded(false);

        setLogoUrl(null);
        setLogoFailed(false);
        fetchLogo(unified.id, unified.media_type).then(l => { if (!cancelled) setLogoUrl(l); });

        const year = unified.release_date ? new Date(unified.release_date).getFullYear() : undefined;
        fetchRelated(unified.id, unified.media_type, 12, year).then(r => {
          if (!cancelled) setRelated(r.filter(t => t.id !== unified.id));
        });
      } catch (err) {
        console.error('Error fetching media details:', err);
        if (!cancelled) setError('Failed to load media details');
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => { cancelled = true; };
  }, [movieId, typeParam]);

  const tvSeasons = useMemo(
    () => (mediaDetails?.media_type === 'tv' ? normalizeSeasons(mediaDetails.seasons) : []),
    [mediaDetails]
  );

  const buildMeta = useCallback((): StreamMeta => {
    const year = mediaDetails?.release_date ? new Date(mediaDetails.release_date).getFullYear() : NaN;
    return {
      tmdb_id: mediaDetails?.id ?? parseInt(movieId),
      media_type: mediaDetails?.media_type === 'tv' ? 'tv' : 'movie',
      title: mediaDetails?.title || '',
      poster_path: mediaDetails?.poster_path || '',
      backdrop_path: mediaDetails?.backdrop_path || '',
      year: Number.isFinite(year) ? year : undefined,
    };
  }, [mediaDetails, movieId]);

  // Saved progress: the movie itself, or the TV episode the profile is in the middle of
  useEffect(() => {
    const pid = activeProfile?.id;
    if (!mediaDetails || !pid) { setSavedProgress(null); return; }
    let cancelled = false;
    (async () => {
      let p: StreamProgress | null = null;
      let next: { season: number; episode: number } | null = null;
      if (mediaDetails.media_type === 'movie') {
        p = findProgress(await getTitleProgress(pid, 'movie', mediaDetails.id));
      } else {
        // Newest entry decides: resume it, or continue with the episode after a finished one
        const latest = (await getTitleProgress(pid, 'tv', mediaDetails.id))[0] || null;
        if (latest && isFinished(latest)) next = nextEpisodeOf(tvSeasons, latest.season, latest.episode);
        else p = latest;
      }
      if (cancelled) return;
      setSavedProgress(p && !isFinished(p) ? p : null);
      setNextUp(next);
    })();
    return () => { cancelled = true; };
  }, [mediaDetails, tvSeasons, activeProfile?.id, progressRefresh]);

  const resumeInfo = savedProgress && savedProgress.position > 0
    ? {
        label: savedProgress.media_type === 'tv' ? episodeLabel(savedProgress.season, savedProgress.episode) : '',
        percent: progressPercent(savedProgress),
        remaining: formatRemaining(savedProgress.position, savedProgress.duration),
      }
    : null;

  const openPlayer = useCallback((req: PlayRequest) => {
    // Stop the hero trailer so its audio doesn't play under the movie
    setIsPlaying(false);
    setPlayRequest(req);
  }, []);

  const closePlayer = useCallback(() => {
    setPlayRequest(null);
    // Let the final progress save land before reloading it
    setTimeout(() => setProgressRefresh(n => n + 1), 600);
  }, []);

  const handleWatch = useCallback(() => {
    if (!mediaDetails) return;
    const meta = buildMeta();
    if (savedProgress?.hash) {
      const req = progressToRequest(savedProgress);
      openPlayer({ ...req, meta: { ...req.meta, ...meta, season: req.meta.season, episode: req.meta.episode, episode_title: req.meta.episode_title } });
      return;
    }
    if (meta.media_type === 'tv') {
      if (savedProgress) {
        openPlayer({ meta: { ...meta, season: savedProgress.season, episode: savedProgress.episode, episode_title: savedProgress.episode_title || undefined } });
      } else if (nextUp) {
        openPlayer({ meta: { ...meta, season: nextUp.season, episode: nextUp.episode } });
      } else {
        openPlayer({ meta: { ...meta, season: tvSeasons[0]?.season_number || 1, episode: 1 } });
      }
      return;
    }
    openPlayer({ meta });
  }, [mediaDetails, buildMeta, savedProgress, nextUp, tvSeasons, openPlayer]);

  // ?play=1 opens the player right away (e.g. from a billboard Play button).
  // Debounced so saved progress can load first and we resume instead of restarting.
  useEffect(() => {
    if (autoPlayHandled.current || !mediaDetails || searchParams?.get('play') !== '1') return;
    const t = setTimeout(() => {
      autoPlayHandled.current = true;
      handleWatch();
    }, 700);
    return () => clearTimeout(t);
  }, [mediaDetails, searchParams, handleWatch]);

  const ytCommand = (func: string) => {
    videoRef.current?.contentWindow?.postMessage(JSON.stringify({ event: 'command', func, args: '' }), '*');
  };

  const toggleMute = () => {
    ytCommand(isMuted ? 'unMute' : 'mute');
    setIsMuted(m => !m);
  };

  // Pause the trailer while the hero is scrolled away, like Netflix
  useEffect(() => {
    const el = heroRef.current;
    if (!el || !trailerKey || !isPlaying) return;
    const obs = new IntersectionObserver(
      ([entry]) => ytCommand(entry.intersectionRatio < 0.35 ? 'pauseVideo' : 'playVideo'),
      { threshold: [0, 0.35, 1] }
    );
    obs.observe(el);
    return () => obs.disconnect();
  }, [trailerKey, isPlaying]);

  if (loading && !mediaDetails) {
    return (
      <NetflixPage>
        <div className="flex min-h-screen items-center justify-center">
          <RedLoader size="large" />
        </div>
      </NetflixPage>
    );
  }

  if (error || !mediaDetails) {
    return (
      <NetflixPage>
        <div className="flex min-h-screen flex-col items-center justify-center px-6 text-center">
          <h1 className="mb-3 text-2xl font-bold">Title not found</h1>
          <p className="mb-6 text-[#b3b3b3]">{error || 'The requested movie or TV show could not be found.'}</p>
          <button onClick={() => router.back()} className="rounded bg-white px-6 py-2 font-bold text-black hover:bg-white/80">
            Go Back
          </button>
        </div>
      </NetflixPage>
    );
  }

  const isTv = mediaDetails.media_type === 'tv';
  const year = mediaDetails.release_date ? new Date(mediaDetails.release_date).getFullYear() : undefined;
  const cast = (mediaDetails.credits?.cast || []).map(p => p.name);
  const crew = mediaDetails.credits?.crew || [];
  const directors = crew.filter(p => p.job === 'Director').map(p => p.name);
  const writers = Array.from(new Set(crew.filter(p => ['Writer', 'Screenplay', 'Story', 'Novel'].includes(p.job || '')).map(p => p.name)));
  const creators = (mediaDetails.created_by || []).map(c => c.name);
  const genres = (mediaDetails.genres || []).map(g => g.name);
  const extra = isTv
    ? (mediaDetails.number_of_seasons
      ? `${mediaDetails.number_of_seasons} Season${mediaDetails.number_of_seasons > 1 ? 's' : ''}`
      : '')
    : formatRuntime(mediaDetails.runtime);
  const backdrop = imageUrl(mediaDetails.backdrop_path, 'original');
  const listItem = { id: mediaDetails.id, mediaType: mediaDetails.media_type };
  const showTrailer = !!trailerKey && isPlaying;

  return (
    <NetflixPage>
      {/* Hero */}
      <div ref={heroRef} className="relative h-[75vw] max-h-[85vh] min-h-[460px] w-full overflow-hidden sm:h-[56.25vw]">
        {backdrop ? (
          <img
            src={backdrop}
            alt=""
            className={`absolute inset-0 h-full w-full object-cover object-top transition-opacity duration-1000 ${showTrailer && isVideoLoaded ? 'opacity-0' : 'opacity-100'}`}
          />
        ) : (
          <div className="absolute inset-0 bg-gradient-to-br from-[#2a2a2a] to-[#141414]" />
        )}

        {showTrailer && (
          <div className={`pointer-events-none absolute inset-0 overflow-hidden transition-opacity duration-1000 ${isVideoLoaded ? 'opacity-100' : 'opacity-0'}`}>
            <iframe
              ref={videoRef}
              title="Trailer"
              src={`https://www.youtube.com/embed/${trailerKey}?autoplay=1&mute=1&controls=0&rel=0&iv_load_policy=3&modestbranding=1&enablejsapi=1&loop=1&playlist=${trailerKey}&disablekb=1&fs=0&cc_load_policy=0&playsinline=1&origin=${typeof window !== 'undefined' ? window.location.origin : ''}`}
              allow="autoplay; encrypted-media"
              referrerPolicy="strict-origin-when-cross-origin"
              onLoad={() => setTimeout(() => setIsVideoLoaded(true), 1200)}
              className="absolute left-1/2 top-1/2 -translate-x-1/2 -translate-y-1/2 border-0"
              style={{ width: '177.78vh', height: '56.25vw', minWidth: '100%', minHeight: '100%' }}
            />
          </div>
        )}

        <div className="absolute inset-0 bg-gradient-to-r from-black/70 via-black/20 to-transparent" />
        <div className="absolute inset-x-0 bottom-0 h-2/5 bg-gradient-to-b from-transparent to-[#141414]" />
        <div className="absolute inset-x-0 top-0 h-28 bg-gradient-to-b from-black/60 to-transparent" />

        <div className="absolute bottom-[12%] left-4 right-4 z-10 md:left-12 md:right-auto md:w-[45%]">
          {logoUrl && !logoFailed ? (
            <img
              src={logoUrl}
              alt={mediaDetails.title}
              onError={() => setLogoFailed(true)}
              className="mb-5 max-h-24 w-auto max-w-[85%] object-contain object-left drop-shadow-[0_4px_20px_rgba(0,0,0,0.7)] md:mb-7 md:max-h-36"
            />
          ) : (
            <h1 className="mb-4 text-3xl font-black leading-tight drop-shadow-lg sm:text-5xl lg:text-6xl">{mediaDetails.title}</h1>
          )}

          <div className="flex items-center gap-2 md:gap-3">
            <WatchButton onClick={handleWatch} resume={!!resumeInfo} detail={resumeInfo?.label || (nextUp ? episodeLabel(nextUp.season, nextUp.episode) : undefined)} />
            <MyListButton item={listItem} />
            {!!mediaDetails.homepage && (
              <a
                href={mediaDetails.homepage}
                target="_blank"
                rel="noopener noreferrer"
                title="Official Site"
                className="flex h-10 w-10 items-center justify-center rounded-full border-2 border-white/50 bg-[#2a2a2a]/60 text-white transition-colors hover:border-white md:h-11 md:w-11"
              >
                <ExternalLink className="h-4 w-4" />
              </a>
            )}
          </div>

          {!!resumeInfo && resumeInfo.percent > 0 && (
            <div className="mt-4 flex items-center gap-3">
              <div className="h-[3px] w-40 overflow-hidden bg-[#5a5a5a] md:w-60">
                <div className="h-full bg-[#E50914]" style={{ width: `${resumeInfo.percent}%` }} />
              </div>
              <span className="text-xs text-[#b3b3b3] md:text-sm">
                {resumeInfo.label ? `${resumeInfo.label} · ` : ''}{resumeInfo.remaining}
              </span>
            </div>
          )}
        </div>

        {showTrailer && (
          <div className="absolute bottom-[14%] right-4 z-10 md:right-12">
            <RoundButton onClick={toggleMute} title={isMuted ? 'Unmute' : 'Mute'}>
              {isMuted ? <VolumeX className="h-5 w-5" /> : <Volume2 className="h-5 w-5" />}
            </RoundButton>
          </div>
        )}
      </div>

      {/* Info */}
      <div className="relative z-10 px-4 md:px-12">
        <div className="grid gap-6 md:grid-cols-[minmax(0,2fr)_minmax(0,1fr)] md:gap-12">
          <div>
            <MetaLine rating={mediaDetails.vote_average} year={year} extra={extra} adult={mediaDetails.adult} />
            {!!mediaDetails.tagline && (
              <p className="mt-3 text-base font-semibold italic text-white/90">{mediaDetails.tagline}</p>
            )}
            <p className="mt-3 max-w-3xl text-sm leading-relaxed text-white md:text-base">
              {mediaDetails.overview || 'No description available.'}
            </p>
          </div>
          <div className="space-y-2">
            <InfoLine label="Cast" values={cast} />
            <InfoLine label="Genres" values={genres} />
            {isTv ? <InfoLine label="Created by" values={creators} /> : <InfoLine label="Director" values={directors} />}
            {isTv && <InfoLine label="Network" values={(mediaDetails.networks || []).map(n => n.name)} />}
          </div>
        </div>

        {/* Episodes (TV) */}
        {isTv && tvSeasons.length > 0 && (
          <div ref={episodesRef} className="mt-10 md:mt-12">
            <SeasonEpisodes
              tmdbId={mediaDetails.id}
              seasons={tvSeasons}
              initialSeason={savedProgress?.season || nextUp?.season || undefined}
              fallbackImage={imageUrl(mediaDetails.backdrop_path, 'w300')}
              refreshKey={progressRefresh}
              onPlay={(season, ep, progress) => {
                if (progress?.hash) {
                  const req = progressToRequest(progress);
                  openPlayer({ ...req, meta: { ...buildMeta(), season, episode: ep.episode_number, episode_title: progress.episode_title || req.meta.episode_title } });
                } else {
                  openPlayer({
                    meta: {
                      ...buildMeta(),
                      season,
                      episode: ep.episode_number,
                      episode_title: ep.name && ep.name !== `Episode ${ep.episode_number}` ? ep.name : undefined,
                    },
                  });
                }
              }}
            />
          </div>
        )}

        <div className="mt-12">
          <MoreLikeThis items={related} />
        </div>

        {/* About */}
        <section className="mt-12 max-w-4xl">
          <h2 className="mb-4 text-xl font-bold md:text-2xl">
            About <span className="font-bold">{mediaDetails.title}</span>
          </h2>
          <div className="space-y-2">
            {isTv ? <InfoLine label="Creators" values={creators} max={10} /> : <InfoLine label="Director" values={directors} max={10} />}
            <InfoLine label="Cast" values={cast} max={15} />
            <InfoLine label="Writer" values={writers} max={8} />
            <InfoLine label="Genres" values={genres} max={10} />
            <InfoLine label={isTv ? 'First aired' : 'Release'} values={formatDate(mediaDetails.release_date) ? [formatDate(mediaDetails.release_date)] : []} />
            {isTv && (mediaDetails.number_of_episodes || 0) > 0 && (
              <InfoLine label="Episodes" values={[String(mediaDetails.number_of_episodes)]} />
            )}
            <InfoLine label="Country" values={(mediaDetails.production_countries || []).map(c => c.name)} max={3} />
            <InfoLine label="Language" values={(mediaDetails.spoken_languages || []).map(l => l.english_name || l.name)} max={3} />
            {!!mediaDetails.status && <InfoLine label="Status" values={[mediaDetails.status]} />}
            {!isTv && (mediaDetails.budget || 0) > 0 && <InfoLine label="Budget" values={[formatCurrency(mediaDetails.budget)]} />}
            {!isTv && (mediaDetails.revenue || 0) > 0 && <InfoLine label="Box office" values={[formatCurrency(mediaDetails.revenue)]} />}
          </div>
        </section>
      </div>

      {!!playRequest && (
        <TorrentStreamPlayer
          {...playRequest}
          seasons={tvSeasons.length > 0 ? tvSeasons : undefined}
          onClose={closePlayer}
        />
      )}
    </NetflixPage>
  );
};

export default TMDBMoviePage;
