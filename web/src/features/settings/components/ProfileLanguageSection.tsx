"use client";

import { useState } from 'react';
import Link from 'next/link';
import { ChevronRight, Loader2 } from 'lucide-react';
import ProfileAvatar from '@/features/profiles/components/ProfileAvatar';
import { updateProfile } from '@/features/profiles/api';
import {
  getActiveProfileId,
  setActiveProfile,
  type Profile,
  type ProfileLanguage,
} from '@/shared/lib/profile';
import { LOCALES, useLocale, useT } from '@/shared/i18n';
import { Segmented, SettingRow, SettingsSection, StatusLine, type Status } from '@/features/settings/components/ui';

export default function ProfileLanguageSection({ profile }: { profile: Profile | null }) {
  const t = useT();
  const locale = useLocale();
  const [saving, setSaving] = useState(false);
  const [status, setStatus] = useState<Status>(null);

  // Applied at once (the i18n provider follows the active profile), then saved; reverted if saving fails
  const changeLanguage = async (language: ProfileLanguage) => {
    if (!profile || saving) return;
    const previous = profile;
    setStatus(null);
    setSaving(true);
    setActiveProfile({ ...profile, language });
    try {
      const updated = await updateProfile(profile.id, { language });
      if (getActiveProfileId() === updated.id) setActiveProfile({ ...previous, ...updated });
    } catch (err) {
      if (getActiveProfileId() === previous.id) setActiveProfile(previous);
      setStatus({ kind: 'error', text: t('settings.languageSaveFailed', { error: (err as Error)?.message || '' }).trim() });
    } finally {
      setSaving(false);
    }
  };

  return (
    <SettingsSection title={t('settings.profileLanguage')}>
      {!profile ? (
        <div className="flex flex-wrap items-center justify-between gap-4">
          <p className="text-sm text-[#b3b3b3]">{t('settings.noProfile')}</p>
          <Link href="/profiles" className="rounded bg-white px-4 py-2 text-sm font-semibold text-black hover:bg-white/80">
            {t('settings.chooseProfile')}
          </Link>
        </div>
      ) : (
        <div>
          <div className="flex items-center gap-4 border-b border-white/10 pb-5">
            <ProfileAvatar profile={profile} size={56} />
            <div className="min-w-0 flex-1">
              <p className="truncate text-lg font-semibold">{profile.name}</p>
              <p className="text-sm text-[#8c8c8c]">{t(`language.${profile.language ?? locale}`)}</p>
            </div>
            <Link
              href="/profiles?manage=1"
              className="group inline-flex shrink-0 items-center gap-1 text-sm font-medium text-white hover:underline pointer-coarse:min-h-11"
            >
              {t('profiles.manageProfiles')}
              <ChevronRight className="h-4 w-4 text-[#8c8c8c] transition-transform group-hover:translate-x-0.5" />
            </Link>
          </div>

          <div className="pt-5">
            <SettingRow id="settings-language" label={t('settings.languageLabel')} hint={t('settings.languageHint')}>
              <div className="flex items-center gap-3">
                {saving && <Loader2 className="h-4 w-4 animate-spin text-[#8c8c8c]" aria-hidden />}
                <Segmented<ProfileLanguage>
                  labelledBy="settings-language"
                  value={profile.language ?? locale}
                  options={LOCALES.map((l) => ({ value: l, label: t(`language.${l}`) }))}
                  onChange={changeLanguage}
                  disabled={saving}
                />
              </div>
            </SettingRow>
          </div>
          {status && (
            <div className="mt-4">
              <StatusLine status={status} />
            </div>
          )}
        </div>
      )}
    </SettingsSection>
  );
}
