"use client";

import React, { useEffect, useMemo, useState } from 'react';
import { useRouter } from 'next/navigation';
import { ChevronRight } from 'lucide-react';
import { apiCall } from '@/shared/lib/api';
import { tmdbImage } from '@/shared/lib/image';
import RedLoader from '@/shared/ui/RedLoader';
import Navbar from '@/features/shell/Navbar';
import ProviderContentRow from '@/features/providers/components/ProviderContentRow';
import ProviderHero, { HeroItem } from '@/features/providers/components/ProviderHero';
import { NfTitle, fromTmdb, fromTmdbList } from '@/features/catalog/titles';

interface ShowcaseItem {
    provider: {
        provider_id: number;
        provider_name: string;
        logo_path: string; // Full URL
        primary_color: string;
        secondary_color: string;
    };
    content: any[];
}

export default function ProvidersPage() {
    const [showcaseData, setShowcaseData] = useState<ShowcaseItem[]>([]);
    const [loading, setLoading] = useState(true);
    const router = useRouter();

    useEffect(() => {
        (async () => {
            try {
                const response = await apiCall('/api/providers/showcase');
                if (response && response.showcase) {
                    setShowcaseData(response.showcase);
                }
            } catch (error) {
                console.error('Failed to fetch provider showcase:', error);
            } finally {
                setLoading(false);
            }
        })();
    }, []);

    const rows = useMemo(
        () => showcaseData.map(p => ({ ...p, items: fromTmdbList(p.content) })),
        [showcaseData]
    );

    // Mixed hero: the top 2 titles of every provider, shuffled
    const heroItems = useMemo(() => {
        const all: HeroItem[] = [];
        showcaseData.forEach(p => {
            (p.content || []).slice(0, 2).forEach(raw => {
                const t = fromTmdb(raw);
                if (!t) return;
                all.push({
                    ...t,
                    trailerUrl: raw.trailer_url || undefined,
                    provider: {
                        name: p.provider.provider_name,
                        logo: p.provider.logo_path,
                        color: p.provider.primary_color,
                    },
                });
            });
        });
        return all.sort(() => 0.5 - Math.random()).slice(0, 15);
    }, [showcaseData]);

    const handlePlay = (item: NfTitle) => router.push(item.playHref || item.href);
    const handleInfo = (item: NfTitle) => router.push(item.href);

    if (loading) {
        return (
            <div className="min-h-screen bg-[#141414] text-white flex items-center justify-center">
                <RedLoader size="large" />
            </div>
        );
    }

    return (
        <div className="min-h-screen bg-[#141414] text-white overflow-x-hidden">
            <Navbar />

            {/* Main Hero Section (Global Mixed Showcase) */}
            {heroItems.length > 0 && <ProviderHero items={heroItems} />}

            <div className="relative z-10 pb-20 pt-8">
                {rows.map((item, index) => {
                    const brandColor = item.provider.primary_color || '#fff';

                    return (
                        <div key={item.provider.provider_id} className="mb-8 group/provider">
                            {/* Provider Header */}
                            <div
                                className="px-4 md:px-12 flex items-center justify-between mb-4 sticky top-[72px] z-30 py-4 transition-all duration-300 backdrop-blur-md bg-gradient-to-r from-[#141414] via-[#141414]/90 to-transparent border-b border-white/5"
                                style={{ borderLeft: `4px solid ${brandColor}` }}
                            >
                                <div className="flex items-center gap-4 cursor-pointer" onClick={() => router.push(`/providers/${item.provider.provider_id}`)}>
                                    <div className="relative w-10 h-10 md:w-12 md:h-12 bg-white/10 rounded-lg p-2 border border-white/10 overflow-hidden group-hover/provider:scale-110 transition-transform">
                                        <img
                                            src={tmdbImage(item.provider.logo_path, 'w92')}
                                            alt={item.provider.provider_name}
                                            className="w-full h-full object-contain"
                                        />
                                        <div className="absolute inset-0 bg-white/20 opacity-0 group-hover/provider:opacity-100 transition-opacity" />
                                    </div>
                                    <div>
                                        <h2 className="text-2xl md:text-3xl font-bold leading-none tracking-tight group-hover/provider:text-white/90 transition-colors">
                                            {item.provider.provider_name}
                                        </h2>
                                        <p className="text-xs text-gray-400 font-medium uppercase tracking-wider mt-1 flex items-center gap-1">
                                            Trending Content <ChevronRight className="w-3 h-3" />
                                        </p>
                                    </div>
                                </div>

                                <button
                                    onClick={() => router.push(`/providers/${item.provider.provider_id}`)}
                                    className="hidden md:flex items-center gap-2 px-6 py-2 rounded-full bg-white/5 hover:bg-white/10 border border-white/10 transition-all hover:scale-105 active:scale-95 text-sm font-bold"
                                    style={{ color: brandColor }}
                                >
                                    Explore Full Library
                                </button>
                            </div>

                            {/* Content Grid/Scroll */}
                            <ProviderContentRow
                                title="" // Title handled by header above
                                items={item.items}
                                onPlay={handlePlay}
                                onInfo={handleInfo}
                                priority={index < 2}
                                accentColor={brandColor}
                            />
                        </div>
                    );
                })}
            </div>
        </div>
    );
}
