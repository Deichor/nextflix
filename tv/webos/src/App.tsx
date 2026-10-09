import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import ConfirmDialog from './components/ConfirmDialog';
import Intro from './components/Intro';
import { getServer } from './lib/config';
import { I18nContext, makeI18n } from './lib/i18n';
import { playSting, takeIntro, whenOnScreen } from './lib/intro';
import { profileLocale, setProfile, type Profile } from './lib/profile';
import type { PlayRequest } from './lib/stream';
import { exitApp } from './lib/webos';
import { EntryProvider, RouterProvider, useRouter, type Entry } from './nav/router';
import { PlayerContext } from './nav/player';
import { focusById, focusedId, setBackHandler } from './nav/spatial';
import Player from './player/Player';
import AddProfileScreen from './screens/AddProfileScreen';
import DetailScreen from './screens/DetailScreen';
import HomeScreen from './screens/HomeScreen';
import MoviesScreen from './screens/MoviesScreen';
import ProfilesScreen from './screens/ProfilesScreen';
import SearchScreen from './screens/SearchScreen';
import SettingsScreen from './screens/SettingsScreen';
import SetupScreen from './screens/SetupScreen';
import SurpriseScreen from './screens/SurpriseScreen';
import TvShowsScreen from './screens/TvShowsScreen';

const TABS = ['home', 'search', 'movies', 'tv', 'settings'];

function Screen({ entry, onPickProfile }: { entry: Entry; onPickProfile: (p: Profile) => void }) {
  const r = entry.route;
  switch (r.name) {
    case 'setup': return <SetupScreen />;
    case 'profiles': return <ProfilesScreen onPick={onPickProfile} />;
    case 'addProfile': return <AddProfileScreen count={r.count} />;
    case 'home': return <HomeScreen />;
    case 'search': return <SearchScreen />;
    case 'movies': return <MoviesScreen />;
    case 'tv': return <TvShowsScreen />;
    case 'settings': return <SettingsScreen />;
    case 'surprise': return <SurpriseScreen />;
    case 'detail': return <DetailScreen id={r.id} type={r.type} autoplay={r.autoplay} />;
  }
}

function Shell({ onLocale }: { onLocale: (p: Profile) => void }) {
  const router = useRouter();
  const top = router.stack[router.stack.length - 1];
  const [play, setPlay] = useState<{ req: PlayRequest; onClosed?: () => void; focus: string | null } | null>(null);
  const [exitAsk, setExitAsk] = useState(false);
  const cancelExit = useCallback(() => setExitAsk(false), []);
  const playRef = useRef(play);
  playRef.current = play;

  const pickProfile = useCallback((p: Profile) => {
    setProfile(p);
    onLocale(p);
    router.reset({ name: 'home' });
  }, [router, onLocale]);

  // Back on a screen: previous screen; on a tab: Home; on Home: the menu, then "Exit?"
  useEffect(() => {
    setBackHandler(() => {
      if (playRef.current || exitAsk) return;
      if (router.back()) return;
      const name = top.route.name;
      if (name === 'home') {
        const inMenu = document.activeElement && (document.activeElement as HTMLElement).closest('.sidenav');
        if (!inMenu && focusById('nav-home', true)) return;
        setExitAsk(true);
      } else if (TABS.indexOf(name) >= 0) {
        router.reset({ name: 'home' });
      } else {
        setExitAsk(true);
      }
    });
  }, [router, top, exitAsk]);

  const player = useMemo(() => ({
    open: (req: PlayRequest, onClosed?: () => void) => setPlay({ req, onClosed, focus: focusedId() }),
  }), []);

  const closePlayer = useCallback(() => {
    const p = playRef.current;
    setPlay(null);
    if (!p) return;
    requestAnimationFrame(() => focusById(p.focus));
    // Let the final progress save land before the screen reloads it
    if (p.onClosed) setTimeout(p.onClosed, 700);
  }, []);

  return (
    <PlayerContext.Provider value={player}>
      <div className={`app ${play ? 'is-playing' : ''}`}>
        <EntryProvider key={top.key} value={top}>
          <Screen entry={top} onPickProfile={pickProfile} />
        </EntryProvider>
      </div>
      {!!play && <Player req={play.req} onClose={closePlayer} />}
      {exitAsk && (
        <I18nExitDialog onCancel={cancelExit} />
      )}
    </PlayerContext.Provider>
  );
}

function I18nExitDialog({ onCancel }: { onCancel: () => void }) {
  return (
    <I18nContext.Consumer>
      {({ t }) => (
        <ConfirmDialog
          title={t('exit.title')}
          confirm={t('exit.exit')}
          cancel={t('common.cancel')}
          onConfirm={exitApp}
          onCancel={onCancel}
        />
      )}
    </I18nContext.Consumer>
  );
}

export default function App() {
  const [locale, setLocale] = useState(profileLocale());
  const i18n = useMemo(() => makeI18n(locale), [locale]);
  useEffect(() => { document.documentElement.lang = locale; }, [locale]);
  const onLocale = useCallback((p: Profile) => setLocale(profileLocale(p)), []);
  // Cold launch only; the screens below load in parallel (a browser may keep the sound muted)
  const [intro, setIntro] = useState(() => {
    const plan = takeIntro();
    if (plan && !plan.animate) {
      whenOnScreen().then(() => playSting());
      return null;
    }
    return plan;
  });
  const endIntro = useCallback(() => setIntro(null), []);

  return (
    <I18nContext.Provider value={i18n}>
      <RouterProvider initial={getServer() ? { name: 'profiles' } : { name: 'setup' }}>
        <Shell onLocale={onLocale} />
      </RouterProvider>
      {!!intro && <Intro sound={intro.sound} onDone={endIntro} />}
    </I18nContext.Provider>
  );
}
