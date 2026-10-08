"use client";

import React, { useState } from 'react';
import { Play, Plus, ChevronDown, Check } from 'lucide-react';
import { motion, AnimatePresence } from 'framer-motion';
import { matchPercent, NfTitle } from '@/features/catalog/titles';
import { useMyList } from '@/features/my-list/store';

interface ProviderMediaCardProps {
    item: NfTitle;
    onPlay: (item: NfTitle) => void;
    onInfo: (item: NfTitle) => void;
    priority?: boolean;
    delay?: number;
    accentColor?: string;
}

const ProviderMediaCard: React.FC<ProviderMediaCardProps> = ({
    item,
    onPlay,
    onInfo,
    priority = false,
    delay = 0,
    accentColor = '#ffffff',
}) => {
    const { has, toggle } = useMyList();
    const [isHovered, setIsHovered] = useState(false);
    const [imageError, setImageError] = useState(false);
    const inList = has(item);

    // Landscape card: backdrop first, poster as fallback
    const imageSrc = item.backdrop || item.poster;
    const match = matchPercent(item.rating);

    return (
        <motion.div
            initial={{ opacity: 0, y: 20 }}
            animate={{ opacity: 1, y: 0 }}
            transition={{ duration: 0.5, delay: delay / 1000 }}
            className="relative group cursor-pointer"
            onMouseEnter={() => setIsHovered(true)}
            onMouseLeave={() => setIsHovered(false)}
            onClick={() => onInfo(item)}
            style={{ zIndex: isHovered ? 50 : 10 }}
        >
            {/* Base Card */}
            <motion.div
                className="relative w-full aspect-video bg-[#141414] rounded-md overflow-hidden"
                animate={{
                    scale: isHovered ? 1.4 : 1,
                    y: isHovered ? -10 : 0,
                }}
                transition={{ duration: 0.3, ease: "easeInOut" }}
                style={{
                    transformOrigin: 'center center',
                    zIndex: isHovered ? 50 : 1,
                    position: 'relative',
                    boxShadow: isHovered ? `0 8px 30px ${accentColor}20` : '0 4px 6px -1px rgba(0, 0, 0, 0.1)',
                }}
            >
                {!imageError && imageSrc ? (
                    <img
                        src={imageSrc}
                        alt={item.title}
                        className="w-full h-full object-cover"
                        onError={() => setImageError(true)}
                        loading={priority ? "eager" : "lazy"}
                        style={{ objectPosition: item.backdrop ? 'center center' : 'center top' }}
                    />
                ) : (
                    <div className="w-full h-full bg-gradient-to-br from-[#2f2f2f] to-[#181818] flex items-center justify-center p-2">
                        <div className="text-white/60 text-xs font-medium text-center">{item.title}</div>
                    </div>
                )}

                {/* Gradient for the title */}
                <div className="absolute inset-x-0 bottom-0 h-1/2 bg-gradient-to-t from-black/90 via-black/40 to-transparent" />

                <div className={`absolute bottom-0 left-0 right-0 p-3 transition-opacity duration-200 ${isHovered ? 'opacity-0' : 'opacity-100'}`}>
                    <h3
                        className="text-white font-bold text-sm leading-tight line-clamp-1 drop-shadow-md"
                        style={{ textShadow: '0 2px 4px rgba(0,0,0,0.8), 0 0 8px rgba(0,0,0,0.6)' }}
                    >
                        {item.title}
                    </h3>
                </div>
            </motion.div>

            {/* Expanded info panel */}
            <AnimatePresence>
                {isHovered && (
                    <motion.div
                        initial={{ opacity: 0 }}
                        animate={{ opacity: 1 }}
                        exit={{ opacity: 0 }}
                        transition={{ duration: 0.2 }}
                        className="absolute top-full left-0 right-0 bg-[#141414] rounded-b-md shadow-2xl p-4 -mt-2"
                        style={{
                            zIndex: 49,
                            scale: 1.4,
                            transformOrigin: 'top center',
                            boxShadow: `0 20px 40px -10px ${accentColor}15`,
                            borderBottom: `2px solid ${accentColor}40`,
                        }}
                    >
                        <div className="flex items-center gap-3 mb-3">
                            <button
                                onClick={e => { e.stopPropagation(); onPlay(item); }}
                                title="Play"
                                className="bg-white text-black p-1.5 rounded-full hover:bg-gray-200 transition-transform active:scale-95"
                            >
                                <Play className="w-4 h-4 fill-black translate-x-0.5" />
                            </button>

                            <button
                                onClick={e => { e.stopPropagation(); toggle(item); }}
                                title={inList ? 'Remove from My List' : 'Add to My List'}
                                className="border-2 border-gray-500 text-gray-300 p-1.5 rounded-full hover:border-white hover:text-white transition-colors"
                            >
                                {inList ? <Check className="w-4 h-4" /> : <Plus className="w-4 h-4" />}
                            </button>

                            <button
                                onClick={e => { e.stopPropagation(); onInfo(item); }}
                                title="More Info"
                                className="border-2 border-gray-500 text-gray-300 p-1.5 rounded-full hover:border-white hover:text-white transition-colors ml-auto"
                            >
                                <ChevronDown className="w-4 h-4" />
                            </button>
                        </div>

                        <h3 className="text-white font-bold text-[11px] leading-tight mb-1 line-clamp-1" style={{ color: accentColor }}>
                            {item.title}
                        </h3>

                        <div className="flex items-center gap-2 text-[10px] font-medium text-gray-400 mb-2">
                            <span className="text-green-500">{match !== null ? `${match}% Match` : 'New'}</span>
                            {!!item.year && <span>{item.year}</span>}
                            <span>{item.mediaType === 'tv' ? 'Series' : 'Movie'}</span>
                            <span className="border border-gray-600 px-1 rounded text-[9px]">HD</span>
                        </div>

                        {!!item.genreNames && item.genreNames.length > 0 && (
                            <div className="flex flex-wrap gap-1.5">
                                {item.genreNames.slice(0, 3).map((genre, index) => (
                                    <span key={genre} className="text-[10px] text-white flex items-center">
                                        {index > 0 && <span className="w-1 h-1 bg-gray-600 rounded-full mr-1.5" />}
                                        {genre}
                                    </span>
                                ))}
                            </div>
                        )}
                    </motion.div>
                )}
            </AnimatePresence>
        </motion.div>
    );
};

export default ProviderMediaCard;
