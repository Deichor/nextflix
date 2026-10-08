import { useSyncExternalStore } from 'react';

// The title shown in a rows screen's hero (the focused card). A tiny store, so a focus change
// re-renders only the hero and not every row.

export interface HeroItem {
  key: string;
  title: string;
  overview: string;
  backdrop: string;
  /** small facts under the title: year, match, "S1:E2" */
  facts: string[];
  kicker?: string;
}

let current: HeroItem | null = null;
const listeners = new Set<() => void>();
let timer: ReturnType<typeof setTimeout> | undefined;

export function setHero(item: HeroItem | null, immediate = false): void {
  if (timer) clearTimeout(timer);
  const apply = () => {
    if (current?.key === item?.key) return;
    current = item;
    listeners.forEach(fn => fn());
  };
  // Applied once focus rests: holding a direction key (repeats come faster) never updates the hero
  if (immediate) apply();
  else timer = setTimeout(apply, 350);
}

const subscribe = (fn: () => void) => {
  listeners.add(fn);
  return () => { listeners.delete(fn); };
};

export const useHero = () => useSyncExternalStore(subscribe, () => current, () => current);
