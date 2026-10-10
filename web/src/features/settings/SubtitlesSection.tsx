"use client";

import { useEffect, useState, type FormEvent } from 'react';
import { ChevronDown, ChevronUp, Loader2 } from 'lucide-react';
import {
  getSubtitleSettings,
  saveSubtitleSettings,
  type SubtitleSettings,
  type SubtitleSettingsInput,
} from '@/features/settings/api';
import SecretField from '@/features/settings/components/SecretField';
import {
  inputClass,
  LoadingLine,
  primaryButton,
  SettingsCard,
  StatusBadge,
  StatusLine,
  type ConnectionState,
  type Status,
} from '@/features/settings/components/ui';
import { useT } from '@/shared/i18n';

// Language names are shown in their own language, like Netflix does.
const LANGUAGE_NAMES: Record<string, string> = { tr: 'Türkçe', en: 'English' };
const KNOWN_LANGS = ['tr', 'en'];

function orderLangs(langs: string[]): string[] {
  const out = langs.filter((l, i) => langs.indexOf(l) === i);
  for (const l of KNOWN_LANGS) if (!out.includes(l)) out.push(l);
  return out;
}

// Connected when at least one provider can be used
function providerState(s: SubtitleSettings | null, loadError: string | null): ConnectionState {
  if (loadError !== null) return 'error';
  if (!s) return 'checking';
  return s.opensubtitles.api_key_set || s.subdl.api_key_set || s.subsource.api_key_set || s.turkcealtyazi.enabled
    ? 'connected'
    : 'notSet';
}

export default function SubtitlesSection() {
  const t = useT();
  const [settings, setSettings] = useState<SubtitleSettings | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [osKey, setOsKey] = useState('');
  const [osUser, setOsUser] = useState('');
  const [osPass, setOsPass] = useState('');
  const [sdlKey, setSdlKey] = useState('');
  const [ssKey, setSsKey] = useState('');
  const [cleared, setCleared] = useState({ osKey: false, osPass: false, sdlKey: false, ssKey: false });
  const [taEnabled, setTaEnabled] = useState(true);
  const [langs, setLangs] = useState<string[]>(KNOWN_LANGS);
  const [saving, setSaving] = useState(false);
  const [status, setStatus] = useState<Status>(null);

  const reset = (s: SubtitleSettings) => {
    setSettings(s);
    setOsKey('');
    setOsPass('');
    setSdlKey('');
    setSsKey('');
    setOsUser(s.opensubtitles.username || '');
    setCleared({ osKey: false, osPass: false, sdlKey: false, ssKey: false });
    setTaEnabled(s.turkcealtyazi.enabled);
    setLangs(orderLangs(s.default_langs || []));
  };

  useEffect(() => {
    const ctrl = new AbortController();
    getSubtitleSettings(ctrl.signal)
      .then(reset)
      .catch((e: unknown) => {
        if ((e as Error)?.name !== 'AbortError') setLoadError((e as Error)?.message || '');
      });
    return () => ctrl.abort();
  }, []);

  const savedLangs = settings ? orderLangs(settings.default_langs || []) : [];
  const dirty =
    !!settings &&
    (osKey.trim() !== '' ||
      osPass !== '' ||
      sdlKey.trim() !== '' ||
      ssKey.trim() !== '' ||
      cleared.osKey ||
      cleared.osPass ||
      cleared.sdlKey ||
      cleared.ssKey ||
      osUser.trim() !== (settings.opensubtitles.username || '') ||
      taEnabled !== settings.turkcealtyazi.enabled ||
      langs.join(',') !== savedLangs.join(','));

  const move = (i: number, dir: -1 | 1) => {
    const j = i + dir;
    if (j < 0 || j >= langs.length) return;
    const next = [...langs];
    [next[i], next[j]] = [next[j], next[i]];
    setLangs(next);
  };

  const save = async (e: FormEvent) => {
    e.preventDefault();
    if (!settings) return;
    const user = osUser.trim();
    const input: SubtitleSettingsInput = {
      opensubtitles: {
        api_key: cleared.osKey ? null : osKey.trim(),
        username: user === '' && settings.opensubtitles.username ? null : user,
        password: cleared.osPass ? null : osPass,
      },
      subdl: { api_key: cleared.sdlKey ? null : sdlKey.trim() },
      subsource: { api_key: cleared.ssKey ? null : ssKey.trim() },
      turkcealtyazi: { enabled: taEnabled },
      default_langs: langs,
    };
    setSaving(true);
    setStatus(null);
    try {
      reset(await saveSubtitleSettings(input));
      setStatus({ kind: 'ok', text: t('settings.saved') });
    } catch (err) {
      setStatus({ kind: 'error', text: (err as Error)?.message || t('settings.couldNotSave') });
    } finally {
      setSaving(false);
    }
  };

  return (
    <SettingsCard
      title={t('settings.subtitleProviders')}
      summary={t('settings.subtitleProvidersSummary')}
      badge={<StatusBadge state={providerState(settings, loadError)} />}
    >
      {loadError !== null ? (
        <p className="text-sm text-[#e87c03]">
          {t('settings.subtitles.couldNotLoad', { error: loadError || t('settings.couldNotLoad') })}
        </p>
      ) : !settings ? (
        <LoadingLine />
      ) : (
        <form onSubmit={save} className="space-y-7">
          <p className="text-xs text-[#8c8c8c]">{t('settings.subtitles.hint')}</p>
          <div>
            <h3 className="text-sm font-semibold">{t('settings.subtitles.languages')}</h3>
            <p className="mb-2 text-xs text-[#8c8c8c]">{t('settings.subtitles.languagesHint')}</p>
            <ol className="space-y-1.5">
              {langs.map((l, i) => (
                <li key={l} className="flex items-center gap-3 rounded bg-[#333] px-3 py-2 text-sm">
                  <span className="w-5 text-[#8c8c8c]">{i + 1}.</span>
                  <span className="flex-1">{LANGUAGE_NAMES[l] || l.toUpperCase()}</span>
                  <button
                    type="button"
                    onClick={() => move(i, -1)}
                    disabled={i === 0}
                    aria-label={t('settings.subtitles.moveUp')}
                    className="rounded p-1 text-[#b3b3b3] hover:bg-white/10 hover:text-white disabled:opacity-30"
                  >
                    <ChevronUp className="h-4 w-4" />
                  </button>
                  <button
                    type="button"
                    onClick={() => move(i, 1)}
                    disabled={i === langs.length - 1}
                    aria-label={t('settings.subtitles.moveDown')}
                    className="rounded p-1 text-[#b3b3b3] hover:bg-white/10 hover:text-white disabled:opacity-30"
                  >
                    <ChevronDown className="h-4 w-4" />
                  </button>
                </li>
              ))}
            </ol>
          </div>

          <div className="border-t border-white/10 pt-6">
            <fieldset className="space-y-3">
              <legend className="text-sm font-semibold">OpenSubtitles.com</legend>
              <p className="text-xs text-[#8c8c8c]">{t('settings.subtitles.opensubtitlesHint')}</p>
              <SecretField
                label={t('settings.subtitles.apiKey')}
                isSet={settings.opensubtitles.api_key_set}
                value={osKey}
                onChange={setOsKey}
                cleared={cleared.osKey}
                onClear={(v) => setCleared((c) => ({ ...c, osKey: v }))}
              />
              <label className="block">
                <span className="mb-1.5 block text-sm text-[#b3b3b3]">{t('settings.subtitles.username')}</span>
                <input
                  type="text"
                  value={osUser}
                  onChange={(e) => setOsUser(e.target.value)}
                  autoComplete="off"
                  spellCheck={false}
                  className={inputClass}
                />
              </label>
              <SecretField
                label={t('settings.subtitles.password')}
                isSet={settings.opensubtitles.password_set}
                value={osPass}
                onChange={setOsPass}
                cleared={cleared.osPass}
                onClear={(v) => setCleared((c) => ({ ...c, osPass: v }))}
              />
            </fieldset>
          </div>

          <div className="border-t border-white/10 pt-6">
            <fieldset className="space-y-3">
              <legend className="text-sm font-semibold">SubDL</legend>
              <p className="text-xs text-[#8c8c8c]">{t('settings.subtitles.subdlHint')}</p>
              <SecretField
                label={t('settings.subtitles.apiKey')}
                isSet={settings.subdl.api_key_set}
                value={sdlKey}
                onChange={setSdlKey}
                cleared={cleared.sdlKey}
                onClear={(v) => setCleared((c) => ({ ...c, sdlKey: v }))}
              />
            </fieldset>
          </div>

          <div className="border-t border-white/10 pt-6">
            <fieldset className="space-y-3">
              <legend className="text-sm font-semibold">SubSource</legend>
              <p className="text-xs text-[#8c8c8c]">{t('settings.subtitles.subsourceHint')}</p>
              <SecretField
                label={t('settings.subtitles.apiKey')}
                isSet={settings.subsource.api_key_set}
                value={ssKey}
                onChange={setSsKey}
                cleared={cleared.ssKey}
                onClear={(v) => setCleared((c) => ({ ...c, ssKey: v }))}
              />
            </fieldset>
          </div>

          <div className="border-t border-white/10 pt-6">
            <fieldset className="space-y-2">
              <legend className="text-sm font-semibold">turkcealtyazi.org</legend>
              <p className="text-xs text-[#8c8c8c]">{t('settings.subtitles.turkcealtyaziHint')}</p>
              <label className="flex cursor-pointer items-center gap-3 text-sm">
                <input
                  type="checkbox"
                  checked={taEnabled}
                  onChange={(e) => setTaEnabled(e.target.checked)}
                  className="h-4 w-4 accent-[#E50914]"
                />
                {t('settings.subtitles.enabled')}
              </label>
            </fieldset>
          </div>

          <div className="space-y-4 border-t border-white/10 pt-6">
            <button type="submit" disabled={saving || !dirty} className={primaryButton}>
              {saving && <Loader2 className="h-4 w-4 animate-spin" />}
              {t('common.save')}
            </button>
            <StatusLine status={status} />
          </div>
        </form>
      )}
    </SettingsCard>
  );
}
