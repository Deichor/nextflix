"use client";

import React, { useState, useEffect, useRef } from 'react';
import { motion } from 'framer-motion';
import { Splide, SplideSlide } from '@splidejs/react-splide';
import '@splidejs/react-splide/css';
import { Play, Info, Volume2, VolumeX, Plus, Check, Star } from 'lucide-react';
import { useRouter } from 'next/navigation';
import YouTube from 'react-youtube';
import { tmdbImage } from '@/shared/lib/image';
import { NfTitle, fetchLogo, getJson } from '@/features/catalog/titles';
import { useMyList } from '@/features/my-list/store';

// A title plus the provider it is featured on (mixed showcase hero)
export type HeroItem = NfTitle & {
    trailerUrl?: string;
    provider?: {
        name: string;
        logo: string;
        color: string;
    };
};

interface ProviderHeroProps {
    providerName?: string;
    providerLogo?: string;
    brandColor?: string;
    items: HeroItem[];
}

const getYouTubeId = (url?: string | null) => {
    if (!url) return null;
    const match = url.match(/^.*(youtu.be\/|v\/|u\/\w\/|embed\/|watch\?v=|&v=)([^#&?]*).*/);
    return match && match[2].length === 11 ? match[2] : null;
};

const ProviderHero: React.FC<ProviderHeroProps> = ({
    providerName = "Streaming",
    providerLogo = "",
    brandColor = "#e50914",
    items,
}) => {
    const router = useRouter();
    const { has, toggle } = useMyList();
    const [activeSlideIndex, setActiveSlideIndex] = useState(0);
    const [isPlaying, setIsPlaying] = useState(false);
    const [isMuted, setIsMuted] = useState(true);
    const [showTrailer, setShowTrailer] = useState(false);
    const [trailerUrl, setTrailerUrl] = useState<string | null>(null);
    const [logoUrl, setLogoUrl] = useState<string | null>(null);
    const [isVideoLoaded, setIsVideoLoaded] = useState(false);
    const playerRef = useRef<any>(null);
    const timeoutRef = useRef<NodeJS.Timeout | null>(null);

    const activeItem: HeroItem | undefined = items[activeSlideIndex] || items[0];
    const inList = !!activeItem && has(activeItem);

    // Branding of the active item's provider, or the page's provider
    const currentProvider = activeItem?.provider || {
        name: providerName,
        logo: providerLogo,
        color: brandColor
    };

    const backdropUrl = (t: HeroItem) => (t.backdropPath ? tmdbImage(t.backdropPath, 'original') : t.backdrop);
    const posterUrl = (t: HeroItem) => (t.posterPath ? tmdbImage(t.posterPath, 'w500') : t.poster);

    const handleSlideChange = (splide: any) => {
        const newIndex = splide.index;
        if (newIndex === activeSlideIndex) return;

        setActiveSlideIndex(newIndex);
        setShowTrailer(false);
        setIsPlaying(false);
        setIsVideoLoaded(false);

        if (timeoutRef.current) clearTimeout(timeoutRef.current);
        // Start the trailer after the slide transition
        timeoutRef.current = setTimeout(() => setShowTrailer(true), 500);
    };

    useEffect(() => {
        timeoutRef.current = setTimeout(() => setShowTrailer(true), 500);
        return () => {
            if (timeoutRef.current) clearTimeout(timeoutRef.current);
        };
    }, []);

    const activeId = activeItem?.id;
    const activeType = activeItem?.mediaType;
    const activeTrailer = activeItem?.trailerUrl;

    // Title logo of the active item
    useEffect(() => {
        if (!activeId || !activeType) return;
        let cancelled = false;
        setLogoUrl(null);
        fetchLogo(activeId, activeType).then(l => { if (!cancelled) setLogoUrl(l); });
        return () => { cancelled = true; };
    }, [activeId, activeType]);

    // Trailer: from the provider payload, otherwise from the TMDB details
    useEffect(() => {
        if (!activeId || !activeType) return;
        setTrailerUrl(activeTrailer || null);
        if (activeTrailer) return;
        let cancelled = false;
        const timer = setTimeout(async () => {
            const res = await getJson<any>(`/api/tmdb-movie/${activeId}?type=${activeType}`);
            const videos: any[] = (res?.data || res)?.videos?.results || [];
            const trailer = videos.find(v => v.site === 'YouTube' && (v.type === 'Trailer' || v.type === 'Teaser'));
            if (!cancelled && trailer) setTrailerUrl(`https://www.youtube.com/watch?v=${trailer.key}`);
        }, 100);
        return () => { cancelled = true; clearTimeout(timer); };
    }, [activeId, activeType, activeTrailer]);

    const youtubeId = getYouTubeId(trailerUrl || activeTrailer);

    const toggleMute = () => {
        if (playerRef.current && isPlaying) {
            if (isMuted) playerRef.current.unMute();
            else playerRef.current.mute();
        }
        setIsMuted(!isMuted);
    };

    if (!items || items.length === 0 || !activeItem) return null;

    return (
        <div className="relative w-full h-[85vh] bg-black text-white overflow-hidden group">
            <Splide
                options={{
                    type: 'fade',
                    rewind: true,
                    pagination: true,
                    arrows: false,
                    autoplay: false,
                    interval: 8000,
                    pauseOnHover: false,
                    speed: 1000,
                }}
                className="h-full w-full absolute inset-0"
                onMoved={handleSlideChange}
            >
                {items.map((item, index) => (
                    <SplideSlide key={`${item.key}-${index}`} className="h-full w-full">
                        {/* Backdrop - Always visible */}
                        {backdropUrl(item) ? (
                            <img
                                src={backdropUrl(item)}
                                alt={item.title}
                                className="absolute inset-0 w-full h-full object-cover z-0"
                                loading={index === 0 ? "eager" : "lazy"}
                                onError={(e) => {
                                    const target = e.target as HTMLImageElement;
                                    if (target.src.includes('/original/')) {
                                        target.src = target.src.replace('/original/', '/w1280/');
                                    } else {
                                        target.style.visibility = 'hidden';
                                    }
                                }}
                            />
                        ) : (
                            <div className="absolute inset-0 w-full h-full bg-gradient-to-br from-gray-900 to-black z-0" />
                        )}

                        {/* Trailer Video Overlay */}
                        {index === activeSlideIndex && showTrailer && youtubeId && (
                            <div className="absolute inset-0 w-full h-full overflow-hidden pointer-events-none z-10">
                                <div 
                                    className="absolute top-1/2 left-1/2 transform -translate-x-1/2 -translate-y-1/2"
                                    style={{ 
                                        width: '120vw', 
                                        height: '120vh', 
                                        minWidth: '177.77vh', 
                                        minHeight: '56.25vw' 
                                    }}
                                >
                                    <YouTube
                                        videoId={youtubeId}
                                        opts={{
                                            height: '100%',
                                            width: '100%',
                                            playerVars: {
                                                autoplay: 1,
                                                controls: 0,
                                                disablekb: 1,
                                                fs: 0,
                                                iv_load_policy: 3,
                                                modestbranding: 1,
                                                rel: 0,
                                                showinfo: 0,
                                                mute: 0,
                                                loop: 1,
                                                playlist: youtubeId,
                                                start: 10,
                                                origin: typeof window !== 'undefined' ? window.location.origin : undefined,
                                                vq: 'hd1080',
                                            },
                                        }}
                                        onReady={(event) => {
                                            playerRef.current = event.target;
                                            setIsVideoLoaded(true);
                                            event.target.playVideo();
                                        }}
                                        onStateChange={(event) => {
                                            if (event.data === 1) {
                                                setIsPlaying(true);
                                            }
                                            if (event.data === 0 || event.data === 2) {
                                                setIsPlaying(false);
                                            }
                                        }}
                                        onError={(event) => {
                                            setIsPlaying(false);
                                            setIsVideoLoaded(false);
                                        }}
                                        className="w-full h-full"
                                        style={{ pointerEvents: 'none' }}
                                    />
                                </div>
                            </div>
                        )}
                    </SplideSlide>
                ))}
            </Splide>

            {/* Content Overlay */}
            <div className="absolute inset-0 z-30 flex flex-col justify-center px-4 md:px-12 lg:px-24 pointer-events-none">
                <div className="max-w-7xl w-full pointer-events-auto mt-20">
                    <motion.div
                        key={activeItem.key}
                        initial={{ opacity: 0, y: 30 }}
                        animate={{ opacity: 1, y: 0 }}
                        transition={{ duration: 0.6 }}
                        className="flex flex-col md:flex-row items-end md:items-start gap-8"
                    >
                        {/* Poster Card (Hidden on mobile for space) */}
                        <div className="hidden md:block flex-shrink-0 relative group">
                            <div className="absolute -inset-1 bg-gradient-to-r from-gray-700/30 to-gray-500/30 rounded-lg blur-lg opacity-70" style={{ background: `linear-gradient(to right, ${currentProvider.color}40, transparent)` }} />
                            <div className="relative w-48 lg:w-64 aspect-[2/3] rounded-lg overflow-hidden shadow-2xl border border-white/10 bg-[#1f1f1f]">
                                {!!posterUrl(activeItem) && (
                                    <img
                                        src={posterUrl(activeItem)}
                                        alt={activeItem.title}
                                        className="w-full h-full object-cover"
                                    />
                                )}
                            </div>
                        </div>

                        {/* Info Section */}
                        <div className="flex-1 space-y-6">
                            {/* Provider Badge - Dynamic */}
                            {!!currentProvider.logo && (
                                <div className="flex items-center gap-2 mb-2">
                                    <div className="bg-white/10 backdrop-blur-md px-3 py-1 rounded-full border border-white/20 flex items-center gap-2">
                                        <img src={tmdbImage(currentProvider.logo, 'w92')} alt={currentProvider.name} className="w-5 h-5 object-contain" />
                                        <span className="text-xs font-bold tracking-wider uppercase text-white/90">
                                            Featured on {currentProvider.name}
                                        </span>
                                    </div>
                                </div>
                            )}

                            {/* Title / Logo */}
                            <div className="min-h-[80px] flex items-end">
                                {logoUrl ? (
                                    <img
                                        src={logoUrl}
                                        alt={activeItem.title}
                                        onError={() => setLogoUrl(null)}
                                        className="max-h-24 md:max-h-32 lg:max-h-40 w-auto object-contain drop-shadow-2xl"
                                    />
                                ) : (
                                    <h1 className="text-4xl md:text-6xl lg:text-7xl font-bold leading-tight drop-shadow-lg">
                                        {activeItem.title}
                                    </h1>
                                )}
                            </div>

                            {/* Metadata Row */}
                            <div className="flex items-center flex-wrap gap-4 text-sm md:text-base font-medium text-gray-200">
                                {!!activeItem.rating && (
                                    <span className="flex items-center gap-1 text-green-400">
                                        <Star className="w-4 h-4 fill-current" />
                                        {Math.round(activeItem.rating * 10)}% Match
                                    </span>
                                )}
                                {!!activeItem.year && (
                                    <>
                                        <span className="text-gray-400">•</span>
                                        <span>{activeItem.year}</span>
                                    </>
                                )}
                                <span className="text-gray-400">•</span>
                                <span className="border border-white/30 px-1.5 rounded text-xs bg-white/5">HD</span>
                                {activeItem.mediaType === 'tv' && (
                                    <>
                                        <span className="text-gray-400">•</span>
                                        <span>Series</span>
                                    </>
                                )}
                            </div>

                            {/* Overview */}
                            <p className="text-gray-300 text-base md:text-lg line-clamp-3 max-w-2xl drop-shadow-md leading-relaxed">
                                {activeItem.overview}
                            </p>

                            {/* Action Buttons */}
                            <div className="flex flex-wrap items-center gap-4 pt-2">
                                <button
                                    onClick={() => router.push(activeItem.playHref || activeItem.href)}
                                    className="flex items-center gap-2 px-8 py-3.5 bg-white text-black rounded-lg font-bold hover:bg-gray-200 transition-all transform hover:scale-105 active:scale-95 shadow-[0_0_20px_rgba(255,255,255,0.3)]"
                                >
                                    <Play className="w-6 h-6 fill-black" />
                                    Play Now
                                </button>

                                <button
                                    onClick={() => router.push(activeItem.href)}
                                    className="flex items-center gap-2 px-8 py-3.5 bg-white/10 backdrop-blur-md text-white rounded-lg font-bold hover:bg-white/20 border border-white/10 transition-all"
                                >
                                    <Info className="w-6 h-6" />
                                    More Info
                                </button>

                                <button
                                    onClick={() => toggle(activeItem)}
                                    title={inList ? 'Remove from My List' : 'Add to My List'}
                                    className="p-3.5 rounded-full border border-white/30 bg-black/40 hover:bg-white/10 hover:border-white transition-all ml-2 backdrop-blur-md"
                                >
                                    {inList ? <Check className="w-6 h-6 text-green-400" /> : <Plus className="w-6 h-6" />}
                                </button>

                                {showTrailer && isPlaying && isVideoLoaded && (
                                    <button
                                        onClick={toggleMute}
                                        className="p-3.5 rounded-full border border-white/30 bg-black/40 hover:bg-white/10 hover:border-white transition-all ml-2 backdrop-blur-md"
                                    >
                                        {isMuted ? <VolumeX className="w-6 h-6" /> : <Volume2 className="w-6 h-6" />}
                                    </button>
                                )}
                            </div>
                        </div>
                    </motion.div>
                </div>
            </div>
        </div>
    );
};

export default ProviderHero;
