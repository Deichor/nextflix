"use client";

import { useEffect, useRef, useState } from 'react';
import { Check } from 'lucide-react';
import ProfileAvatar, { AVATAR_PRESETS, PROFILE_COLORS } from '@/features/profiles/components/ProfileAvatar';
import { getActiveProfileId, setActiveProfile, type Profile } from '@/shared/lib/profile';
import { createProfile, deleteProfile, updateProfile, MAX_PROFILE_NAME } from '@/features/profiles/api';

interface ProfileEditorProps {
  /** null = create a new profile */
  profile: Profile | null;
  /** Total number of existing profiles (deleting the last one is not allowed) */
  profileCount: number;
  onClose: () => void;
  /** Called after a successful save or delete so the caller can reload its list */
  onChanged: () => void;
}

/** Netflix-style full-screen "Edit Profile" / "Add Profile" screen. */
export default function ProfileEditor({ profile, profileCount, onClose, onChanged }: ProfileEditorProps) {
  const isNew = !profile;
  const [name, setName] = useState(profile?.name ?? '');
  const [avatar, setAvatar] = useState(profile?.avatar ?? AVATAR_PRESETS[profileCount % AVATAR_PRESETS.length].key);
  const [color, setColor] = useState(profile?.color ?? PROFILE_COLORS[profileCount % PROFILE_COLORS.length]);
  const [saving, setSaving] = useState(false);
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const inputRef = useRef<HTMLInputElement>(null);

  const trimmed = name.trim();
  const canDelete = !isNew && profileCount > 1;

  useEffect(() => {
    inputRef.current?.focus();
    const onKey = (e: KeyboardEvent) => {
      if (e.key === 'Escape') onClose();
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [onClose]);

  const save = async () => {
    if (!trimmed) {
      setError('Please enter a name.');
      return;
    }
    setSaving(true);
    setError(null);
    try {
      const input = { name: trimmed.slice(0, MAX_PROFILE_NAME), avatar, color };
      if (isNew) {
        await createProfile(input);
      } else {
        const updated = await updateProfile(profile.id, input);
        if (getActiveProfileId() === updated.id) setActiveProfile(updated);
      }
      onChanged();
      onClose();
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Could not save profile.');
    } finally {
      setSaving(false);
    }
  };

  const remove = async () => {
    if (!profile || !canDelete) return;
    setSaving(true);
    setError(null);
    try {
      await deleteProfile(profile.id);
      if (getActiveProfileId() === profile.id) setActiveProfile(null);
      onChanged();
      onClose();
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Could not delete profile.');
      setConfirmDelete(false);
    } finally {
      setSaving(false);
    }
  };

  const buttonBase =
    'px-6 py-2 text-sm md:text-base font-semibold uppercase tracking-[0.15em] transition-colors disabled:opacity-50 disabled:cursor-not-allowed';

  return (
    <div className="fixed inset-0 z-[100] overflow-y-auto bg-[#141414] text-white" role="dialog" aria-modal="true">
      <div className="mx-auto flex min-h-full w-full max-w-3xl flex-col justify-center px-4 py-12 md:px-8">
        <h1 className="text-4xl font-medium md:text-6xl">{isNew ? 'Add Profile' : 'Edit Profile'}</h1>
        {isNew && (
          <p className="mt-2 text-base text-[#808080] md:text-lg">Add a profile for another person watching Nextflix.</p>
        )}

        <div className="mt-6 flex flex-col gap-6 border-y border-[#333] py-6 sm:flex-row">
          <ProfileAvatar profile={{ avatar, color, name: trimmed }} size="clamp(96px, 18vw, 136px)" />

          <div className="flex min-w-0 flex-1 flex-col gap-6">
            <div>
              <input
                ref={inputRef}
                value={name}
                onChange={(e) => setName(e.target.value.slice(0, MAX_PROFILE_NAME))}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') save();
                }}
                maxLength={MAX_PROFILE_NAME}
                placeholder="Name"
                aria-label="Profile name"
                className="w-full bg-[#666] px-3 py-2 text-lg text-white placeholder:text-[#ccc] outline-none focus:ring-2 focus:ring-white"
              />
              <div className="mt-1 flex justify-between text-xs text-[#808080]">
                <span>{error ? <span className="text-[#e87c03]">{error}</span> : ' '}</span>
                <span>
                  {name.length}/{MAX_PROFILE_NAME}
                </span>
              </div>
            </div>

            <section>
              <h2 className="mb-3 text-sm uppercase tracking-widest text-[#b3b3b3]">Avatar</h2>
              <div className="grid grid-cols-5 gap-2 sm:grid-cols-10">
                {AVATAR_PRESETS.map((preset) => {
                  const selected = preset.key === avatar;
                  return (
                    <button
                      key={preset.key}
                      type="button"
                      onClick={() => setAvatar(preset.key)}
                      title={preset.label}
                      aria-label={preset.label}
                      aria-pressed={selected}
                      className={`rounded-[8%] outline-none transition-transform hover:scale-110 focus-visible:ring-2 focus-visible:ring-white ${
                        selected ? 'ring-2 ring-white ring-offset-2 ring-offset-[#141414]' : 'opacity-80 hover:opacity-100'
                      }`}
                    >
                      <ProfileAvatar profile={{ avatar: preset.key, color }} size="100%" style={{ aspectRatio: '1 / 1', height: 'auto' }} />
                    </button>
                  );
                })}
              </div>
            </section>

            <section>
              <h2 className="mb-3 text-sm uppercase tracking-widest text-[#b3b3b3]">Color</h2>
              <div className="flex flex-wrap gap-3">
                {PROFILE_COLORS.map((c) => {
                  const selected = c.toLowerCase() === color.toLowerCase();
                  return (
                    <button
                      key={c}
                      type="button"
                      onClick={() => setColor(c)}
                      aria-label={`Color ${c}`}
                      aria-pressed={selected}
                      className={`flex h-9 w-9 items-center justify-center rounded-full outline-none transition-transform hover:scale-110 focus-visible:ring-2 focus-visible:ring-white ${
                        selected ? 'ring-2 ring-white ring-offset-2 ring-offset-[#141414]' : ''
                      }`}
                      style={{ backgroundColor: c }}
                    >
                      {selected && <Check className="h-5 w-5 text-white drop-shadow" strokeWidth={3} />}
                    </button>
                  );
                })}
              </div>
            </section>
          </div>
        </div>

        {confirmDelete ? (
          <div className="mt-8 flex flex-col gap-4 sm:flex-row sm:items-center">
            <p className="flex-1 text-[#b3b3b3]">
              Delete <span className="font-semibold text-white">{profile?.name}</span>? Its watch history will be gone forever.
            </p>
            <div className="flex gap-3">
              <button
                type="button"
                onClick={remove}
                disabled={saving}
                className={`${buttonBase} bg-[#E50914] text-white hover:bg-[#f40612]`}
              >
                Delete
              </button>
              <button
                type="button"
                onClick={() => setConfirmDelete(false)}
                disabled={saving}
                className={`${buttonBase} border border-[#808080] text-[#808080] hover:border-white hover:text-white`}
              >
                Keep
              </button>
            </div>
          </div>
        ) : (
          <div className="mt-8 flex flex-wrap gap-4">
            <button
              type="button"
              onClick={save}
              disabled={saving || !trimmed}
              className={`${buttonBase} bg-white text-black hover:bg-[#E50914] hover:text-white`}
            >
              {saving ? 'Saving...' : 'Save'}
            </button>
            <button
              type="button"
              onClick={onClose}
              disabled={saving}
              className={`${buttonBase} border border-[#808080] text-[#808080] hover:border-white hover:text-white`}
            >
              Cancel
            </button>
            {!isNew && (
              <button
                type="button"
                onClick={() => setConfirmDelete(true)}
                disabled={saving || !canDelete}
                title={canDelete ? undefined : 'You need at least one profile'}
                className={`${buttonBase} border border-[#808080] text-[#808080] hover:border-white hover:text-white`}
              >
                Delete Profile
              </button>
            )}
          </div>
        )}
      </div>
    </div>
  );
}
