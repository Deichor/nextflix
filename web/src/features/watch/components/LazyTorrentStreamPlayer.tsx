'use client';

import dynamic from 'next/dynamic';

export type { TorrentStreamPlayerProps } from '@/features/watch/components/TorrentStreamPlayer';

// The player is big; pages load it only when it is about to be used
const TorrentStreamPlayer = dynamic(() => import('@/features/watch/components/TorrentStreamPlayer'), {
  ssr: false,
  loading: () => <div className="fixed inset-0 z-[100] bg-black" />,
});

/** Starts downloading the player code (e.g. on hover over Play). */
export const preloadPlayer = () => {
  import('@/features/watch/components/TorrentStreamPlayer').catch(() => { /* loads again on open */ });
};

export default TorrentStreamPlayer;
