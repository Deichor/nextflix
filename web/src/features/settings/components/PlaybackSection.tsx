"use client";

import { useMemo } from 'react';
import { intlLocale, useLocale, useT, type Locale } from '@/shared/i18n';
import type { Profile } from '@/shared/lib/profile';
import {
  AUDIO_ORIGINAL,
  SUBTITLE_SIZE_SCALE,
  usePlayerPrefs,
  type SubtitleSize,
} from '@/features/settings/playerPrefs';
import { Segmented, SettingRow, SettingsSection, Toggle, type SegmentOption } from '@/features/settings/components/ui';

const baseLang = (code?: string) => (code || '').trim().toLowerCase().split(/[-_]/)[0];

function languageName(locale: Locale, code: string): string {
  try {
    const name = new Intl.DisplayNames([intlLocale(locale)], { type: 'language' }).of(code) || code;
    return name.charAt(0).toLocaleUpperCase(intlLocale(locale)) + name.slice(1);
  } catch {
    return code.toUpperCase();
  }
}

// A language the player remembered that is not one of the fixed choices is shown as an extra option
function withCurrent(options: SegmentOption<string>[], value: string, locale: Locale): SegmentOption<string>[] {
  return options.some((o) => o.value === value) ? options : [...options, { value, label: languageName(locale, value) }];
}

function SubtitlePreview({ size }: { size: SubtitleSize }) {
  const t = useT();
  return (
    <div
      className="relative aspect-video w-full max-w-md overflow-hidden rounded-md border border-white/10 bg-[radial-gradient(ellipse_at_30%_20%,#3a4a5c_0%,#1c2430_45%,#0b0d10_100%)]"
      style={{ containerType: 'inline-size' }}
      role="img"
      aria-label={t('settings.subtitlePreviewLabel')}
    >
      <p
        className="absolute inset-x-[6%] bottom-[9%] text-center font-semibold leading-[1.25] text-white"
        style={{
          // Same look as the player's subtitle overlay, scaled to the preview frame
          fontSize: `calc(${SUBTITLE_SIZE_SCALE[size]} * 4.6cqw)`,
          textShadow: '0 0 4px rgba(0,0,0,0.95), 0 0 2px rgba(0,0,0,0.95), 0 2px 4px rgba(0,0,0,0.8)',
        }}
      >
        {t('settings.subtitlePreview')}
      </p>
    </div>
  );
}

export default function PlaybackSection({ profile }: { profile: Profile | null }) {
  const t = useT();
  const locale = useLocale();
  const profileLang = profile?.language ?? locale;
  const [prefs, update] = usePlayerPrefs(profile?.id ?? null);

  // Without a saved choice show what the player does by default for this profile's language
  const audio = prefs.audioLang ? baseLang(prefs.audioLang) : profileLang === 'tr' ? 'tr' : AUDIO_ORIGINAL;
  const sub = prefs.sub === 'off' ? 'off' : prefs.sub ? baseLang(prefs.sub.lang) : profileLang === 'tr' ? 'tr' : 'off';
  const size: SubtitleSize = prefs.subSize ?? 'medium';
  const autoplay = prefs.autoplayNext !== false;

  const audioOptions = useMemo(
    () =>
      withCurrent(
        [
          { value: AUDIO_ORIGINAL, label: t('settings.audioOriginal') },
          { value: 'tr', label: t('language.tr') },
          { value: 'en', label: t('language.en') },
        ],
        audio,
        locale,
      ),
    [t, audio, locale],
  );

  const subOptions = useMemo(
    () =>
      withCurrent(
        [
          { value: 'off', label: t('settings.subtitlesOff') },
          { value: 'tr', label: t('language.tr') },
          { value: 'en', label: t('language.en') },
        ],
        sub,
        locale,
      ),
    [t, sub, locale],
  );

  const sizeOptions: SegmentOption<SubtitleSize>[] = [
    { value: 'small', label: t('settings.sizeSmall') },
    { value: 'medium', label: t('settings.sizeMedium') },
    { value: 'large', label: t('settings.sizeLarge') },
  ];

  return (
    <SettingsSection title={t('settings.playback')}>
      {profile && <p className="mb-5 text-sm text-[#8c8c8c]">{t('settings.playbackHint', { name: profile.name })}</p>}

      <SettingRow id="settings-audio" label={t('settings.audioLanguage')} hint={t('settings.audioLanguageHint')}>
        <Segmented labelledBy="settings-audio" value={audio} options={audioOptions} onChange={(v) => update({ audioLang: v })} />
      </SettingRow>

      <SettingRow id="settings-subs" label={t('settings.subtitlesDefault')} hint={t('settings.subtitlesDefaultHint')}>
        <Segmented
          labelledBy="settings-subs"
          value={sub}
          options={subOptions}
          onChange={(v) => update({ sub: v === 'off' ? 'off' : { lang: v } })}
        />
      </SettingRow>

      <div className="border-b border-white/10 py-5">
        <div className="flex flex-col gap-3 sm:flex-row sm:items-center sm:justify-between sm:gap-6">
          <div className="min-w-0">
            <p id="settings-sub-size" className="text-[15px] font-medium text-white">
              {t('settings.subtitleSize')}
            </p>
            <p className="mt-0.5 text-sm text-[#8c8c8c]">{t('settings.subtitleSizeHint')}</p>
          </div>
          <Segmented labelledBy="settings-sub-size" value={size} options={sizeOptions} onChange={(v) => update({ subSize: v })} />
        </div>
        <div className="mt-4">
          <SubtitlePreview size={size} />
        </div>
      </div>

      <SettingRow id="settings-autoplay" label={t('settings.autoplayNext')} hint={t('settings.autoplayNextHint')}>
        <Toggle labelledBy="settings-autoplay" checked={autoplay} onChange={(v) => update({ autoplayNext: v })} />
      </SettingRow>
    </SettingsSection>
  );
}
