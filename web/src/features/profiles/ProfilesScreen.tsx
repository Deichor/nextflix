"use client";

import { Suspense, useCallback, useEffect, useState } from 'react';
import { useRouter, useSearchParams } from 'next/navigation';
import { Pencil, PlusCircle, RefreshCw } from 'lucide-react';
import ProfileAvatar from '@/features/profiles/components/ProfileAvatar';
import ProfileEditor from '@/features/profiles/components/ProfileEditor';
import { setActiveProfile, type Profile } from '@/shared/lib/profile';
import { listProfiles, MAX_PROFILES } from '@/features/profiles/api';
import { useT } from '@/shared/i18n';
import { usePageTitle } from '@/shared/hooks/usePageTitle';

const AVATAR_SIZE = 'clamp(84px, 10vw, 180px)';

// Only allow same-origin paths and never bounce back to the picker
function safeNext(next: string | null): string {
  if (!next || !next.startsWith('/') || next.startsWith('//') || next.startsWith('/profiles')) return '/';
  return next;
}

function ProfilesPicker() {
  const t = useT();
  const router = useRouter();
  const params = useSearchParams();
  const next = safeNext(params.get('next'));

  const [profiles, setProfiles] = useState<Profile[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [manage, setManage] = useState(params.get('manage') === '1');
  // undefined = closed, null = new profile
  const [editing, setEditing] = useState<Profile | null | undefined>(undefined);

  const load = useCallback(async () => {
    setError(null);
    try {
      setProfiles(await listProfiles());
    } catch (e) {
      setError(e instanceof Error ? e.message : '');
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  const select = (profile: Profile) => {
    if (manage) {
      setEditing(profile);
      return;
    }
    setActiveProfile(profile);
    // Full navigation so every page refetches with the new profile
    window.location.assign(next);
  };

  const toggleManage = () => {
    // The ?manage=1 entry point came from the navbar: "Done" returns there
    if (manage && params.get('manage') === '1') {
      router.replace(params.get('next') ? `/profiles?next=${encodeURIComponent(next)}` : '/profiles');
    }
    setManage((m) => !m);
  };

  usePageTitle(manage ? t('profiles.manageProfiles') : t('profiles.whoIsWatching'));

  const canAdd = !!profiles && profiles.length < MAX_PROFILES;

  return (
    <main className="fixed inset-0 overflow-y-auto bg-[#141414] text-white">
      <div className="absolute left-0 top-0 px-4 py-5 md:px-12">
        <span className="text-2xl font-black tracking-tight text-[#E50914] md:text-3xl">NEXTFLIX</span>
      </div>

      <div className="flex min-h-full flex-col items-center justify-center px-4 py-24">
        <h1 className="text-center text-3xl font-medium md:text-[3.5vw] md:leading-tight">
          {manage ? t('profiles.manageTitle') : t('profiles.whoIsWatching')}
        </h1>

        {error !== null && !profiles && (
          <div className="mt-10 flex flex-col items-center gap-4 text-center">
            <p className="text-[#b3b3b3]">{t('profiles.couldNotReach', { error: error || t('profiles.couldNotLoad') })}</p>
            <button
              type="button"
              onClick={load}
              className="inline-flex items-center gap-2 border border-[#808080] px-6 py-2 uppercase tracking-[0.15em] text-[#808080] transition-colors hover:border-white hover:text-white"
            >
              <RefreshCw className="h-4 w-4" /> {t('common.retry')}
            </button>
          </div>
        )}

        {!profiles && error === null && (
          <div className="mt-10 flex gap-6" aria-busy="true">
            {[0, 1, 2].map((i) => (
              <div key={i} className="animate-pulse rounded-[8%] bg-[#2a2a2a]" style={{ width: AVATAR_SIZE, height: AVATAR_SIZE }} />
            ))}
          </div>
        )}

        {profiles && (
          <ul className="mt-8 flex flex-wrap justify-center gap-[2vw] md:mt-10">
            {profiles.map((p) => (
              <li key={p.id}>
                <button
                  type="button"
                  onClick={() => select(p)}
                  className="group flex flex-col items-center outline-none"
                  style={{ width: AVATAR_SIZE }}
                >
                  <div className="relative">
                    <ProfileAvatar
                      profile={p}
                      size={AVATAR_SIZE}
                      className={`outline outline-[3px] outline-offset-0 outline-transparent transition-[outline-color] group-hover:outline-white group-focus-visible:outline-white ${
                        manage ? 'opacity-50' : ''
                      }`}
                    />
                    {manage && (
                      <div className="pointer-events-none absolute inset-0 flex items-center justify-center rounded-[8%] bg-black/40">
                        <span className="flex h-[38%] w-[38%] items-center justify-center rounded-full border-2 border-white">
                          <Pencil className="h-1/2 w-1/2" />
                        </span>
                      </div>
                    )}
                  </div>
                  <span className="mt-3 w-full truncate text-center text-sm text-[#808080] transition-colors group-hover:text-white group-focus-visible:text-white md:text-lg">
                    {p.name}
                  </span>
                </button>
              </li>
            ))}

            {canAdd && (
              <li>
                <button
                  type="button"
                  onClick={() => setEditing(null)}
                  className="group flex flex-col items-center outline-none"
                  style={{ width: AVATAR_SIZE }}
                >
                  <div
                    className="flex items-center justify-center rounded-[8%] outline outline-[3px] outline-transparent transition-colors group-hover:bg-[#e5e5e5] group-hover:outline-white group-focus-visible:outline-white"
                    style={{ width: AVATAR_SIZE, height: AVATAR_SIZE }}
                  >
                    <PlusCircle className="h-1/2 w-1/2 text-[#808080] group-hover:text-[#141414]" strokeWidth={1.25} />
                  </div>
                  <span className="mt-3 text-sm text-[#808080] transition-colors group-hover:text-white md:text-lg">
                    {t('profiles.addProfile')}
                  </span>
                </button>
              </li>
            )}
          </ul>
        )}

        {profiles && (
          <button
            type="button"
            onClick={toggleManage}
            className={`mt-12 px-6 py-2 text-sm uppercase tracking-[0.2em] transition-colors md:mt-16 md:px-8 md:text-lg ${
              manage
                ? 'bg-white font-semibold text-black hover:bg-[#E50914] hover:text-white'
                : 'border border-[#808080] text-[#808080] hover:border-white hover:text-white'
            }`}
          >
            {manage ? t('common.done') : t('profiles.manageProfiles')}
          </button>
        )}
      </div>

      {editing !== undefined && (
        <ProfileEditor
          profile={editing}
          profileCount={profiles?.length ?? 0}
          onClose={() => setEditing(undefined)}
          onChanged={load}
        />
      )}
    </main>
  );
}

export default function ProfilesPage() {
  return (
    <Suspense fallback={<div className="fixed inset-0 bg-[#141414]" />}>
      <ProfilesPicker />
    </Suspense>
  );
}
