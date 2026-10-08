"use client";

import React, { useState, useEffect, useRef, useCallback } from 'react';
import { motion, AnimatePresence } from 'framer-motion';
import {
  Play,
  Star,
  Calendar,
  Clock,
  TrendingUp,
  Sparkles,
  Award,
  Heart,
  RotateCcw,
  Plus,
  Tv,
  Zap,
  Eye,
  Volume2,
  VolumeX
} from 'lucide-react';
import { Notification } from '@/features/notifications/types';
import { getColorPaletteByGenre } from '@/features/notifications/palette';
import { tmdbImage } from '@/shared/lib/image';
import TileEffects from '@/features/notifications/components/TileEffects';

interface NotificationTileProps {
  notification: Notification;
  size: 'small' | 'medium' | 'large' | 'banner' | 'hero';
  className?: string;
  onTrailerEnd?: () => void;
  onTrailerStart?: () => void;
  onTileClick?: () => void;
  autoPlayAudio?: boolean;
  canPlayVideo?: boolean; // New prop to control video playback
}

// Declare global YouTube types
declare global {
  interface Window {
    YT: any;
    onYouTubeIframeAPIReady: () => void;
    __initializedYTPlayers?: Set<string>; // Track initialized players globally
  }
}

// Initialize global player tracking
if (typeof window !== 'undefined' && !window.__initializedYTPlayers) {
  window.__initializedYTPlayers = new Set();
}

const NotificationTile: React.FC<NotificationTileProps> = ({
  notification,
  size,
  className = '',
  onTrailerEnd,
  onTrailerStart,
  onTileClick,
  canPlayVideo = true // Default to true for backward compatibility
}) => {
  // Type alias for cleaner code - notification already has all enhanced fields
  const enhancedNotification = notification;

  const [imageLoaded, setImageLoaded] = useState(false);
  const [imageError, setImageError] = useState(false);
  const [currentImageUrl, setCurrentImageUrl] = useState<string | null>(null);
  const [isMuted, setIsMuted] = useState(true);
  const [ytReady, setYtReady] = useState(false);
  const [videoReady, setVideoReady] = useState(false);
  const [trailerPlaying, setTrailerPlaying] = useState(false);
  const [trailerFailed, setTrailerFailed] = useState(false);
  const playerRef = useRef<any>(null);
  const playerInitializedRef = useRef(false);
  const failedUrlsRef = useRef<Set<string>>(new Set());
  const hasPlayedRef = useRef(false);
  const containerIdRef = useRef(`yt-player-${notification.id}`); // Stable container ID

  // Store callbacks in refs to prevent re-initialization
  const onTrailerEndRef = useRef(onTrailerEnd);
  const onTrailerStartRef = useRef(onTrailerStart);
  const onTileClickRef = useRef(onTileClick);

  // Update refs when callbacks change
  useEffect(() => {
    onTrailerEndRef.current = onTrailerEnd;
    onTrailerStartRef.current = onTrailerStart;
    onTileClickRef.current = onTileClick;
  }, [onTrailerEnd, onTrailerStart, onTileClick]);

  // Initialize image URL once
  useEffect(() => {
    const backdropUrl = getBackdropUrl();
    if (backdropUrl && !currentImageUrl) {
      setCurrentImageUrl(backdropUrl);
    }
  }, [notification.id]); // Only on mount or ID change

  // Load YouTube IFrame API
  useEffect(() => {
    if (window.YT && window.YT.Player) {
      setYtReady(true);
      return;
    }

    const tag = document.createElement('script');
    tag.src = 'https://www.youtube.com/iframe_api';
    const firstScriptTag = document.getElementsByTagName('script')[0];
    firstScriptTag.parentNode?.insertBefore(tag, firstScriptTag);

    window.onYouTubeIframeAPIReady = () => {
      setYtReady(true);
    };
  }, []);

  // Initialize YouTube player - STRICT SIZE AND TRAILER CHECKS
  useEffect(() => {
    // STRICT CHECKS: Only hero and large tiles with explicit trailers
    if (!ytReady ||
      (size !== 'hero' && size !== 'large') ||
      !enhancedNotification.trailer_key) return; // Only hero and large tiles

    const containerId = containerIdRef.current;

    // Only initialize once per notification - CRITICAL: Check if player already exists
    if (playerInitializedRef.current || playerRef.current) {
      return;
    }

    // IMMEDIATE initialization - no delay for instant playback
    const container = document.getElementById(containerId);
    if (!container) {
      // Container not ready yet, try again very soon
      setTimeout(() => {
        const retryContainer = document.getElementById(containerId);
        if (!retryContainer) return;
        initializePlayer(retryContainer);
      }, 100);
      return;
    }

    initializePlayer(container);

    function initializePlayer(container: HTMLElement) {
      try {
        playerRef.current = new window.YT.Player(containerId, {
          videoId: enhancedNotification.trailer_key,
          playerVars: {
            autoplay: 1,
            mute: 1,
            controls: 0,
            showinfo: 0,
            rel: 0,
            iv_load_policy: 3,
            modestbranding: 1,
            playsinline: 1,
            disablekb: 1,
            fs: 0,
            cc_load_policy: 0,
            enablejsapi: 1,
            loop: 0,
            start: 10, // Start 10 seconds in to skip intro
            origin: window.location.origin,
          },
          events: {
            onStateChange: (event: any) => {
              if (event.data === 1) { // Playing
                setVideoReady(true);
                setTrailerPlaying(true);
                onTrailerStartRef.current?.();
              } else if (event.data === 0) { // Ended
                hasPlayedRef.current = true; // Mark as played
                event.target.stopVideo(); // FORCE STOP to prevent looping/restarting
                setVideoReady(false);
                setTrailerPlaying(false);
                onTrailerEndRef.current?.(); // Notify parent that trailer ended
              } else if (event.data === 2) { // Paused
                setTrailerPlaying(false);
              }
            },
            onReady: (event: any) => {
              if (hasPlayedRef.current) return; // Don't replay if already finished
              const iframe = event.target.getIframe();
              if (iframe) {
                iframe.referrerPolicy = "strict-origin-when-cross-origin";
              }
              if (!isMuted) {
                event.target.unMute();
              }
              event.target.seekTo(10, true); // Explicitly seek to 10s with allowSeekAhead
              event.target.playVideo();

              // Set up interval to end video 15 seconds early (matching LocalMoviesHeroSlider)
              const checkEndTime = setInterval(() => {
                try {
                  const player = event.target;
                  const duration = player.getDuration();
                  const currentTime = player.getCurrentTime();

                  // End 15 seconds before actual end
                  if (duration > 0 && currentTime >= duration - 15) {
                    clearInterval(checkEndTime);
                    hasPlayedRef.current = true;
                    player.stopVideo();
                    setVideoReady(false);
                    setTrailerPlaying(false);
                    onTrailerEndRef.current?.();
                  }
                } catch (e) {
                  // Player might be destroyed
                  clearInterval(checkEndTime);
                }
              }, 500);

              // Store interval ref for cleanup
              (event.target as any)._endCheckInterval = checkEndTime;
            },
            onError: (event: any) => {
              console.error('Trailer error:', event.data);
              setVideoReady(false);
              setTrailerFailed(true);
            }
          },
        });
        playerInitializedRef.current = true;
      } catch (error) {
        console.error('Failed to initialize trailer:', error);
        setTrailerFailed(true);
      }
    }
  }, [ytReady, notification.id, size]); // CRITICAL: Minimal dependencies to prevent re-initialization

  const getNotificationIcon = (type: string) => {
    const iconProps = { className: "w-4 h-4" };

    switch (type) {
      case 'tmdb_upcoming':
      case 'tmdb_upcoming_tv':
      case 'tmdb_coming_soon':
        return <Calendar {...iconProps} />;
      case 'tmdb_now_playing':
      case 'tmdb_now_airing_tv':
        return <Sparkles {...iconProps} />;
      case 'tmdb_trending':
      case 'local_trending':
        return <TrendingUp {...iconProps} />;
      case 'new_episodes':
        return <Tv {...iconProps} />;
      case 'new_movies':
      case 'recently_added':
        return <Plus {...iconProps} />;
      case 'movie_suggestion':
        return <Award {...iconProps} />;
      case 'single_movie_suggestion':
        return <Star {...iconProps} />;
      case 'watch_again':
      case 'continue_watching':
        return <RotateCcw {...iconProps} />;
      case 'genre_based':
        return <Heart {...iconProps} />;
      default:
        return <Zap {...iconProps} />;
    }
  };

  const getThemeColors = () => {
    if (enhancedNotification.genres && enhancedNotification.genres.length > 0) {
      return getColorPaletteByGenre(enhancedNotification.genres);
    }

    switch (enhancedNotification.category || notification.type) {
      case 'trending':
        return {
          primary: '#ff6b35',
          secondary: '#ff8c42',
          accent: '#ffa726',
          background: 'linear-gradient(135deg, #1a0f00 0%, #331e00 50%, #000000 100%)',
          text: '#ffffff',
        };
      case 'new':
        return {
          primary: '#4caf50',
          secondary: '#66bb6a',
          accent: '#81c784',
          background: 'linear-gradient(135deg, #0d1a0d 0%, #1a331a 50%, #000000 100%)',
          text: '#ffffff',
        };
      case 'recommended':
        return {
          primary: '#e91e63',
          secondary: '#f06292',
          accent: '#f48fb1',
          background: 'linear-gradient(135deg, #1a0d14 0%, #330d1a 50%, #000000 100%)',
          text: '#ffffff',
        };
      default:
        return {
          primary: '#e50914',
          secondary: '#831010',
          accent: '#ff6b6b',
          background: 'linear-gradient(135deg, #141414 0%, #1a1a1a 50%, #000000 100%)',
          text: '#ffffff',
        };
    }
  };

  // Notification images come from TMDB only; tmdbImage routes them through /tmdb-img
  const firstUsable = (...urls: Array<string | undefined>) => {
    for (const url of urls) {
      const proxied = tmdbImage(url, 'w1280');
      if (proxied && !failedUrlsRef.current.has(proxied)) return proxied;
    }
    return null;
  };

  const firstMedia = enhancedNotification.media_details?.[0];
  const getBackdropUrl = () => firstUsable(enhancedNotification.backdrop_url, firstMedia?.backdrop_url);
  const getPosterUrl = () => firstUsable(enhancedNotification.poster_url, firstMedia?.poster_url);

  // Helper function to determine if this is a TV series notification
  const isTVSeries = () => {
    return notification.type === 'tmdb_upcoming_tv' || 
           notification.type === 'tmdb_now_airing_tv' ||
           notification.type === 'new_episodes' ||
           (enhancedNotification.media_details && 
            enhancedNotification.media_details.some(media => media.source_type === 'tmdb' && 
            (notification.type.includes('tv') || media.title?.toLowerCase().includes('season'))));
  };

  const getLogoUrl = () => firstUsable(enhancedNotification.logo_url?.trim() || undefined);

  const handleImageError = useCallback((url: string) => {
        failedUrlsRef.current.add(url);
    setImageError(true);
    setImageLoaded(true);
  }, []);

  const formatTimestamp = (timestamp: number) => {
    const now = Date.now() / 1000;
    const diff = now - timestamp;

    if (diff < 60) return "Just now";
    if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
    if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
    return `${Math.floor(diff / 86400)}d ago`;
  };

  const themeColors = getThemeColors();
  const backdropUrl = getBackdropUrl();
  const posterUrl = getPosterUrl();
  const logoUrl = getLogoUrl();

  // Size-specific styling - ENHANCED for better content display at all sizes
  const getSizeClasses = () => {
    switch (size) {
      case 'hero':
        return {
          container: 'p-4 md:p-6',
          title: 'text-xl sm:text-2xl md:text-3xl font-bold',
          message: 'text-xs sm:text-sm md:text-base', // Reduced from text-sm sm:text-base md:text-lg
          showDetails: true,
          showTrailer: true, // Hero tiles show trailers
          showPoster: true,
          useWhiteText: false, // Use red title
        };
      case 'banner':
        return {
          container: 'p-2 md:p-3', // Reduced padding for better space utilization
          title: 'text-base sm:text-lg md:text-xl font-bold', // Slightly smaller title
          message: 'text-xs', // Reduced from text-xs sm:text-sm
          showDetails: true,
          showTrailer: false, // Banner tiles don't show trailers
          showPoster: false, // Hide poster to save space
          useWhiteText: false, // Use red title
        };
      case 'large':
        return {
          container: 'p-3 md:p-4',
          title: 'text-base sm:text-lg md:text-xl font-bold',
          message: 'text-xs sm:text-sm', // Reduced from text-sm
          showDetails: true,
          showTrailer: true, // Large tiles show trailers
          showPoster: true,
          useWhiteText: false, // Use red title
        };
      case 'medium':
        return {
          container: 'p-2 md:p-3',
          title: 'text-sm sm:text-base md:text-lg font-bold',
          message: 'text-xs', // Reduced from text-xs sm:text-sm
          showDetails: true,
          showTrailer: false, // Medium tiles NO TRAILERS
          showPoster: true,
          useWhiteText: false, // Use red title
        };
      case 'small':
        return {
          container: 'p-2 md:p-3',
          title: 'text-sm font-bold text-white leading-tight',
          message: 'text-xs text-white/90 leading-tight',
          showDetails: true, // Show content for small tiles
          showTrailer: false, // Small tiles NEVER show trailers
          showPoster: false, // Don't show poster for small tiles to save space
          useWhiteText: true, // Use white text for better readability
        };
      default:
        return {
          container: 'p-3',
          title: 'text-base font-bold',
          message: 'text-sm',
          showDetails: true,
          showTrailer: false, // Default: no trailers
          showPoster: true,
          useWhiteText: false, // Use red title
        };
    }
  };

  const sizeConfig = getSizeClasses();

  return (
    <motion.div
      className={`relative overflow-hidden rounded-xl border border-white/10 backdrop-blur-sm ${className} cursor-pointer`}
      style={{
        background: themeColors.background,
        boxShadow: `0 8px 32px ${themeColors.primary}20, inset 0 1px 0 rgba(255,255,255,0.1)`,
      }}
      onClick={(e) => {
        // Only trigger if not clicking on buttons
        if ((e.target as HTMLElement).closest('button')) return;
        onTileClickRef.current?.();
      }}
      transition={{ duration: 0.2 }}
      animate={{
        // Subtle breathing animation for high priority items
        ...(enhancedNotification.priority === 'high' && {
          boxShadow: [
            `0 8px 32px ${themeColors.primary}20, inset 0 1px 0 rgba(255,255,255,0.1)`,
            `0 12px 40px ${themeColors.primary}40, inset 0 1px 0 rgba(255,255,255,0.15)`,
            `0 8px 32px ${themeColors.primary}20, inset 0 1px 0 rgba(255,255,255,0.1)`,
          ]
        })
      }}
      {...(enhancedNotification.priority === 'high' && {
        transition: {
          boxShadow: { duration: 2, repeat: Infinity, ease: "easeInOut" },
        }
      })}
    >
      {/* Background Image - Show backdrop, hide when trailer plays */}
      {backdropUrl && !imageError ? (
        <motion.div
          initial={{ scale: 1.05, opacity: 0 }}
          animate={{ 
            scale: imageLoaded ? 1 : 1.05,
            opacity: videoReady ? 0 : (imageLoaded ? 1 : 0)
          }}
          transition={{ 
            scale: { duration: 1.2, ease: "easeOut" },
            opacity: { duration: 0.5, ease: "easeInOut" }
          }}
          className="absolute inset-0 z-0"
        >
          <img
            src={currentImageUrl || backdropUrl}
            alt="Backdrop"
            className="w-full h-full object-cover"
            loading="eager"
            onLoad={() => {
              setImageLoaded(true);
              setImageError(false);
            }}
            onError={() => {
                            handleImageError(currentImageUrl || backdropUrl);
              // Try poster as backdrop fallback
              if (posterUrl && currentImageUrl !== posterUrl && !failedUrlsRef.current.has(posterUrl)) {
                setCurrentImageUrl(posterUrl);
                setImageError(false);
                setImageLoaded(false);
              } else {
                setImageError(true);
                setImageLoaded(true);
              }
            }}
          />
        </motion.div>
      ) : null}

      {/* YouTube Player - ONLY for Hero and Large tiles - Hidden until ready */}
      {(size === 'hero' || size === 'large') && enhancedNotification.trailer_key && !trailerFailed && (
        <div
          className="absolute inset-0 z-10 flex items-center justify-center overflow-hidden"
          style={{
            opacity: videoReady ? 1 : 0,
            pointerEvents: videoReady ? 'auto' : 'none',
            transition: 'opacity 0.6s ease-out'
          }}
        >
          <div className="relative w-full h-full overflow-hidden">
            <div
              id={containerIdRef.current}
              className="absolute top-1/2 left-1/2 transform -translate-x-1/2 -translate-y-1/2"
              style={{
                width: '120vw',
                height: '120vh',
                minWidth: '200vh',
                minHeight: '70vw',
              }}
            />
          </div>
        </div>
      )}

      {/* Gradient Overlays - Always present for text readability */}
      <motion.div
        initial={{ opacity: 0 }}
        animate={{ opacity: 1 }}
        transition={{ duration: 0.5, delay: 0.2 }}
        className="absolute inset-0 z-15 pointer-events-none"
      >
        {/* Minimal gradients to preserve backdrop/video visibility while ensuring text readability */}
        <div className="absolute inset-0 bg-gradient-to-t from-black/80 via-transparent to-transparent" />
        <div className="absolute bottom-0 left-0 right-0 h-1/2 bg-gradient-to-t from-black/90 via-black/40 to-transparent" />
        {/* Text area protection */}
        <div className="absolute bottom-0 left-0 right-0 h-1/3 bg-gradient-to-t from-black/95 to-transparent" />
      </motion.div>

      {/* Tile Effects */}
      <TileEffects priority={enhancedNotification.priority || 'medium'} themeColors={themeColors} />

      {/* Content */}
      <div className={`absolute inset-0 flex flex-col justify-end z-20 ${sizeConfig.container} ${size === 'banner' ? 'pb-2' : 'pb-4'}`}>
        {/* Icon and Timestamp with conditional visibility enhancement */}
        <div className={`flex items-center gap-2 ${size === 'banner' ? 'mb-1' : 'mb-2'}`}>
          <div
            className="rounded-full backdrop-blur-md border p-1.5"
            style={{
              backgroundColor: `${themeColors.primary}40`,
              borderColor: `${themeColors.primary}60`,
              boxShadow: '0 2px 8px rgba(0,0,0,0.8)',
            }}
          >
            {getNotificationIcon(notification.type)}
          </div>
          {/* TV Series indicator for small tiles */}
          {isTVSeries() && size === 'small' && (
            <div
              className="rounded-full backdrop-blur-md border p-1"
              style={{
                backgroundColor: `${themeColors.accent}40`,
                borderColor: `${themeColors.accent}60`,
                boxShadow: '0 2px 8px rgba(0,0,0,0.8)',
              }}
            >
              <Tv className="w-3 h-3 text-red-400" />
            </div>
          )}
          <div
            className="text-xs font-medium"
            style={{
              color: '#ef4444',
              textShadow: '0 1px 3px rgba(0,0,0,0.9), 0 2px 6px rgba(0,0,0,0.8)',
            }}
          >
            {formatTimestamp(notification.timestamp)}
          </div>
        </div>

        {/* Title Section - Logo with text fallback */}
        <div className={size === 'banner' ? 'mb-1' : 'mb-2'}>
          {/* Logo - only for larger tiles and when available */}
          {logoUrl && size !== 'small' && (
            <img
              src={logoUrl}
              alt={notification.title}
              className={`w-auto mb-2 ${size === 'hero' ? 'max-h-16 md:max-h-20' :
                size === 'large' ? 'max-h-12 md:max-h-16' :
                  size === 'banner' ? 'max-h-10 md:max-h-12' :
                    size === 'medium' ? 'max-h-8 md:max-h-10' :
                      'max-h-6 md:max-h-8'
                }`}
              onLoad={() => {
                // Hide text title when logo loads successfully
                const textTitle = document.getElementById(`text-title-${notification.id}`);
                if (textTitle) {
                  textTitle.style.display = 'none';
                }
              }}
              onError={(e) => {
                e.currentTarget.style.display = 'none';
                // Ensure text title is visible
                const textTitle = document.getElementById(`text-title-${notification.id}`);
                if (textTitle) {
                  textTitle.style.display = 'block';
                }
                handleImageError(logoUrl);
              }}
            />
          )}

          {/* Text Title - ALWAYS present as fallback */}
          <h1
            id={`text-title-${notification.id}`}
            className={`${sizeConfig.title}`}
            style={{
              display: 'block', // Always visible initially
              color: sizeConfig.useWhiteText ? '#ffffff' : '#e50914', // White for small tiles, Netflix red for others
              fontWeight: 'bold',
              zIndex: 100,
              position: 'relative',
              lineHeight: size === 'small' ? '1.2' : '1.3',
              textShadow: sizeConfig.useWhiteText 
                ? '0 2px 4px rgba(0,0,0,1), 0 4px 8px rgba(0,0,0,0.9), 1px 1px 0px rgba(0,0,0,1)' // Dark shadow for white text
                : '0 2px 4px rgba(255,255,255,0.3), 0 4px 8px rgba(255,255,255,0.2), 0 0 20px rgba(0,0,0,0.8), 1px 1px 0px rgba(0,0,0,1)', // White glow + dark shadow for red text
            }}
          >
            {notification.title}
          </h1>
        </div>

        {/* Enhanced Description with strong text shadow */}
        {(size === 'hero' || size === 'large' || size === 'banner') && enhancedNotification.overview ? (
          <p
            className={`leading-snug ${size === 'banner' ? 'mb-1' : 'mb-3'} ${sizeConfig.message}`}
            style={{
              color: '#ffffff',
              textShadow: '0 1px 3px rgba(0,0,0,1), 0 2px 6px rgba(0,0,0,0.9), 1px 1px 0px rgba(0,0,0,0.8)',
              display: '-webkit-box',
              WebkitLineClamp: size === 'hero' ? 2 : size === 'large' ? 2 : 1, // Reduced from 3:2
              WebkitBoxOrient: 'vertical',
              overflow: 'hidden',
              textOverflow: 'ellipsis',
            }}
          >
            {enhancedNotification.overview}
          </p>
        ) : (
          <p
            className={`leading-snug mb-2 ${sizeConfig.message}`}
            style={{
              color: sizeConfig.useWhiteText ? '#ffffff' : '#ffffff',
              textShadow: sizeConfig.useWhiteText
                ? '0 2px 4px rgba(0,0,0,1), 0 4px 8px rgba(0,0,0,0.9), 1px 1px 0px rgba(0,0,0,1)' // Strong dark shadow for white text on small tiles
                : '0 1px 3px rgba(0,0,0,1), 0 2px 6px rgba(0,0,0,0.9), 1px 1px 0px rgba(0,0,0,0.8)',
              display: '-webkit-box',
              WebkitLineClamp: 1, // Always 1 line for small/medium
              WebkitBoxOrient: 'vertical',
              overflow: 'hidden',
              textOverflow: 'ellipsis',
            }}
          >
            {notification.message}
          </p>
        )}

        {/* Movie Details Row - Rating, Year, Genres, TV Tag */}
        {(size === 'hero' || size === 'large' || size === 'banner') && (
          <div className={`flex flex-wrap items-center gap-2 ${size === 'banner' ? 'mb-1' : 'mb-3'} text-xs`}>
            {/* TV Series Tag */}
            {isTVSeries() && (
              <span className="inline-flex items-center gap-1 px-2 py-1 rounded-full bg-red-500/20 text-red-400 border border-red-500/30">
                <Tv className="w-3 h-3" />
                TV
              </span>
            )}
            {!!enhancedNotification.rating && enhancedNotification.rating > 0 && (
              <span className="inline-flex items-center gap-1 px-2 py-1 rounded-full bg-yellow-500/20 text-yellow-400 border border-yellow-500/30">
                <Star className="w-3 h-3 fill-current" />
                {enhancedNotification.rating.toFixed(1)}
              </span>
            )}
            {((enhancedNotification.year && enhancedNotification.year > 1900) || enhancedNotification.release_date) && (
              <span className="inline-flex items-center px-2 py-1 rounded-full bg-blue-500/20 text-blue-400 border border-blue-500/30">
                {(enhancedNotification.year && enhancedNotification.year > 1900) ? enhancedNotification.year : new Date(enhancedNotification.release_date!).getFullYear()}
              </span>
            )}
            {enhancedNotification.genres && enhancedNotification.genres.length > 0 && (
              <span className="inline-flex items-center px-2 py-1 rounded-full bg-purple-500/20 text-purple-400 border border-purple-500/30">
                {enhancedNotification.genres.slice(0, 2).join(' • ')}
              </span>
            )}
            {!!enhancedNotification.runtime && enhancedNotification.runtime > 0 && (
              <span className="inline-flex items-center gap-1 px-2 py-1 rounded-full bg-green-500/20 text-green-400 border border-green-500/30">
                <Clock className="w-3 h-3" />
                {Math.floor(enhancedNotification.runtime / 60)}h {enhancedNotification.runtime % 60}m
              </span>
            )}
          </div>
        )}

        {/* Continue Watching Progress */}
        {notification.type === 'continue_watching' && enhancedNotification.progress && (
          <div className="mb-3">
            <div className="flex items-center justify-between text-xs text-white/70 mb-1">
              <span>{Math.round(enhancedNotification.progress)}% watched</span>
              {enhancedNotification.remaining_min && (
                <span>{enhancedNotification.remaining_min}m left</span>
              )}
            </div>
            <div className="w-full h-1 bg-white/20 rounded-full overflow-hidden">
              <motion.div
                className="h-full bg-red-600"
                initial={{ width: 0 }}
                animate={{ width: `${enhancedNotification.progress}%` }}
                transition={{ duration: 1, delay: 0.5 }}
              />
            </div>
          </div>
        )}

        {/* Movie Grid for Multi-item Notifications - TMDB and Local URLs */}
        {sizeConfig.showDetails && enhancedNotification.media_details && enhancedNotification.media_details.length > 1 && (
          <div className="flex gap-2 mb-3 overflow-x-auto scrollbar-hide">
            {enhancedNotification.media_details.slice(0, size === 'hero' ? 6 : 4).map((media) => {
              const mediaPosterUrl = tmdbImage(media.poster_url, 'w185');
              if (!mediaPosterUrl) return null;

              return (
                <div key={`${media.source_type}_${media.source_id}`} className="flex-shrink-0">
                  <div className="w-12 h-16 rounded overflow-hidden border border-white/20 bg-gray-800">
                    <img
                      src={mediaPosterUrl}
                      alt={media.title}
                      className="w-full h-full object-cover"
                      onError={(e) => {
                        // Just hide on error, no retries
                        e.currentTarget.style.display = 'none';
                      }}
                    />
                  </div>
                </div>
              );
            })}
          </div>
        )}

        {/* Single Movie Details - TMDB and Local URLs */}
        {sizeConfig.showPoster && posterUrl && !enhancedNotification.media_details && (
          <div className="flex items-start gap-3 mb-3">
            <div className="flex-shrink-0">
              <div className="w-16 h-24 rounded border border-white/20 bg-gray-800 overflow-hidden">
                <img
                  src={posterUrl}
                  alt="Poster"
                  className="w-full h-full object-cover"
                  onError={(e) => {
                    // Just hide on error, no retries
                    e.currentTarget.style.display = 'none';
                    handleImageError(posterUrl);
                  }}
                />
              </div>
            </div>
            {sizeConfig.showDetails && (
              <div className="flex-1 min-w-0">
                {/* TV Series Tag for single movie details */}
                {isTVSeries() && (
                  <div className="flex items-center gap-1 mb-1">
                    <Tv className="w-3 h-3 text-red-400" />
                    <span className="text-red-400 text-xs font-medium">TV Series</span>
                  </div>
                )}
                {!!enhancedNotification.rating && enhancedNotification.rating > 0 && (
                  <div className="flex items-center gap-1 mb-1">
                    <Star className="w-3 h-3 text-yellow-400 fill-current" />
                    <span className="text-white/80 text-xs font-medium">
                      {enhancedNotification.rating.toFixed(1)}
                    </span>
                  </div>
                )}
                {enhancedNotification.genres && enhancedNotification.genres.length > 0 && (
                  <div className="flex flex-wrap gap-1">
                    {enhancedNotification.genres.slice(0, 2).map((genre) => (
                      <span
                        key={genre}
                        className="px-1.5 py-0.5 text-xs rounded-full border font-medium"
                        style={{
                          backgroundColor: `${themeColors.primary}20`,
                          borderColor: `${themeColors.primary}40`,
                          color: themeColors.accent
                        }}
                      >
                        {genre}
                      </span>
                    ))}
                  </div>
                )}
              </div>
            )}
          </div>
        )}

        {/* Sound Control Icon - ONLY for Hero and Large tiles with video */}
        {(size === 'hero' || size === 'large') && !!enhancedNotification.trailer_key && !trailerFailed && (
          <div className="absolute top-2 right-2 z-30">
            <button
              onClick={(e) => {
                e.stopPropagation();
                const newMutedState = !isMuted;
                setIsMuted(newMutedState);

                // Control YouTube player audio
                if (playerRef.current) {
                  try {
                    if (newMutedState) {
                      playerRef.current.mute();
                    } else {
                      playerRef.current.unMute();
                    }
                  } catch (error) {
                    console.error('Failed to control audio:', error);
                  }
                }
              }}
              className="backdrop-blur-md rounded-full border p-2 transition-all hover:scale-110 bg-black/50 hover:bg-black/70"
              style={{
                borderColor: `${themeColors.primary}50`
              }}
              title={isMuted ? "Unmute" : "Mute"}
            >
              {isMuted ?
                <VolumeX className="w-4 h-4 text-white" /> :
                <Volume2 className="w-4 h-4 text-white" />
              }
            </button>
          </div>
        )}

        {/* No Action Buttons - Removed for cleaner tile appearance */}
      </div>
    </motion.div>
  );
};

// Custom comparison function to prevent re-renders when notification content is the same
const MemoizedNotificationTile = React.memo(NotificationTile, (prevProps, nextProps) => {
  // CRITICAL: Only compare notification ID and size - ignore callbacks completely
  // Callbacks are stored in refs inside the component, so they don't need to trigger re-renders
  return prevProps.notification.id === nextProps.notification.id &&
    prevProps.size === nextProps.size &&
    prevProps.canPlayVideo === nextProps.canPlayVideo &&
    prevProps.className === nextProps.className;
});

MemoizedNotificationTile.displayName = 'MemoizedNotificationTile';

export default MemoizedNotificationTile;