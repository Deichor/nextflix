// Mirrors server/internal/modules/notifications/model.go (JSON field names must match).
export type NotificationType =
    | 'tmdb_upcoming'
    | 'tmdb_now_playing'
    | 'tmdb_trending'
    | 'tmdb_upcoming_tv'
    | 'tmdb_now_airing_tv'
    | 'tmdb_coming_soon';

export interface NotificationMedia {
    id: number;
    title: string;
    poster_url: string;
    backdrop_url: string;
    rating: number;
    year: number;
    runtime: number;
    genres: string[] | null;
    overview: string;
    source_type: string;
    source_id: string;
}

export interface Notification {
    id: string;
    type: NotificationType;
    title: string;
    message: string;
    tmdb_ids?: number[];
    tmdb_titles?: string[];
    timestamp: number;
    /** Per profile (X-User-ID), filled in by the server */
    read: boolean;

    backdrop_url?: string;
    poster_url?: string;
    logo_url?: string;
    trailer_key?: string;
    rating?: number;
    release_date?: string;
    runtime?: number;
    genres?: string[];
    overview?: string;
    popularity?: number;
    priority?: 'high' | 'medium' | 'low';
    category?: string;
    media_details?: NotificationMedia[];
    days_until?: number;
}

export interface NotificationCounts {
    /** Badge count: newer than the profile's "seen" mark and not read */
    unseen: number;
    unread: number;
    total: number;
}

export interface NotificationResponse extends NotificationCounts {
    notifications: Notification[];
    count: number;
}

export interface NotificationCountResponse extends NotificationCounts {
    count: number;
}
