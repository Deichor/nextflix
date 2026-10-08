"use client";

import { useCallback, useEffect, useRef, useState } from 'react';
import Link from 'next/link';
import { ChevronDown, Pencil, Settings, Users, User } from 'lucide-react';
import ProfileAvatar from '@/features/profiles/components/ProfileAvatar';
import { setActiveProfile, useActiveProfile, type Profile } from '@/shared/lib/profile';
import { listProfiles } from '@/features/profiles/api';

const CLOSE_DELAY_MS = 250;

/** Navbar profile dropdown: active avatar + caret, profile switcher and account links. */
export default function ProfileMenu({ className = '' }: { className?: string }) {
  const active = useActiveProfile();
  const [open, setOpen] = useState(false);
  const [profiles, setProfiles] = useState<Profile[]>([]);
  const rootRef = useRef<HTMLDivElement>(null);
  const closeTimer = useRef<ReturnType<typeof setTimeout> | null>(null);

  const loadProfiles = useCallback(() => {
    listProfiles()
      .then(setProfiles)
      .catch(() => {
        /* keep the last known list */
      });
  }, []);

  useEffect(() => {
    loadProfiles();
  }, [loadProfiles]);

  useEffect(() => {
    if (open) loadProfiles();
  }, [open, loadProfiles]);

  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent | TouchEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) setOpen(false);
    };
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') setOpen(false);
    };
    document.addEventListener('mousedown', onDown);
    document.addEventListener('touchstart', onDown);
    document.addEventListener('keydown', onKey);
    return () => {
      document.removeEventListener('mousedown', onDown);
      document.removeEventListener('touchstart', onDown);
      document.removeEventListener('keydown', onKey);
    };
  }, [open]);

  useEffect(
    () => () => {
      if (closeTimer.current) clearTimeout(closeTimer.current);
    },
    [],
  );

  const cancelClose = () => {
    if (closeTimer.current) clearTimeout(closeTimer.current);
    closeTimer.current = null;
  };
  const openNow = () => {
    cancelClose();
    setOpen(true);
  };
  const closeSoon = () => {
    cancelClose();
    closeTimer.current = setTimeout(() => setOpen(false), CLOSE_DELAY_MS);
  };

  const switchTo = (profile: Profile) => {
    setOpen(false);
    setActiveProfile(profile);
    // Reload so every list (My List, Continue Watching, ...) refetches for the new profile
    window.location.reload();
  };

  const others = profiles.filter((p) => p.id !== active?.id);
  const itemClass = 'flex w-full items-center gap-3 px-3 py-2 text-left text-sm text-white hover:underline';

  return (
    <div
      ref={rootRef}
      className={`relative ${className}`}
      onMouseEnter={() => {
        // Hover-to-open only for real mouse pointers
        if (window.matchMedia('(hover: hover)').matches) openNow();
      }}
      onMouseLeave={() => {
        if (window.matchMedia('(hover: hover)').matches) closeSoon();
      }}
    >
      <button
        type="button"
        onClick={() => (open ? setOpen(false) : openNow())}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={active ? `Profile menu for ${active.name}` : 'Profile menu'}
        className="flex items-center gap-2 outline-none focus-visible:ring-2 focus-visible:ring-white rounded"
      >
        {active ? (
          <ProfileAvatar profile={active} size={32} />
        ) : (
          <div className="flex h-8 w-8 items-center justify-center rounded bg-[#333]">
            <User className="h-5 w-5 text-[#b3b3b3]" />
          </div>
        )}
        <ChevronDown
          className={`hidden h-4 w-4 text-white transition-transform duration-200 sm:block ${open ? 'rotate-180' : ''}`}
        />
      </button>

      {open && (
        <div
          role="menu"
          className="absolute right-0 top-full z-[60] mt-3 w-56 border border-[#333] bg-black/90 py-2 text-white shadow-2xl backdrop-blur-sm"
        >
          {/* Little caret pointing at the avatar */}
          <span
            aria-hidden
            className="absolute -top-2 right-6 h-0 w-0 border-x-8 border-b-8 border-x-transparent border-b-[#e5e5e5]"
          />

          {others.length > 0 && (
            <ul className="pb-1">
              {others.map((p) => (
                <li key={p.id}>
                  <button type="button" role="menuitem" onClick={() => switchTo(p)} className={`group ${itemClass}`}>
                    <ProfileAvatar profile={p} size={32} />
                    <span className="truncate">{p.name}</span>
                  </button>
                </li>
              ))}
            </ul>
          )}

          <Link href="/profiles?manage=1" role="menuitem" onClick={() => setOpen(false)} className={itemClass}>
            <Pencil className="h-5 w-5 text-[#b3b3b3]" />
            Manage Profiles
          </Link>
          <Link href="/settings" role="menuitem" onClick={() => setOpen(false)} className={itemClass}>
            <Settings className="h-5 w-5 text-[#b3b3b3]" />
            Settings
          </Link>

          <div className="mt-2 border-t border-[#333] pt-2">
            <Link
              href="/profiles"
              role="menuitem"
              onClick={() => setOpen(false)}
              className="flex w-full items-center justify-center gap-2 px-3 py-2 text-sm text-white hover:underline"
            >
              <Users className="h-4 w-4 text-[#b3b3b3]" />
              Switch Profile
            </Link>
          </div>
        </div>
      )}
    </div>
  );
}
