"use client";

import type { ReactNode } from 'react';
import { CheckCircle2, Loader2, XCircle } from 'lucide-react';
import { useT } from '@/shared/i18n';

export type Status = { kind: 'ok' | 'error'; text: string } | null;
export type ConnectionState = 'connected' | 'notSet' | 'error' | 'checking';

export const inputClass =
  'w-full rounded bg-[#333] px-3 py-2.5 text-sm text-white outline-none placeholder:text-[#8c8c8c] focus:ring-2 focus:ring-white/70';

export const primaryButton =
  'inline-flex items-center justify-center gap-2 rounded bg-[#E50914] px-5 py-2 text-sm pointer-coarse:min-h-11 font-semibold text-white transition-colors hover:bg-[#f40612] disabled:cursor-not-allowed disabled:opacity-50';

export const secondaryButton =
  'inline-flex items-center justify-center gap-2 rounded bg-white/15 px-5 py-2 text-sm pointer-coarse:min-h-11 font-semibold text-white transition-colors hover:bg-white/25 disabled:cursor-not-allowed disabled:opacity-50';

export function StatusLine({ status }: { status: Status }) {
  if (!status) return null;
  const ok = status.kind === 'ok';
  const Icon = ok ? CheckCircle2 : XCircle;
  return (
    <p className={`flex items-start gap-2 text-sm ${ok ? 'text-[#46d369]' : 'text-[#e87c03]'}`} role="status">
      <Icon className="mt-0.5 h-4 w-4 shrink-0" />
      <span>{status.text}</span>
    </p>
  );
}

export function LoadingLine() {
  const t = useT();
  return (
    <div className="flex items-center gap-2 text-sm text-[#b3b3b3]">
      <Loader2 className="h-4 w-4 animate-spin" /> {t('common.loading')}
    </div>
  );
}

const BADGE: Record<ConnectionState, { cls: string; dot: string; key: 'settings.status.connected' | 'settings.status.notSet' | 'settings.status.error' | 'settings.status.checking' }> = {
  connected: { cls: 'bg-[#46d369]/15 text-[#46d369]', dot: 'bg-[#46d369]', key: 'settings.status.connected' },
  notSet: { cls: 'bg-white/10 text-[#b3b3b3]', dot: 'bg-[#8c8c8c]', key: 'settings.status.notSet' },
  error: { cls: 'bg-[#E50914]/15 text-[#ff6b6b]', dot: 'bg-[#E50914]', key: 'settings.status.error' },
  checking: { cls: 'bg-white/10 text-[#b3b3b3]', dot: 'bg-[#b3b3b3] animate-pulse', key: 'settings.status.checking' },
};

export function StatusBadge({ state, title }: { state: ConnectionState; title?: string }) {
  const t = useT();
  const b = BADGE[state];
  return (
    <span
      className={`inline-flex shrink-0 items-center gap-1.5 whitespace-nowrap rounded-full px-2.5 py-1 text-xs font-semibold ${b.cls}`}
      title={title}
    >
      <span className={`h-1.5 w-1.5 rounded-full ${b.dot}`} aria-hidden />
      {t(b.key)}
    </span>
  );
}

// Card on the advanced page: title, one-line explanation and status in the header
export function SettingsCard({
  title,
  summary,
  badge,
  children,
}: {
  title: string;
  summary: string;
  badge?: ReactNode;
  children: ReactNode;
}) {
  return (
    <section className="overflow-hidden rounded-md border border-white/10 bg-[#181818]">
      <header className="flex items-start justify-between gap-4 border-b border-white/10 px-5 py-4 md:px-6">
        <div className="min-w-0">
          <h2 className="text-lg font-semibold">{title}</h2>
          <p className="mt-0.5 text-sm text-[#b3b3b3]">{summary}</p>
        </div>
        {badge}
      </header>
      <div className="px-5 py-5 md:px-6 md:py-6">{children}</div>
    </section>
  );
}

// Section of the main page, Netflix account style: heading on the left, content on the right
export function SettingsSection({ title, children }: { title: string; children: ReactNode }) {
  return (
    <section className="grid gap-4 border-t border-white/15 py-8 md:grid-cols-[13rem_1fr] md:gap-8">
      <h2 className="text-base font-medium uppercase tracking-wide text-[#8c8c8c] md:pt-1">{title}</h2>
      <div className="min-w-0">{children}</div>
    </section>
  );
}

// One preference: label and hint, control below on mobile and to the right on desktop
export function SettingRow({
  label,
  hint,
  id,
  children,
}: {
  label: string;
  hint?: string;
  id?: string;
  children: ReactNode;
}) {
  return (
    <div className="flex flex-col gap-3 border-b border-white/10 py-5 first:pt-0 last:border-b-0 last:pb-0 sm:flex-row sm:items-center sm:justify-between sm:gap-6">
      <div className="min-w-0">
        <p id={id} className="text-[15px] font-medium text-white">
          {label}
        </p>
        {hint && <p className="mt-0.5 text-sm text-[#8c8c8c]">{hint}</p>}
      </div>
      <div className="shrink-0">{children}</div>
    </div>
  );
}

export interface SegmentOption<V extends string> {
  value: V;
  label: string;
}

export function Segmented<V extends string>({
  value,
  options,
  onChange,
  labelledBy,
  disabled,
}: {
  value: V | null;
  options: SegmentOption<V>[];
  onChange: (v: V) => void;
  labelledBy?: string;
  disabled?: boolean;
}) {
  return (
    <div role="radiogroup" aria-labelledby={labelledBy} className="inline-flex shrink-0 flex-nowrap gap-1 self-start whitespace-nowrap rounded-md bg-[#2a2a2a] p-1 sm:self-auto">
      {options.map((o) => {
        const active = o.value === value;
        return (
          <button
            key={o.value}
            type="button"
            role="radio"
            aria-checked={active}
            disabled={disabled}
            onClick={() => !active && onChange(o.value)}
            className={`min-w-[4.5rem] rounded px-3 py-1.5 text-sm font-medium pointer-coarse:min-h-11 transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/70 disabled:cursor-not-allowed disabled:opacity-60 ${
              active ? 'bg-white text-black' : 'text-[#d2d2d2] hover:bg-white/10 hover:text-white'
            }`}
          >
            {o.label}
          </button>
        );
      })}
    </div>
  );
}

export function Toggle({
  checked,
  onChange,
  labelledBy,
}: {
  checked: boolean;
  onChange: (v: boolean) => void;
  labelledBy?: string;
}) {
  return (
    <button
      type="button"
      role="switch"
      aria-checked={checked}
      aria-labelledby={labelledBy}
      onClick={() => onChange(!checked)}
      className={`relative inline-flex h-7 w-12 items-center rounded-full transition-colors pointer-coarse:before:absolute pointer-coarse:before:-inset-2 pointer-coarse:before:content-[''] focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-white/70 ${
        checked ? 'bg-[#E50914]' : 'bg-[#4d4d4d]'
      }`}
    >
      <span
        className={`inline-block h-5 w-5 rounded-full bg-white shadow transition-transform ${
          checked ? 'translate-x-6' : 'translate-x-1'
        }`}
      />
    </button>
  );
}
