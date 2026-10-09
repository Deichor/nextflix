"use client";

import Link from 'next/link';
import { ChevronLeft } from 'lucide-react';
import { usePageTitle } from '@/shared/hooks/usePageTitle';
import PageShell from '@/features/shell/PageShell';
import { useT } from '@/shared/i18n';
import IndexerCard from '@/features/settings/components/IndexerCard';
import DebridCard from '@/features/settings/components/DebridCard';
import SubtitlesSection from '@/features/settings/SubtitlesSection';

export default function AdvancedSettingsScreen() {
  const t = useT();
  usePageTitle(t('settings.advanced'));

  return (
    <PageShell>
      <div className="mx-auto max-w-3xl px-4 pt-24 md:px-12 md:pt-28">
        <Link
          href="/settings"
          className="mb-4 inline-flex items-center gap-1 text-sm font-medium text-[#b3b3b3] hover:text-white pointer-coarse:min-h-11"
        >
          <ChevronLeft className="h-4 w-4" aria-hidden />
          {t('settings.backToSettings')}
        </Link>
        <h1 className="text-3xl font-semibold md:text-4xl">{t('settings.advanced')}</h1>
        <p className="mb-8 mt-2 text-sm text-[#b3b3b3] md:text-base">{t('settings.advancedIntro')}</p>

        <div className="space-y-6">
          <IndexerCard />
          <DebridCard />
          <SubtitlesSection />
        </div>
      </div>
    </PageShell>
  );
}
