"use client";

import { useEffect, useState, type FormEvent } from 'react';
import Link from 'next/link';
import { CheckCircle2, Eye, EyeOff, Loader2, XCircle } from 'lucide-react';
import { usePageTitle } from '@/shared/hooks/usePageTitle';
import PageShell from '@/features/shell/PageShell';
import {
  getIndexerSettings,
  saveIndexerSettings,
  testIndexer,
  type IndexerSettings,
} from '@/features/settings/api';

type Status = { kind: 'ok' | 'error'; text: string } | null;

const inputClass =
  'w-full rounded bg-[#333] px-3 py-2.5 text-sm text-white outline-none placeholder:text-[#8c8c8c] focus:ring-2 focus:ring-white/70';

function StatusLine({ status }: { status: Status }) {
  if (!status) return null;
  const ok = status.kind === 'ok';
  const Icon = ok ? CheckCircle2 : XCircle;
  return (
    <p className={`mt-4 flex items-start gap-2 text-sm ${ok ? 'text-[#46d369]' : 'text-[#e87c03]'}`} role="status">
      <Icon className="mt-0.5 h-4 w-4 shrink-0" />
      <span>{status.text}</span>
    </p>
  );
}

function IndexerSection() {
  const [settings, setSettings] = useState<IndexerSettings | null>(null);
  const [url, setUrl] = useState('');
  const [apiKey, setApiKey] = useState('');
  const [showKey, setShowKey] = useState(false);
  const [loadError, setLoadError] = useState('');
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [status, setStatus] = useState<Status>(null);

  useEffect(() => {
    const ctrl = new AbortController();
    getIndexerSettings(ctrl.signal)
      .then((s) => {
        setSettings(s);
        setUrl(s.jackett_url);
        setApiKey(s.jackett_api_key);
      })
      .catch((e: unknown) => {
        if ((e as Error)?.name !== 'AbortError') setLoadError((e as Error)?.message || 'Could not load settings');
      });
    return () => ctrl.abort();
  }, []);

  const dirty = !!settings && (url.trim() !== settings.jackett_url || apiKey.trim() !== settings.jackett_api_key);

  const save = async (e: FormEvent) => {
    e.preventDefault();
    if (!settings) return;
    setSaving(true);
    setStatus(null);
    try {
      const saved = await saveIndexerSettings({ ...settings, jackett_url: url.trim(), jackett_api_key: apiKey.trim() });
      const next = {
        ...settings,
        ...saved,
        jackett_url: saved?.jackett_url ?? url.trim(),
        jackett_api_key: saved?.jackett_api_key ?? apiKey.trim(),
      };
      setSettings(next);
      setUrl(next.jackett_url);
      setApiKey(next.jackett_api_key);
      setStatus({ kind: 'ok', text: 'Settings saved.' });
    } catch (err) {
      setStatus({ kind: 'error', text: (err as Error)?.message || 'Could not save settings' });
    } finally {
      setSaving(false);
    }
  };

  const test = async () => {
    setTesting(true);
    setStatus(null);
    try {
      const result = await testIndexer({ jackett_url: url.trim(), jackett_api_key: apiKey.trim() });
      setStatus({ kind: result.success ? 'ok' : 'error', text: result.message });
    } catch (err) {
      setStatus({ kind: 'error', text: (err as Error)?.message || 'Could not reach the server' });
    } finally {
      setTesting(false);
    }
  };

  return (
    <section className="rounded-md bg-[#181818] p-6">
      <h2 className="mb-1 text-lg font-semibold">Indexer</h2>
      <p className="mb-5 text-sm text-[#b3b3b3]">
        Jackett finds the torrents behind the Play button. The API key is shown on the Jackett dashboard.
      </p>

      {loadError ? (
        <p className="text-sm text-[#e87c03]">Could not load the indexer settings. {loadError}</p>
      ) : !settings ? (
        <div className="flex items-center gap-2 text-sm text-[#b3b3b3]">
          <Loader2 className="h-4 w-4 animate-spin" /> Loading...
        </div>
      ) : (
        <form onSubmit={save} className="space-y-4">
          <label className="block">
            <span className="mb-1.5 block text-sm text-[#b3b3b3]">Jackett URL</span>
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

          <label className="block">
            <span className="mb-1.5 block text-sm text-[#b3b3b3]">API key</span>
            <div className="relative">
              <input
                type={showKey ? 'text' : 'password'}
                value={apiKey}
                onChange={(e) => setApiKey(e.target.value)}
                autoComplete="off"
                spellCheck={false}
                className={`${inputClass} pr-10 font-mono`}
              />
              <button
                type="button"
                onClick={() => setShowKey((v) => !v)}
                className="absolute inset-y-0 right-0 flex w-10 items-center justify-center text-[#b3b3b3] hover:text-white"
                aria-label={showKey ? 'Hide API key' : 'Show API key'}
              >
                {showKey ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
              </button>
            </div>
          </label>

          <div className="flex flex-wrap gap-3 pt-1">
            <button
              type="submit"
              disabled={saving || !dirty}
              className="inline-flex items-center gap-2 rounded bg-[#E50914] px-5 py-2 text-sm font-semibold text-white transition-colors hover:bg-[#f40612] disabled:cursor-not-allowed disabled:opacity-50"
            >
              {saving && <Loader2 className="h-4 w-4 animate-spin" />}
              Save
            </button>
            <button
              type="button"
              onClick={test}
              disabled={testing || !url.trim()}
              className="inline-flex items-center gap-2 rounded bg-white/15 px-5 py-2 text-sm font-semibold text-white transition-colors hover:bg-white/25 disabled:cursor-not-allowed disabled:opacity-50"
            >
              {testing && <Loader2 className="h-4 w-4 animate-spin" />}
              Test connection
            </button>
          </div>
          <StatusLine status={status} />
        </form>
      )}
    </section>
  );
}

export default function SettingsScreen() {
  usePageTitle('Settings');

  return (
    <PageShell>
      <div className="mx-auto max-w-3xl space-y-6 px-4 pt-28 md:px-12">
        <h1 className="text-3xl font-semibold md:text-4xl">Settings</h1>
        <IndexerSection />
        <section className="rounded-md bg-[#181818] p-6">
          <h2 className="mb-1 text-lg font-semibold">Profiles</h2>
          <p className="mb-4 text-sm text-[#b3b3b3]">Add, rename or remove profiles.</p>
          <Link
            href="/profiles?manage=1"
            className="inline-block rounded bg-white px-4 py-2 text-sm font-semibold text-black hover:bg-white/80"
          >
            Manage Profiles
          </Link>
        </section>
      </div>
    </PageShell>
  );
}
