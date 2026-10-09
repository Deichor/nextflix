import { useState } from 'react';
import { Film, Home, Search, Settings, Shuffle, Tv } from 'lucide-react';
import { useT, type MessageKey } from '../lib/i18n';
import { getProfile } from '../lib/profile';
import { useRouter, type Tab } from '../nav/router';
import Avatar from './Avatar';

const ITEMS: { tab: Tab; label: MessageKey; icon: typeof Home }[] = [
  { tab: 'search', label: 'nav.search', icon: Search },
  { tab: 'home', label: 'nav.home', icon: Home },
  { tab: 'tv', label: 'nav.tvShows', icon: Tv },
  { tab: 'movies', label: 'nav.movies', icon: Film },
  { tab: 'settings', label: 'nav.settings', icon: Settings },
];

// Netflix TV-style rail on the left; it widens while it has focus. A class instead of
// :focus-within, which makes every focus move anywhere re-check the styles of the whole page.
export default function SideNav({ active }: { active: Tab }) {
  const t = useT();
  const router = useRouter();
  const profile = getProfile();
  const [open, setOpen] = useState(false);
  return (
    <nav
      className={`sidenav ${open ? 'is-open' : ''}`}
      data-nav-group="sidenav"
      data-nav-enter="left"
      onFocus={() => setOpen(true)}
      onBlur={e => { if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setOpen(false); }}
    >
      {!!profile && (
        <button
          type="button"
          className="sidenav-item sidenav-profile"
          data-nav
          data-nav-id="nav-profile"
          onClick={() => router.reset({ name: 'profiles' })}
        >
          <Avatar profile={profile} className="sidenav-avatar" />
          <span className="sidenav-label">{profile.name}</span>
        </button>
      )}
      <div className="sidenav-items">
        {ITEMS.map(({ tab, label, icon: Icon }) => (
          <button
            key={tab}
            type="button"
            className={`sidenav-item ${tab === active ? 'is-active' : ''}`}
            data-nav
            data-nav-id={`nav-${tab}`}
            data-nav-default={tab === active ? '' : undefined}
            onClick={() => { if (tab !== active) router.reset({ name: tab }); }}
          >
            <Icon className="sidenav-icon" />
            <span className="sidenav-label">{t(label)}</span>
          </button>
        ))}
        <button
          type="button"
          className="sidenav-item"
          data-nav
          data-nav-id="nav-surprise"
          onClick={() => router.push({ name: 'surprise' })}
        >
          <Shuffle className="sidenav-icon" />
          <span className="sidenav-label">{t('nav.surprise')}</span>
        </button>
      </div>
    </nav>
  );
}
