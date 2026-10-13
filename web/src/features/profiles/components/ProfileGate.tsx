"use client";

import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { usePathname, useRouter } from 'next/navigation';
import { getActiveProfile, setActiveProfile, useActiveProfile } from '@/shared/lib/profile';
import { listProfiles } from '@/features/profiles/api';

const PICKER_PATH = '/profiles';
const CHECK_TIMEOUT_MS = 6000;
// Set by the picker right before it navigates (sessionStorage, this tab only)
export const PICKED_KEY = 'hfx-profile-picked';
// Last successful backend check {id, at} (localStorage)
const CHECKED_KEY = 'hfx-profile-checked';
const CHECKED_TRUST_MS = 24 * 60 * 60 * 1000;

function isPickerPath(path: string | null) {
  return !!path && (path === PICKER_PATH || path.startsWith(`${PICKER_PATH}/`));
}

// A profile just picked, or one the backend confirmed within a day, renders before the check ends
function isTrusted(id: number): boolean {
  try {
    if (window.sessionStorage.getItem(PICKED_KEY) === String(id)) return true;
    const checked = JSON.parse(window.localStorage.getItem(CHECKED_KEY) || 'null');
    return checked?.id === id && typeof checked.at === 'number' && Date.now() - checked.at < CHECKED_TRUST_MS;
  } catch {
    return false;
  }
}

function rememberCheck(id: number | null) {
  try {
    if (id) window.localStorage.setItem(CHECKED_KEY, JSON.stringify({ id, at: Date.now() }));
    else window.localStorage.removeItem(CHECKED_KEY);
  } catch {
    /* storage unavailable */
  }
}

function clearPicked() {
  try {
    window.sessionStorage.removeItem(PICKED_KEY);
  } catch {
    /* storage unavailable */
  }
}

/**
 * Requires an active profile on every page except the profile picker.
 * The stored profile is validated against the backend once per page load;
 * if the backend is unreachable the stored profile is trusted. A trusted profile
 * (see isTrusted) renders right away while that check runs in the background.
 */
export default function ProfileGate({ children }: { children: ReactNode }) {
  const pathname = usePathname();
  const router = useRouter();
  const onPicker = isPickerPath(pathname);
  const profileId = useActiveProfile()?.id ?? null;
  const hasProfile = profileId !== null;
  // Set once the backend check of this page load is done; later navigations only need hasProfile
  const [validated, setValidated] = useState(false);
  // hasProfile is false on the server and during hydration, so storage is only read on the client
  const trusted = useMemo(() => profileId !== null && isTrusted(profileId), [profileId]);
  const verified = (validated || trusted) && hasProfile;

  useEffect(() => {
    if (onPicker || validated) return;

    let cancelled = false;
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), CHECK_TIMEOUT_MS);

    (async () => {
      const stored = getActiveProfile();
      try {
        const profiles = await listProfiles(controller.signal);
        const fresh = stored ? profiles.find((p) => p.id === stored.id) : undefined;
        if (fresh) {
          if (
            fresh.name !== stored!.name ||
            fresh.avatar !== stored!.avatar ||
            fresh.color !== stored!.color ||
            fresh.language !== stored!.language
          ) {
            setActiveProfile(fresh);
          }
          rememberCheck(fresh.id);
        } else if (stored) {
          // Unknown to the backend: clearing it sends the effect below to the picker
          rememberCheck(null);
          setActiveProfile(null);
        }
      } catch {
        // Backend unreachable: keep whatever is stored
      } finally {
        clearTimeout(timer);
        clearPicked();
      }
      if (cancelled) return;
      setValidated(true);
    })();

    return () => {
      cancelled = true;
      clearTimeout(timer);
      controller.abort();
    };
  }, [onPicker, validated]);

  // No profile (none stored, cleared elsewhere, e.g. deleted in another tab) -> the picker
  useEffect(() => {
    if (onPicker || !validated || hasProfile) return;
    const current = window.location.pathname + window.location.search;
    router.replace(`${PICKER_PATH}?next=${encodeURIComponent(current)}`);
  }, [onPicker, validated, hasProfile, router]);

  if (onPicker) return <>{children}</>;
  if (!verified) return <div className="fixed inset-0 bg-[#141414]" aria-busy="true" />;
  return <>{children}</>;
}
