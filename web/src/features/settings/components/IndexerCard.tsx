"use client";

import { useCallback, useEffect, useState, type FormEvent } from 'react';
import { Loader2 } from 'lucide-react';
import { getIndexerSettings, saveIndexerSettings, testIndexer, type IndexerSettings } from '@/features/settings/api';
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
  type ConnectionState,
  type Status,
} from '@/features/settings/components/ui';
import { useT } from '@/shared/i18n';

const configured = (s: IndexerSettings) => !!s.jackett_url && s.api_key_set;

export default function IndexerCard() {
  const t = useT();
  const [settings, setSettings] = useState<IndexerSettings | null>(null);
  const [url, setUrl] = useState('');
  const [apiKey, setApiKey] = useState('');
  const [keyCleared, setKeyCleared] = useState(false);
  const [bitsearch, setBitsearch] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [status, setStatus] = useState<Status>(null);
  const [connection, setConnection] = useState<ConnectionState>('checking');
  const [connectionError, setConnectionError] = useState('');

  // Checks the stored settings so the badge shows the real state
  const checkStored = useCallback((s: IndexerSettings) => {
    if (!configured(s)) {
      setConnection('notSet');
      return;
    }
    setConnection('checking');
    testIndexer({ jackett_url: '', jackett_api_key: '' })
      .then((r) => {
        setConnection(r.success ? 'connected' : 'error');
        setConnectionError(r.success ? '' : r.message);
      })
      .catch((e: unknown) => {
        setConnection('error');
        setConnectionError((e as Error)?.message || '');
      });
  }, []);

  const reset = useCallback((s: IndexerSettings) => {
    setSettings(s);
    setUrl(s.jackett_url);
    setApiKey('');
    setKeyCleared(false);
    setBitsearch(s.bitsearch_enabled);
  }, []);

  useEffect(() => {
    const ctrl = new AbortController();
    getIndexerSettings(ctrl.signal)
      .then((s) => {
        reset(s);
        checkStored(s);
      })
      .catch((e: unknown) => {
        if ((e as Error)?.name === 'AbortError') return;
        setLoadError((e as Error)?.message || '');
        setConnection('error');
      });
    return () => ctrl.abort();
  }, [reset, checkStored]);

  const dirty =
    !!settings &&
    (url.trim() !== settings.jackett_url || apiKey.trim() !== '' || keyCleared || bitsearch !== settings.bitsearch_enabled);

  const save = async (e: FormEvent) => {
    e.preventDefault();
    if (!settings) return;
    setSaving(true);
    setStatus(null);
    try {
      const saved = await saveIndexerSettings({
        jackett_url: url.trim(),
        bitsearch_enabled: bitsearch,
        // Omitted keeps the stored key, null removes it
        ...(keyCleared ? { jackett_api_key: null } : apiKey.trim() ? { jackett_api_key: apiKey.trim() } : {}),
      });
      reset(saved);
      checkStored(saved);
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
      const result = await testIndexer({ jackett_url: url.trim(), jackett_api_key: keyCleared ? '' : apiKey.trim() });
      setStatus({ kind: result.success ? 'ok' : 'error', text: result.message || t('settings.connected') });
      if (!dirty) {
        setConnection(result.success ? 'connected' : 'error');
        setConnectionError(result.success ? '' : result.message);
      }
    } catch (err) {
      setStatus({ kind: 'error', text: (err as Error)?.message || t('settings.couldNotReach') });
    } finally {
      setTesting(false);
    }
  };

  return (
    <SettingsCard
      title={t('settings.indexerTitle')}
      summary={t('settings.indexerSummary')}
      badge={<StatusBadge state={connection} title={connectionError || undefined} />}
    >
      {loadError !== null ? (
        <p className="text-sm text-[#e87c03]">
          {t('settings.couldNotLoadIndexer', { error: loadError || t('settings.couldNotLoad') })}
        </p>
      ) : !settings ? (
        <LoadingLine />
      ) : (
        <form onSubmit={save} className="space-y-5">
          <label className="block">
            <span className="mb-1.5 block text-sm text-[#b3b3b3]">{t('settings.jackettUrl')}</span>
            <input
              type="url"
              value={url}
              onChange={(e) => setUrl(e.target.value)}
              placeholder="http://jackett:9117"
              autoComplete="off"
              spellCheck={false}
              className={inputClass}
            />
          </label>

          <SecretField
            label={t('settings.apiKey')}
            hint={t('settings.indexerKeyHint')}
            isSet={settings.api_key_set}
            value={apiKey}
            onChange={setApiKey}
            cleared={keyCleared}
            onClear={setKeyCleared}
          />

          <div className="flex items-center justify-between gap-6">
            <div className="min-w-0">
              <p id="bitsearch-label" className="text-sm text-white">
                {t('settings.bitsearch')}
              </p>
              <p className="mt-0.5 text-xs text-[#8c8c8c]">{t('settings.bitsearchHint')}</p>
            </div>
            <Toggle checked={bitsearch} onChange={setBitsearch} labelledBy="bitsearch-label" />
          </div>

          <div className="flex flex-wrap gap-3 pt-1">
            <button type="submit" disabled={saving || !dirty} className={primaryButton}>
              {saving && <Loader2 className="h-4 w-4 animate-spin" />}
              {t('common.save')}
            </button>
            <button type="button" onClick={test} disabled={testing || !url.trim()} className={secondaryButton}>
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
