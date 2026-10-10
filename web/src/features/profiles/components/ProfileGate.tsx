"use client";

import { useEffect, useState, type ReactNode } from 'react';
import { usePathname, useRouter } from 'next/navigation';
import { getActiveProfile, setActiveProfile, useActiveProfile } from '@/shared/lib/profile';
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
  const hasProfile = !!useActiveProfile();
  // Set once the backend check of this page load is done; later navigations only need hasProfile
  const [validated, setValidated] = useState(false);
  const verified = validated && hasProfile;

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
        } else if (stored) {
          // Unknown to the backend: clearing it sends the effect below to the picker
          setActiveProfile(null);
        }
      } catch {
        // Backend unreachable: keep whatever is stored
      } finally {
        clearTimeout(timer);
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
