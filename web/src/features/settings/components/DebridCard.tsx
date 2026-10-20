"use client";

import { useCallback, useEffect, useRef, useState, type FormEvent } from 'react';
import { Loader2 } from 'lucide-react';
import {
  getDebridSettings,
  saveDebridSettings,
  testDebrid,
  type DebridSettings,
  type DebridTestResult,
} from '@/features/settings/api';
import SecretField from '@/features/settings/components/SecretField';
import {
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
import { useFormat, useT } from '@/shared/i18n';

export default function DebridCard() {
  const t = useT();
  const f = useFormat();
  const [settings, setSettings] = useState<DebridSettings | null>(null);
  const [apiKey, setApiKey] = useState('');
  const [keyCleared, setKeyCleared] = useState(false);
  const [enabled, setEnabled] = useState(true);
  const [prefer, setPrefer] = useState(true);
  const [addUncached, setAddUncached] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [status, setStatus] = useState<Status>(null);
  const [connection, setConnection] = useState<ConnectionState>('checking');
  const [connectionNote, setConnectionNote] = useState('');

  const planText = useCallback(
    (r: DebridTestResult) => {
      const plan = r.planName || 'TorBox';
      if (r.active === false) return t('settings.debridPlanExpired', { plan });
      if (!r.expiresAt) return t('settings.debridPlanNoDate', { plan });
      return t('settings.debridPlan', { plan, date: f.date(r.expiresAt, { dateStyle: 'medium' }) });
    },
    [t, f],
  );
  // A ref keeps checkStored (and the load effect) stable, so a language change does not reload and reset the form
  const planTextRef = useRef(planText);
  useEffect(() => { planTextRef.current = planText; }, [planText]);

  // Asks TorBox with the stored key so the badge shows the real state and the plan
  const checkStored = useCallback(
    (s: DebridSettings) => {
      if (!s.torbox.api_key_set) {
        setConnection('notSet');
        setConnectionNote('');
        return;
      }
      setConnection('checking');
      testDebrid('')
        .then((r) => {
          setConnection(r.success && r.active !== false ? 'connected' : 'error');
          setConnectionNote(r.success ? planTextRef.current(r) : r.message);
        })
        .catch((e: unknown) => {
          setConnection('error');
          setConnectionNote((e as Error)?.message || '');
        });
    },
    [],
  );

  const reset = useCallback((s: DebridSettings) => {
    setSettings(s);
    setApiKey('');
    setKeyCleared(false);
    setEnabled(s.torbox.enabled);
    setPrefer(s.torbox.prefer);
    setAddUncached(s.torbox.add_uncached);
  }, []);

  useEffect(() => {
    const ctrl = new AbortController();
    getDebridSettings(ctrl.signal)
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
    (apiKey.trim() !== '' ||
      keyCleared ||
      enabled !== settings.torbox.enabled ||
      prefer !== settings.torbox.prefer ||
      addUncached !== settings.torbox.add_uncached);

  const save = async (e: FormEvent) => {
    e.preventDefault();
    if (!settings) return;
    setSaving(true);
    setStatus(null);
    try {
      const saved = await saveDebridSettings({
        torbox: {
          enabled,
          prefer,
          add_uncached: addUncached,
          // Omitted keeps the stored key, null removes it
          ...(keyCleared ? { api_key: null } : apiKey.trim() ? { api_key: apiKey.trim() } : {}),
        },
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
      const r = await testDebrid(keyCleared ? '' : apiKey.trim());
      setStatus({ kind: r.success && r.active !== false ? 'ok' : 'error', text: r.success ? planText(r) : r.message });
    } catch (err) {
      setStatus({ kind: 'error', text: (err as Error)?.message || t('settings.couldNotReach') });
    } finally {
      setTesting(false);
    }
  };

  const toggles: { id: string; label: string; hint: string; value: boolean; set: (v: boolean) => void }[] = [
    { id: 'debrid-enabled', label: t('settings.debridEnabled'), hint: t('settings.debridEnabledHint'), value: enabled, set: setEnabled },
    { id: 'debrid-prefer', label: t('settings.debridPrefer'), hint: t('settings.debridPreferHint'), value: prefer, set: setPrefer },
    {
      id: 'debrid-add',
      label: t('settings.debridAddUncached'),
      hint: t('settings.debridAddUncachedHint'),
      value: addUncached,
      set: setAddUncached,
    },
  ];

  return (
    <SettingsCard
      title={t('settings.debridTitle')}
      summary={t('settings.debridSummary')}
      badge={<StatusBadge state={connection} title={connectionNote || undefined} />}
    >
      {loadError !== null ? (
        <p className="text-sm text-[#e87c03]">{loadError || t('settings.couldNotLoad')}</p>
      ) : !settings ? (
        <LoadingLine />
      ) : (
        <form onSubmit={save} className="space-y-5">
          <SecretField
            label={t('settings.apiKey')}
            hint={t('settings.debridKeyHint')}
            isSet={settings.torbox.api_key_set}
            value={apiKey}
            onChange={setApiKey}
            cleared={keyCleared}
            onClear={setKeyCleared}
          />
          {connection === 'connected' && connectionNote && <p className="text-xs text-[#46d369]">{connectionNote}</p>}

          {toggles.map((o) => (
            <div key={o.id} className="flex items-center justify-between gap-6">
              <div className="min-w-0">
                <p id={`${o.id}-label`} className="text-sm text-white">
                  {o.label}
                </p>
                <p className="mt-0.5 text-xs text-[#8c8c8c]">{o.hint}</p>
              </div>
              <Toggle checked={o.value} onChange={o.set} labelledBy={`${o.id}-label`} />
            </div>
          ))}

          <div className="flex flex-wrap gap-3 pt-1">
            <button type="submit" disabled={saving || !dirty} className={primaryButton}>
              {saving && <Loader2 className="h-4 w-4 animate-spin" />}
              {t('common.save')}
            </button>
            <button
              type="button"
              onClick={test}
              disabled={testing || (!settings.torbox.api_key_set && !apiKey.trim()) || (keyCleared && !apiKey.trim())}
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
