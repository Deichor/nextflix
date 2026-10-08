"use client";

import { useState } from 'react';
import { Eye, EyeOff } from 'lucide-react';
import { useT } from '@/shared/i18n';
import { inputClass } from '@/features/settings/components/ui';

// A stored secret is never sent back: the field stays empty and only a new value or "Remove" changes it.
export default function SecretField({
  label,
  hint,
  isSet,
  value,
  onChange,
  cleared,
  onClear,
}: {
  label: string;
  hint?: string;
  isSet: boolean;
  value: string;
  onChange: (v: string) => void;
  cleared: boolean;
  onClear: (v: boolean) => void;
}) {
  const t = useT();
  const [show, setShow] = useState(false);
  const stored = isSet && !cleared;
  return (
    <label className="block">
      <span className="mb-1.5 flex items-center justify-between gap-3 text-sm text-[#b3b3b3]">
        <span>{label}</span>
        <span className={stored ? 'text-[#46d369]' : 'text-[#8c8c8c]'}>
          {stored ? t('settings.subtitles.saved') : t('settings.subtitles.notSet')}
        </span>
      </span>
      <div className="flex gap-2">
        <div className="relative flex-1">
          <input
            type={show ? 'text' : 'password'}
            value={value}
            onChange={(e) => {
              onChange(e.target.value);
              if (e.target.value) onClear(false);
            }}
            placeholder={stored ? t('settings.subtitles.storedPlaceholder') : ''}
            autoComplete="new-password"
            spellCheck={false}
            className={`${inputClass} pr-10 font-mono`}
          />
          <button
            type="button"
            onClick={() => setShow((v) => !v)}
            className="absolute inset-y-0 right-0 flex w-10 items-center justify-center text-[#b3b3b3] hover:text-white"
            aria-label={show ? t('settings.subtitles.hide') : t('settings.subtitles.show')}
          >
            {show ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
          </button>
        </div>
        {stored && (
          <button
            type="button"
            onClick={() => {
              onChange('');
              onClear(true);
            }}
            className="rounded bg-white/10 px-3 text-sm text-white hover:bg-white/20"
          >
            {t('settings.subtitles.remove')}
          </button>
        )}
      </div>
      {hint && <span className="mt-1.5 block text-xs text-[#8c8c8c]">{hint}</span>}
    </label>
  );
}
