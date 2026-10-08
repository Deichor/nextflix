"use client";

import React from "react";
import { motion, AnimatePresence } from "framer-motion";
import { X, Film, Clock } from "lucide-react";
import { Notification } from "@/features/notifications/types";
import { useNavigate } from "@/shared/navigation/useNavigate";
import { notificationHref } from "@/features/notifications/links";
import { tmdbImage } from "@/shared/lib/image";

interface NotificationDropdownProps {
    isOpen: boolean;
    onClose: () => void;
    notifications: Notification[];
    onNotificationClick: (movieId: number) => void;
}

const NotificationDropdown: React.FC<NotificationDropdownProps> = ({
    isOpen,
    onClose,
    notifications,
    onNotificationClick,
}) => {
    const navigate = useNavigate();
    const formatTimestamp = (timestamp: number) => {
        const now = Date.now() / 1000;
        const diff = now - timestamp;

        if (diff < 60) return "Just now";
        if (diff < 3600) return `${Math.floor(diff / 60)}m ago`;
        if (diff < 86400) return `${Math.floor(diff / 3600)}h ago`;
        if (diff < 604800) return `${Math.floor(diff / 86400)}d ago`;
        return new Date(timestamp * 1000).toLocaleDateString();
    };

    const handleNotificationClick = (notification: Notification, index = 0) => {
        const href = notificationHref(notification, index);
        if (href) {
            const id = notification.tmdb_ids?.[index] ?? notification.media_details?.[index]?.id ?? 0;
            onNotificationClick(id);
            navigate.push(href);
        }
        onClose();
    };

    return (
        <AnimatePresence>
            {isOpen && (
                <motion.div
                    initial={{ opacity: 0, y: -10 }}
                    animate={{ opacity: 1, y: 0 }}
                    exit={{ opacity: 0, y: -10 }}
                    className="absolute top-full right-0 mt-2 w-96 bg-black/95 backdrop-blur-sm border border-white/20 rounded-lg shadow-2xl z-50 max-h-[32rem] overflow-hidden flex flex-col"
                >
                    {/* Header */}
                    <div className="flex items-center justify-between p-4 border-b border-white/10">
                        <h3 className="text-white font-semibold text-lg">Notifications</h3>
                        <div className="flex items-center gap-2">
                            <button
                                onClick={() => {
                                    navigate.push('/notifications');
                                    onClose();
                                }}
                                className="text-red-400 hover:text-red-300 transition-colors text-sm font-medium"
                            >
                                View All
                            </button>
                            <button
                                onClick={onClose}
                                className="text-white/60 hover:text-white transition-colors"
                            >
                                <X className="w-5 h-5" />
                            </button>
                        </div>
                    </div>

                    {/* Notifications List */}
                    <div className="flex-1 overflow-y-auto">
                        {notifications.length === 0 ? (
                            <div className="p-8 text-center">
                                <div className="w-16 h-16 bg-white/5 rounded-full flex items-center justify-center mx-auto mb-4">
                                    <Film className="w-8 h-8 text-white/40" />
                                </div>
                                <p className="text-white/60">No notifications yet</p>
                                <p className="text-white/40 text-sm mt-1 mb-4">
                                    Check back later for movie recommendations
                                </p>
                                <button
                                    onClick={() => {
                                        navigate.push('/notifications');
                                        onClose();
                                    }}
                                    className="text-red-400 hover:text-red-300 transition-colors text-sm font-medium"
                                >
                                    View Live Activity →
                                </button>
                            </div>
                        ) : (
                            <div className="py-2">
                                {notifications.map((notification) => (
                                    <div
                                        key={notification.id}
                                        className="px-4 py-3 hover:bg-white/5 transition-colors border-b border-white/5 last:border-0"
                                    >
                                        <div className="flex items-start gap-3">
                                            <div className="flex-shrink-0">
                                                <div className="w-10 h-10 bg-red-600/20 rounded-full flex items-center justify-center">
                                                    <Film className="w-5 h-5 text-red-500" />
                                                </div>
                                            </div>
                                            <div className="flex-1 min-w-0">
                                                <h4 className="text-white font-medium text-sm mb-1">
                                                    {notification.title}
                                                </h4>
                                                <p className="text-white/70 text-sm mb-2">
                                                    {notification.message}
                                                </p>

                                                {/* Poster strip for multi-title notifications */}
                                                {(notification.media_details?.length ?? 0) > 1 && (
                                                    <div className="flex gap-2 flex-wrap pb-2">
                                                        {notification.media_details!.slice(0, 4).map((media, idx) => {
                                                            const poster = tmdbImage(media.poster_url, 'w185');
                                                            return (
                                                                <button
                                                                    key={`${media.id}-${idx}`}
                                                                    onClick={() => handleNotificationClick(notification, idx)}
                                                                    className="flex-shrink-0 group relative"
                                                                    title={media.title}
                                                                >
                                                                    {poster ? (
                                                                        <img
                                                                            src={poster}
                                                                            alt={media.title}
                                                                            className="w-16 h-24 object-cover rounded bg-gray-800 group-hover:ring-2 group-hover:ring-red-500 transition-all"
                                                                        />
                                                                    ) : (
                                                                        <div className="w-16 h-24 rounded bg-gray-800 flex items-center justify-center group-hover:ring-2 group-hover:ring-red-500">
                                                                            <Film className="w-6 h-6 text-white/40" />
                                                                        </div>
                                                                    )}
                                                                </button>
                                                            );
                                                        })}
                                                    </div>
                                                )}

                                                {/* TMDB Movie suggestions */}
                                                {notification.tmdb_ids &&
                                                    notification.tmdb_ids.length > 0 &&
                                                    notification.tmdb_titles &&
                                                    notification.tmdb_titles.length > 0 && (
                                                        <div className="flex flex-col gap-2">
                                                            <button
                                                                onClick={() => handleNotificationClick(notification)}
                                                                className="flex items-center gap-2 p-2 hover:bg-white/10 rounded transition-colors"
                                                            >
                                                                <div className="w-10 h-10 bg-red-600/20 rounded flex items-center justify-center flex-shrink-0">
                                                                    <Film className="w-5 h-5 text-red-500" />
                                                                </div>
                                                                <div className="flex-1 text-left">
                                                                    <p className="text-white font-medium text-sm">
                                                                        {notification.tmdb_titles[0]}
                                                                    </p>
                                                                    <p className="text-white/60 text-xs">
                                                                        View details
                                                                    </p>
                                                                </div>
                                                            </button>
                                                        </div>
                                                    )}

                                                {/* Timestamp */}
                                                <div className="flex items-center gap-1 text-white/40 text-xs mt-2">
                                                    <Clock className="w-3 h-3" />
                                                    <span>{formatTimestamp(notification.timestamp)}</span>
                                                </div>
                                            </div>
                                        </div>
                                    </div>
                                ))}
                                
                                {/* Footer with View All link */}
                                {notifications.length > 0 && (
                                    <div className="border-t border-white/10 p-4">
                                        <button
                                            onClick={() => {
                                                navigate.push('/notifications');
                                                onClose();
                                            }}
                                            className="w-full text-center text-red-400 hover:text-red-300 transition-colors text-sm font-medium py-2 hover:bg-white/5 rounded"
                                        >
                                            View All Notifications in Live Activity →
                                        </button>
                                    </div>
                                )}
                            </div>
                        )}
                    </div>
                </motion.div>
            )}
        </AnimatePresence>
    );
};

export default NotificationDropdown;
