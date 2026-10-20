"use client";

import { useCallback, useEffect, useState, type FormEvent } from 'react';
import { Loader2, X } from 'lucide-react';
import {
  getSubtitleSettings,
  saveSubtitleSettings,
  testAITranslate,
  type AITranslateSettings,
} from '@/features/settings/api';
import SecretField from '@/features/settings/components/SecretField';
import {
  inputClass,
  LoadingLine,
  primaryButton,
  secondaryButton,
  SettingsCard,
  StatusBadge,
  StatusLine,
  Toggle,
  type Status,
} from '@/features/settings/components/ui';
import { intlLocale, useLocale, useT } from '@/shared/i18n';

const PROFILE = 'profile';

export default function AutoTranslateCard() {
  const t = useT();
  const locale = useLocale();
  const [settings, setSettings] = useState<AITranslateSettings | null>(null);
  const [apiKey, setApiKey] = useState('');
  const [keyCleared, setKeyCleared] = useState(false);
  const [enabled, setEnabled] = useState(false);
  const [langs, setLangs] = useState<string[]>([PROFILE]);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [status, setStatus] = useState<Status>(null);

  const reset = useCallback((s: AITranslateSettings) => {
    setSettings(s);
    setApiKey('');
    setKeyCleared(false);
    setEnabled(s.enabled);
    setLangs(s.langs);
  }, []);

  useEffect(() => {
    const ctrl = new AbortController();
    getSubtitleSettings(ctrl.signal)
      .then((s) => reset(s.ai_translate))
      .catch((e: unknown) => {
        if ((e as Error)?.name === 'AbortError') return;
        setLoadError((e as Error)?.message || '');
      });
    return () => ctrl.abort();
  }, [reset]);

  const langName = (code: string) =>
    code === PROFILE ? t('settings.aiProfileLang') : settings?.languages.find((l) => l.code === code)?.native || code;

  const dirty =
    !!settings &&
    (apiKey.trim() !== '' || keyCleared || enabled !== settings.enabled || langs.join(',') !== settings.langs.join(','));

  const save = async (e: FormEvent) => {
    e.preventDefault();
    if (!settings) return;
    setSaving(true);
    setStatus(null);
    try {
      const saved = await saveSubtitleSettings({
        ai_translate: {
          enabled,
          langs,
          // Omitted keeps the stored key, null removes it
          ...(keyCleared ? { api_key: null } : apiKey.trim() ? { api_key: apiKey.trim() } : {}),
        },
      });
      reset(saved.ai_translate);
      setStatus({ kind: 'ok', text: t('settings.saved') });
    } catch (err) {
      setStatus({ kind: 'error', text: (err as Error)?.message || t('settings.couldNotSave') });
    } finally {
      setSaving(false);
    }
  };

  const test = async () => {
    setTesting(true);
    setStatus(null);
    try {
      // An empty key falls back to the stored one on the server
      const typed = keyCleared ? '' : apiKey.trim();
      const r = await testAITranslate(typed);
      setStatus({ kind: r.success ? 'ok' : 'error', text: r.success ? t('settings.aiKeyOk') : r.message });
      // A passing test of the stored key ends the pause on the server
      if (r.success && !typed) setSettings((s) => (s ? { ...s, error: undefined, error_kind: undefined, error_at: undefined } : s));
    } catch (err) {
      setStatus({ kind: 'error', text: (err as Error)?.message || t('settings.couldNotReach') });
    } finally {
      setTesting(false);
    }
  };

  const usage = settings?.usage;
  const errorReason =
    settings?.error_kind === 'credit'
      ? t('settings.aiErrorCredit')
      : settings?.error_kind === 'key'
        ? t('settings.aiErrorKey')
        : settings?.error || '';
  const errorTime = settings?.error_at
    ? new Date(settings.error_at).toLocaleTimeString(intlLocale(locale), { hour: '2-digit', minute: '2-digit' })
    : '';

  return (
    <SettingsCard
      title={t('settings.aiTitle')}
      summary={t('settings.aiSummary')}
      badge={settings && !settings.api_key_set ? <StatusBadge state="notSet" /> : undefined}
    >
      {loadError !== null ? (
        <p className="text-sm text-[#e87c03]">{loadError || t('settings.couldNotLoad')}</p>
      ) : !settings ? (
        <LoadingLine />
      ) : (
        <form onSubmit={save} className="space-y-5">
          <div className="flex items-center justify-between gap-6">
            <div className="min-w-0">
              <p id="ai-enabled-label" className="text-sm text-white">
                {t('settings.aiEnabled')}
              </p>
              <p className="mt-0.5 text-xs text-[#8c8c8c]">{t('settings.aiEnabledHint')}</p>
            </div>
            <Toggle checked={enabled} onChange={setEnabled} labelledBy="ai-enabled-label" />
          </div>

          <SecretField
            label={t('settings.apiKey')}
            hint={t('settings.aiKeyHint', { model: 'Claude Haiku 5.5' })}
            isSet={settings.api_key_set}
            value={apiKey}
            onChange={setApiKey}
            cleared={keyCleared}
            onClear={setKeyCleared}
          />

          <div>
            <p className="text-sm text-white">{t('settings.aiLangs')}</p>
            <p className="mt-0.5 text-xs text-[#8c8c8c]">{t('settings.aiLangsHint')}</p>
            <div className="mt-2 flex flex-wrap gap-2">
              {langs.map((code) => (
                <span key={code} className="inline-flex items-center gap-1 rounded-full bg-white/10 py-1 pl-3 pr-1 text-sm text-white">
                  {langName(code)}
                  {langs.length > 1 && (
                    <button
                      type="button"
                      onClick={() => setLangs(langs.filter((l) => l !== code))}
                      className="rounded-full p-1 text-[#b3b3b3] hover:bg-white/10 hover:text-white"
                      aria-label={t('settings.aiRemoveLang', { lang: langName(code) })}
                    >
                      <X className="h-3.5 w-3.5" />
                    </button>
                  )}
                </span>
              ))}
            </div>
            <select
              value=""
              onChange={(e) => e.target.value && setLangs([...langs, e.target.value])}
              className={`${inputClass} mt-2 sm:max-w-xs`}
            >
              <option value="">{t('settings.aiAddLang')}</option>
              {!langs.includes(PROFILE) && <option value={PROFILE}>{t('settings.aiProfileLang')}</option>}
              {settings.languages
                .filter((l) => !langs.includes(l.code))
                .map((l) => (
                  <option key={l.code} value={l.code}>
                    {l.native}
                  </option>
                ))}
            </select>
          </div>

          {usage && (
            <p className="text-xs text-[#8c8c8c]">
              {t('settings.aiUsage', { lines: usage.lines, cost: usage.cost_usd.toFixed(usage.cost_usd < 1 ? 4 : 2) })}
            </p>
          )}
          {settings.error && (
            <p className="text-sm text-[#e87c03]">{t('settings.aiError', { time: errorTime, reason: errorReason })}</p>
          )}

          <div className="flex flex-wrap gap-3 pt-1">
            <button type="submit" disabled={saving || !dirty} className={primaryButton}>
              {saving && <Loader2 className="h-4 w-4 animate-spin" />}
              {t('common.save')}
            </button>
            <button
              type="button"
              onClick={test}
              disabled={testing || (!settings.api_key_set && !apiKey.trim()) || (keyCleared && !apiKey.trim())}
              className={secondaryButton}
            >
              {testing && <Loader2 className="h-4 w-4 animate-spin" />}
              {t('settings.testConnection')}
            </button>
          </div>
          <StatusLine status={status} />
        </form>
      )}
    </SettingsCard>
  );
}
