"use client";

import React, { useEffect, useMemo, useState, useCallback } from 'react';
import { useParams, useRouter } from 'next/navigation';
import { ArrowLeft } from 'lucide-react';
import { apiCall } from '@/shared/lib/api';
import RedLoader from '@/shared/ui/RedLoader';
import Navbar from '@/features/shell/Navbar';
import ProviderHero, { HeroItem } from '@/features/providers/components/ProviderHero';
import ProviderContentRow from '@/features/providers/components/ProviderContentRow';
import { NfTitle, fromTmdb, fromTmdbList } from '@/features/catalog/titles';

interface ProviderData {
    provider: {
        id: number;
        name: string;
        logo_url: string;
        primary_color: string;
        secondary_color: string;
    };
    movies: any[];
    tv_shows: any[];
    trending?: any[];
    popular?: any[];
    new_releases?: any[];
    regions: string[];
}

export default function ProviderDetailPage() {
    const params = useParams();
    const router = useRouter();
    const [data, setData] = useState<ProviderData | null>(null);
    const [loading, setLoading] = useState(true);
    const [error, setError] = useState('');
    const [activeTab, setActiveTab] = useState<'all' | 'movies' | 'tv'>('all');

    const fetchProviderData = useCallback(async () => {
        try {
            const providerId = params?.id;
            if (!providerId) return;

            const response = await apiCall(`/api/providers/${providerId}/content`);
            if (response) {
                setData(prev => ({
                    ...response,
                    trending: prev?.trending || [],
                    popular: prev?.popular || [],
                    new_releases: prev?.new_releases || []
                }));

                // Extra rows load in the background
                try {
                    const [trending, popular, newReleases] = await Promise.all([
                        apiCall(`/api/providers/${providerId}/trending`),
                        apiCall(`/api/providers/${providerId}/popular`),
                        apiCall(`/api/providers/${providerId}/new`)
                    ]);

                    setData(prev => prev ? {
                        ...prev,
                        trending: trending?.results || [],
                        popular: popular?.results || [],
                        new_releases: newReleases?.results || []
                    } : null);
                } catch (secondaryErr) {
                    console.warn('Failed to fetch secondary provider data', secondaryErr);
                }
            }
        } catch (err) {
            console.error('Failed to fetch provider data:', err);
            setError('Failed to load provider content');
        } finally {
            setLoading(false);
        }
    }, [params?.id]);

    useEffect(() => {
        if (params?.id) fetchProviderData();
    }, [params?.id, fetchProviderData]);

    const handlePlay = (item: NfTitle) => router.push(item.playHref || item.href);
    const handleInfo = (item: NfTitle) => router.push(item.href);

    const rows = useMemo(() => ({
        movies: fromTmdbList(data?.movies, 'movie'),
        tv: fromTmdbList(data?.tv_shows, 'tv'),
        trending: fromTmdbList(data?.trending, 'movie'),
        newReleases: fromTmdbList(data?.new_releases, 'movie'),
    }), [data]);

    // Hero from the first movies/TV shows so it does not jump when the extra rows arrive
    const heroItems = useMemo(() => {
        const out: HeroItem[] = [];
        const add = (raw: any, type: 'movie' | 'tv') => {
            const t = fromTmdb(raw, type);
            if (t) out.push({ ...t, trailerUrl: raw.trailer_url || undefined });
        };
        (data?.movies || []).forEach(m => add(m, 'movie'));
        (data?.tv_shows || []).forEach(t => add(t, 'tv'));
        return out.slice(0, 8);
    }, [data?.movies, data?.tv_shows]);

    if (loading && !data) {
        return (
            <div className="min-h-screen bg-[#141414] flex items-center justify-center">
                <RedLoader size="large" />
            </div>
        );
    }

    if (error || !data) {
        return (
            <div className="min-h-screen bg-[#141414] flex items-center justify-center text-white">
                <div className="text-center">
                    <h1 className="text-2xl font-bold mb-4">Provider Not Found</h1>
                    <button
                        onClick={() => router.push('/providers')}
                        className="bg-white text-black px-6 py-2 rounded-full font-bold hover:bg-gray-200"
                    >
                        Back to Providers
                    </button>
                </div>
            </div>
        );
    }

    const { provider } = data;
    const brandColor = provider.primary_color || '#ffffff';
    const showMovies = activeTab === 'all' || activeTab === 'movies';
    const showTv = activeTab === 'all' || activeTab === 'tv';
    const rowProps = { onPlay: handlePlay, onInfo: handleInfo, accentColor: brandColor };

    return (
        <div className="min-h-screen bg-[#141414] text-white">
            <Navbar />

            {/* Back Button Overlay */}
            <div className="absolute top-24 left-4 md:left-12 z-50">
                <button
                    onClick={() => router.push('/providers')}
                    className="flex items-center gap-2 text-white/80 hover:text-white bg-black/40 hover:bg-black/60 backdrop-blur-md px-4 py-2 rounded-full border border-white/10 transition-all group"
                >
                    <ArrowLeft className="w-5 h-5 group-hover:-translate-x-1 transition-transform" />
                    <span className="font-medium text-sm">Back to Providers</span>
                </button>
            </div>

            <ProviderHero
                providerName={provider.name}
                providerLogo={provider.logo_url}
                brandColor={brandColor}
                items={heroItems}
            />

            <div className="relative z-20 pt-8 pb-12 space-y-8">

                {/* Filter Tabs */}
                <div className="flex justify-center mb-8 sticky top-20 z-40">
                    <div className="bg-black/80 backdrop-blur-md p-1 rounded-full border border-white/10 flex gap-2">
                        {(['all', 'movies', 'tv'] as const).map((tab) => (
                            <button
                                key={tab}
                                onClick={() => setActiveTab(tab)}
                                className={`px-6 py-2 rounded-full font-medium transition-all ${activeTab === tab
                                    ? 'bg-white text-black shadow-lg scale-105'
                                    : 'text-gray-400 hover:text-white hover:bg-white/10'
                                    }`}
                                style={activeTab === tab ? { backgroundColor: brandColor, color: '#fff' } : {}}
                            >
                                {tab === 'all' ? 'All Content' : tab === 'tv' ? 'TV Shows' : 'Movies'}
                            </button>
                        ))}
                    </div>
                </div>

                {/* Content Rows (empty rows render nothing) */}
                {showMovies && <ProviderContentRow title="New Releases" items={rows.newReleases} priority {...rowProps} />}
                <ProviderContentRow title={`Trending on ${provider.name}`} items={rows.trending} {...rowProps} />
                {showMovies && <ProviderContentRow title="Popular Movies" items={rows.movies} {...rowProps} />}
                {showTv && <ProviderContentRow title="TV Shows" items={rows.tv} {...rowProps} />}
            </div>
        </div>
    );
}
