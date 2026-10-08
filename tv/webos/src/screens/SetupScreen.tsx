import { useEffect, useState } from 'react';
import Keyboard from '../components/Keyboard';
import { DEFAULT_SERVER, SERVER_CANDIDATES, getServer, isPackaged, normalizeServer, setServer } from '../lib/config';
import { useT } from '../lib/i18n';
import { useFocusRestore, useRouter } from '../nav/router';

type Status = { kind: 'idle' } | { kind: 'testing' } | { kind: 'ok'; version: string } | { kind: 'failed'; error: string };

async function testServer(url: string, timeoutMs = 8000): Promise<{ version: string }> {
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), timeoutMs);
  try {
    const res = await fetch(`${url}/api/health`, { signal: ctrl.signal, cache: 'no-store' });
    if (!res.ok) throw new Error(`HTTP ${res.status}`);
    const data = await res.json();
    // The profiles call is the first one with custom headers (a CORS preflight)
    const profiles = await fetch(`${url}/api/profiles`, { signal: ctrl.signal, headers: { 'X-Lang': 'en' }, cache: 'no-store' });
    if (!profiles.ok) throw new Error(`HTTP ${profiles.status}`);
    return { version: String(data?.version || '') };
  } finally {
    clearTimeout(timer);
  }
}

// First run (and Settings > Change server): where the Nextflix server lives
export default function SetupScreen() {
  const t = useT();
  const router = useRouter();
  const [value, setValue] = useState(() => getServer() || DEFAULT_SERVER);
  const [status, setStatus] = useState<Status>({ kind: 'idle' });
  useFocusRestore(true, 'setup-save');

  // First run: probe the usual addresses and preselect the first one that answers
  useEffect(() => {
    if (getServer()) return;
    let cancelled = false;
    setStatus({ kind: 'testing' });
    Promise.all(SERVER_CANDIDATES.map(url => testServer(url, 4000).then(r => ({ url, ...r }), () => null))).then(found => {
      if (cancelled) return;
      const hit = found.find(Boolean);
      if (hit) {
        setValue(hit.url);
        setStatus({ kind: 'ok', version: hit.version });
      } else {
        setStatus({ kind: 'idle' });
      }
    });
    return () => { cancelled = true; };
  }, []);

  const run = async (save: boolean) => {
    const url = normalizeServer(value);
    setValue(url);
    setStatus({ kind: 'testing' });
    try {
      const { version } = await testServer(url);
      setStatus({ kind: 'ok', version });
      if (save) {
        setServer(url);
        router.reset({ name: 'profiles' });
      }
    } catch (e) {
      const msg = e instanceof Error ? (e.name === 'AbortError' ? 'timeout' : e.message) : String(e);
      setStatus({ kind: 'failed', error: msg });
    }
  };

  return (
    <div className="screen setup-screen">
      <div className="brand">NEXTFLIX</div>
      <h1 className="setup-title">{t('setup.title')}</h1>
      <div className="setup-body">
        <div className="setup-left">
          <label className="field-label">{t('setup.address')}</label>
          <div className="text-field">{value}<span className="caret" /></div>
          <p className="setup-hint">{t('setup.hint')}</p>
          <div className="setup-buttons" data-nav-group="setup-actions">
            <button type="button" className="btn btn-primary" data-nav data-nav-id="setup-save" onClick={() => run(true)}>
              {t('setup.save')}
            </button>
            <button type="button" className="btn btn-secondary" data-nav data-nav-id="setup-test" onClick={() => run(false)}>
              {t('setup.test')}
            </button>
          </div>
          <div className={`setup-status is-${status.kind}`}>
            {status.kind === 'testing' && t('setup.testing')}
            {status.kind === 'ok' && t('setup.ok', { version: status.version })}
            {status.kind === 'failed' && (
              <>
                {t('setup.failed', { error: status.error })}
                {isPackaged() && <div className="setup-cors">{t('setup.corsHint')}</div>}
              </>
            )}
          </div>
        </div>
        <Keyboard layout="url" value={value} onChange={v => { setValue(v); setStatus({ kind: 'idle' }); }} />
      </div>
    </div>
  );
}
