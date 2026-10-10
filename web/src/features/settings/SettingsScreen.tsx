"use client";

import { useSyncExternalStore } from 'react';
import Link from 'next/link';
import { ChevronRight, Settings2 } from 'lucide-react';
import { usePageTitle } from '@/shared/hooks/usePageTitle';
import PageShell from '@/features/shell/PageShell';
import { useActiveProfile } from '@/shared/lib/profile';
import { useT } from '@/shared/i18n';
import ProfileLanguageSection from '@/features/settings/components/ProfileLanguageSection';
import PlaybackSection from '@/features/settings/components/PlaybackSection';
import { LoadingLine } from '@/features/settings/components/ui';

// false on the server and while hydrating, true afterwards
const noSubscribe = () => () => {};
const onClient = () => true;
const onServer = () => false;

export default function SettingsScreen() {
  const t = useT();
  usePageTitle(t('settings.title'));
  const profile = useActiveProfile();
  // useActiveProfile is null while hydrating; wait so "no profile" does not flash
  const mounted = useSyncExternalStore(noSubscribe, onClient, onServer);

  return (
    <PageShell>
      <div className="mx-auto max-w-4xl px-4 pt-24 md:px-12 md:pt-28">
        <h1 className="text-3xl font-semibold md:text-4xl">{t('settings.title')}</h1>
        <p className="mb-8 mt-2 text-sm text-[#b3b3b3] md:text-base">{t('settings.intro')}</p>

        {!mounted ? (
          <div className="border-t border-white/15 py-8">
            <LoadingLine />
          </div>
        ) : (
          <>
            <ProfileLanguageSection profile={profile} />
            <PlaybackSection profile={profile} />
          </>
        )}

        <div className="border-t border-white/15 py-8">
          <Link
            href="/settings/advanced"
            className="group flex items-center gap-4 rounded-md border border-white/10 bg-[#181818] px-5 py-4 transition-colors hover:border-white/25 hover:bg-[#202020] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/70"
          >
            <span className="flex h-10 w-10 shrink-0 items-center justify-center rounded-full bg-white/10">
              <Settings2 className="h-5 w-5 text-white" aria-hidden />
            </span>
            <span className="min-w-0 flex-1">
              <span className="block text-[15px] font-medium text-white">{t('settings.advanced')}</span>
              <span className="block text-sm text-[#8c8c8c]">{t('settings.advancedHint')}</span>
            </span>
            <ChevronRight className="h-5 w-5 shrink-0 text-[#8c8c8c] transition-transform group-hover:translate-x-0.5 group-hover:text-white" />
          </Link>
        </div>
      </div>
    </PageShell>
  );
}
