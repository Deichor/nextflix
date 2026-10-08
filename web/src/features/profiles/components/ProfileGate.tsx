"use client";

import { useEffect, useRef, useState, type ReactNode } from 'react';
import { usePathname, useRouter } from 'next/navigation';
import { getActiveProfile, setActiveProfile, PROFILE_EVENT } from '@/shared/lib/profile';
import { listProfiles } from '@/features/profiles/api';

const PICKER_PATH = '/profiles';
const CHECK_TIMEOUT_MS = 6000;

function isPickerPath(path: string | null) {
  return !!path && (path === PICKER_PATH || path.startsWith(`${PICKER_PATH}/`));
}

/**
 * Requires an active profile on every page except the profile picker.
 * The stored profile is validated against the backend once per page load;
 * if the backend is unreachable the stored profile is trusted.
 */
export default function ProfileGate({ children }: { children: ReactNode }) {
  const pathname = usePathname();
  const router = useRouter();
  const onPicker = isPickerPath(pathname);
  const [verified, setVerified] = useState(false);
  const validated = useRef(false);

  useEffect(() => {
    if (onPicker) return;

    const redirect = () => {
      const current = window.location.pathname + window.location.search;
      router.replace(`${PICKER_PATH}?next=${encodeURIComponent(current)}`);
    };

    // Later navigations only need the cheap local check
    if (validated.current) {
      if (getActiveProfile()) setVerified(true);
      else {
        setVerified(false);
        redirect();
      }
      return;
    }

    let cancelled = false;
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), CHECK_TIMEOUT_MS);

    (async () => {
      const stored = getActiveProfile();
      let ok = !!stored;
      try {
        const profiles = await listProfiles(controller.signal);
        const fresh = stored ? profiles.find((p) => p.id === stored.id) : undefined;
        if (fresh) {
          if (fresh.name !== stored!.name || fresh.avatar !== stored!.avatar || fresh.color !== stored!.color) {
            setActiveProfile(fresh);
          }
          ok = true;
        } else {
          if (stored) setActiveProfile(null);
          ok = false;
        }
      } catch {
        // Backend unreachable: keep whatever is stored
      } finally {
        clearTimeout(timer);
      }
      if (cancelled) return;
      validated.current = true;
      if (ok) setVerified(true);
      else redirect();
    })();

    return () => {
      cancelled = true;
      clearTimeout(timer);
      controller.abort();
    };
  }, [onPicker, pathname, router]);

  // Profile cleared elsewhere (e.g. deleted in another tab) -> back to the picker
  useEffect(() => {
    if (onPicker) return;
    const onChange = () => {
      if (!getActiveProfile()) {
        setVerified(false);
        const current = window.location.pathname + window.location.search;
        router.replace(`${PICKER_PATH}?next=${encodeURIComponent(current)}`);
      }
    };
    window.addEventListener(PROFILE_EVENT, onChange);
    window.addEventListener('storage', onChange);
    return () => {
      window.removeEventListener(PROFILE_EVENT, onChange);
      window.removeEventListener('storage', onChange);
    };
  }, [onPicker, router]);

  if (onPicker) return <>{children}</>;
  if (!verified) return <div className="fixed inset-0 bg-[#141414]" aria-busy="true" />;
  return <>{children}</>;
}
