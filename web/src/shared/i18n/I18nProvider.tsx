"use client";

import { createContext, useContext, useEffect, useMemo, useSyncExternalStore, type ReactNode } from 'react';
import type { MessageKey } from '@/shared/i18n/en';
import {
  DEFAULT_LOCALE,
  LANG_EVENT,
  LANG_STORAGE_KEY,
  detectLocale,
  formatDate,
  formatDuration,
  formatNumber,
  formatRelativeTime,
  formatShortDate,
  translate,
  translateParts,
  type Locale,
  type Vars,
} from '@/shared/i18n/core';
import { getActiveProfile, PROFILE_EVENT } from '@/shared/lib/profile';
import { installLangFetch } from '@/shared/lib/api';

// Every backend fetch carries the content language (X-Lang); runs once, before any effect fetches
installLangFetch();

export type TFunction = ((key: MessageKey, vars?: Vars) => string) & {
  /** Placeholders can be React nodes, e.g. t.rich('profiles.deleteConfirm', { name: <b>{n}</b> }) */
  rich: (key: MessageKey, vars: Record<string, ReactNode>) => ReactNode[];
};

function subscribe(onChange: () => void) {
  window.addEventListener(PROFILE_EVENT, onChange);
  window.addEventListener(LANG_EVENT, onChange);
  window.addEventListener('storage', onChange);
  return () => {
    window.removeEventListener(PROFILE_EVENT, onChange);
    window.removeEventListener(LANG_EVENT, onChange);
    window.removeEventListener('storage', onChange);
  };
}

// The server and the hydration pass render English; React re-renders right after with the client locale
const serverLocale = () => DEFAULT_LOCALE;

const LocaleContext = createContext<Locale>(DEFAULT_LOCALE);

export function I18nProvider({ children }: { children: ReactNode }) {
  const locale = useSyncExternalStore(subscribe, detectLocale, serverLocale);

  useEffect(() => {
    document.documentElement.lang = locale;
    // Remember the last profile's language for pages without a profile (the picker)
    if (getActiveProfile()?.language === locale) {
      try {
        window.localStorage.setItem(LANG_STORAGE_KEY, locale);
      } catch {
        /* storage unavailable */
      }
    }
  }, [locale]);

  return <LocaleContext.Provider value={locale}>{children}</LocaleContext.Provider>;
}

export function useLocale(): Locale {
  return useContext(LocaleContext);
}

export function useT(): TFunction {
  const locale = useLocale();
  return useMemo(() => {
    const t = ((key: MessageKey, vars?: Vars) => translate(locale, key, vars)) as TFunction;
    t.rich = (key, vars) => translateParts<ReactNode>(locale, key, vars);
    return t;
  }, [locale]);
}

/** Intl format helpers bound to the current locale. */
export function useFormat() {
  const locale = useLocale();
  return useMemo(
    () => ({
      locale,
      number: (n: number, opts?: Intl.NumberFormatOptions) => formatNumber(locale, n, opts),
      date: (d: Date | string | number, opts?: Intl.DateTimeFormatOptions) => formatDate(locale, d, opts),
      shortDate: (d: Date | string | number) => formatShortDate(locale, d),
      relativeTime: (d: Date | string | number, now?: number) => formatRelativeTime(locale, d, now),
      duration: (minutes: number) => formatDuration(locale, minutes),
    }),
    [locale],
  );
}
