import { useEffect, useRef, useState } from 'react';
import { Plus, Settings } from 'lucide-react';
import Avatar from '../components/Avatar';
import Spinner from '../components/Spinner';
import { profiles as loadProfiles } from '../lib/catalog';
import { useT } from '../lib/i18n';
import { MAX_PROFILES, getProfileId, type Profile } from '../lib/profile';
import { useFocusRestore, useRouter } from '../nav/router';
import { focusById, focusDefault } from '../nav/spatial';

interface ProfilesScreenProps {
  onPick: (p: Profile) => void;
}

// Netflix "Who's watching?"
export default function ProfilesScreen({ onPick }: ProfilesScreenProps) {
  const t = useT();
  const router = useRouter();
  const [list, setList] = useState<Profile[] | null>(null);
  const [error, setError] = useState('');
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    let cancelled = false;
    setError('');
    loadProfiles()
      .then(p => { if (!cancelled) setList(p); })
      .catch(e => { if (!cancelled) { setList([]); setError(e instanceof Error ? e.message : String(e)); } });
    return () => { cancelled = true; };
  }, [attempt]);

  const last = getProfileId();
  const canAdd = !!list && !error && list.length < MAX_PROFILES;
  const fallback = !list ? undefined
    : list.some(p => p.id === last) ? `profile-${last}`
    : list[0] ? `profile-${list[0].id}`
    : canAdd ? 'profile-add' : 'profiles-retry';
  useFocusRestore(list !== null, fallback);

  // Retry removes the focused button: focus the first profile (or Retry again) once the answer is in
  const refocus = useRef(false);
  useEffect(() => {
    if (!refocus.current || list === null) return;
    const raf = requestAnimationFrame(() => {
      refocus.current = false;
      // The viewer already moved on (e.g. to "Change server")
      const cur = document.activeElement as HTMLElement | null;
      if (cur && cur !== document.body && document.body.contains(cur) && cur.hasAttribute('data-nav')) return;
      if (!focusById(fallback)) focusDefault();
    });
    return () => cancelAnimationFrame(raf);
  }, [list, fallback]);

  return (
    <div className="screen profiles-screen">
      <div className="brand brand-corner">NEXTFLIX</div>
      <h1 className="profiles-title">{t('profiles.whoIsWatching')}</h1>
      {list === null && <Spinner />}
      {!!list && (list.length > 0 || canAdd) && (
        <div className="profiles-list" data-nav-group="profiles">
          {list.map(p => (
            <button key={p.id} type="button" className="profile-tile" data-nav data-nav-id={`profile-${p.id}`} onClick={() => onPick(p)}>
              <Avatar profile={p} className="profile-avatar" />
              <span className="profile-name">{p.name}</span>
            </button>
          ))}
          {canAdd && (
            <button
              type="button"
              className="profile-tile profile-add"
              data-nav
              data-nav-id="profile-add"
              onClick={() => router.push({ name: 'addProfile', count: list.length })}
            >
              <div className="profile-avatar profile-add-square">
                <Plus className="profile-add-icon" strokeWidth={1.5} />
              </div>
              <span className="profile-name">{t('profiles.addProfile')}</span>
            </button>
          )}
        </div>
      )}
      {!!error && (
        <div className="profiles-error">
          <p>{t('profiles.couldNotReach', { error })}</p>
          <button type="button" className="btn btn-primary" data-nav data-nav-id="profiles-retry" onClick={() => { refocus.current = true; setList(null); setAttempt(a => a + 1); }}>
            {t('common.retry')}
          </button>
        </div>
      )}
      <button type="button" className="btn btn-ghost profiles-settings" data-nav data-nav-id="profiles-server" onClick={() => router.push({ name: 'setup' })}>
        <Settings className="btn-icon" /> {t('settings.changeServer')}
      </button>
    </div>
  );
}
