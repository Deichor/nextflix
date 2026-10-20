"use client";

import { useEffect, useSyncExternalStore } from 'react';
import { contentLang, getApiUrl } from '@/shared/lib/api';
import { getActiveProfileId, profileHeaders, PROFILE_EVENT, PROFILE_KEY } from '@/shared/lib/profile';
import type {
  Notification,
  NotificationCountResponse,
  NotificationCounts,
  NotificationResponse,
} from '@/features/notifications/types';

// One shared notification store for the navbar bell and the /notifications page.
// Read/seen state is per profile on the server (X-User-ID). Requests are throttled:
// the badge count is polled at most once a minute and the list is only fetched when shown.

export interface NotificationsState extends NotificationCounts {
  items: Notification[];
  loaded: boolean;
  loading: boolean;
}

const REFRESH_MS = 60_000;
const LIST_LIMIT = 50;

const EMPTY: NotificationsState = { items: [], unseen: 0, unread: 0, total: 0, loaded: false, loading: false };

let state: NotificationsState = EMPTY;
let owner: string | null = null; // profile and content language the state belongs to
let listAt = 0;
let countAt = 0;
let seenUntil = 0; // newest timestamp already sent to /seen
let listReq: Promise<void> | null = null;
let countReq: Promise<void> | null = null;
const listeners = new Set<() => void>();

function set(patch: Partial<NotificationsState>) {
  state = { ...state, ...patch };
  listeners.forEach(fn => fn());
}

function currentOwner(): string {
  return `${getActiveProfileId() ?? ''}:${contentLang()}`;
}

// Drops cached state when the active profile or its language changed (titles are localized)
function syncOwner(): string {
  const id = currentOwner();
  if (id !== owner) {
    owner = id;
    listAt = 0;
    countAt = 0;
    seenUntil = 0;
    listReq = null;
    countReq = null;
    state = EMPTY;
    listeners.forEach(fn => fn());
  }
  return id;
}

const endpoint = (path = '') => `${getApiUrl()}/api/notifications${path}`;

function counts(data: Partial<NotificationCounts> & { count?: number }): Partial<NotificationCounts> {
  const out: Partial<NotificationCounts> = {};
  if (typeof data.unseen === 'number') out.unseen = data.unseen;
  else if (typeof data.count === 'number') out.unseen = data.count;
  if (typeof data.unread === 'number') out.unread = data.unread;
  if (typeof data.total === 'number') out.total = data.total;
  return out;
}

export function refreshNotifications(force = false): Promise<void> {
  const me = syncOwner();
  if (listReq) return listReq;
  if (!force && state.loaded && Date.now() - listAt < REFRESH_MS) return Promise.resolve();
  set({ loading: true });
  const req = (async () => {
    try {
      const res = await fetch(endpoint(`?limit=${LIST_LIMIT}`), { headers: profileHeaders(), cache: 'no-store' });
      if (!res.ok) throw new Error(String(res.status));
      const data: NotificationResponse = await res.json();
      if (me !== owner) return;
      listAt = countAt = Date.now();
      set({ items: Array.isArray(data.notifications) ? data.notifications : [], ...counts(data), loaded: true });
    } catch {
      if (me === owner) set({ loaded: true });
    } finally {
      if (me === owner) listReq = null;
      if (me === owner) set({ loading: false });
    }
  })();
  listReq = req;
  return req;
}

export function refreshNotificationCount(force = false): Promise<void> {
  const me = syncOwner();
  if (countReq) return countReq;
  if (!force && Date.now() - countAt < REFRESH_MS) return Promise.resolve();
  countAt = Date.now(); // also throttles retries while the server is unreachable
  const req = (async () => {
    try {
      const res = await fetch(endpoint('/count'), { headers: profileHeaders(), cache: 'no-store' });
      if (!res.ok) return;
      const data: NotificationCountResponse = await res.json();
      if (me !== owner) return;
      countAt = Date.now();
      const next = counts(data);
      const changed = next.total !== state.total || next.unread !== state.unread || next.unseen !== state.unseen;
      set(next);
      // Something new arrived: refresh the list too, but only if it has been shown before
      if (changed && state.loaded) {
        listAt = 0;
        void refreshNotifications();
      }
    } catch {
      /* notifications are optional */
    } finally {
      if (me === owner) countReq = null;
    }
  })();
  countReq = req;
  return req;
}

async function post(path: string, body?: unknown): Promise<boolean> {
  const me = currentOwner();
  try {
    const res = await fetch(endpoint(path), {
      method: 'POST',
      headers: { 'Content-Type': 'application/json', ...profileHeaders() },
      body: body === undefined ? undefined : JSON.stringify(body),
      cache: 'no-store',
    });
    if (!res.ok) return false;
    const data = await res.json().catch(() => ({}));
    if (me === owner) {
      countAt = Date.now();
      set(counts(data));
    }
    return true;
  } catch {
    return false;
  }
}

// Undoes an optimistic "read" on the current items (not a stale snapshot). Fresher server
// data wins: a list or count refresh that landed meanwhile is left alone.
function rollbackRead(ids: Set<string>, listStamp: number, countStamp: number, countsBefore: Partial<NotificationCounts>) {
  const patch: Partial<NotificationsState> = {};
  if (listAt === listStamp && ids.size) {
    patch.items = state.items.map(n => (ids.has(n.id) && n.read ? { ...n, read: false } : n));
  }
  if (countAt === countStamp) Object.assign(patch, countsBefore);
  if (Object.keys(patch).length) set(patch);
}

export async function markNotificationRead(id: string): Promise<void> {
  const me = syncOwner();
  const item = state.items.find(n => n.id === id);
  if (item?.read) return;
  const listStamp = listAt;
  const countStamp = countAt;
  const before = { unread: state.unread };
  if (item) {
    set({
      items: state.items.map(n => (n.id === id ? { ...n, read: true } : n)),
      unread: Math.max(0, state.unread - 1),
    });
  }
  const ok = await post(`/${encodeURIComponent(id)}/read`);
  if (!ok && me === currentOwner() && me === owner) {
    rollbackRead(new Set(item ? [id] : []), listStamp, countStamp, before);
  }
}

export async function markAllNotificationsRead(): Promise<void> {
  const me = syncOwner();
  const listStamp = listAt;
  const countStamp = countAt;
  const before = { unseen: state.unseen, unread: state.unread };
  const prevSeenUntil = seenUntil;
  const flipped = new Set(state.items.filter(n => !n.read).map(n => n.id));
  seenUntil = Math.max(seenUntil, ...state.items.map(n => n.timestamp || 0));
  const markedUntil = seenUntil;
  set({ items: state.items.map(n => (n.read ? n : { ...n, read: true })), unseen: 0, unread: 0 });
  const ok = await post('/read-all');
  if (!ok && me === currentOwner() && me === owner) {
    // Let markNotificationsSeen post /seen again later
    if (seenUntil === markedUntil) seenUntil = prevSeenUntil;
    rollbackRead(flipped, listStamp, countStamp, before);
  }
}

// Clears the badge up to the newest notification the user has been shown.
// Only posts when newer items are on screen than the last mark, so repeated opens cost nothing.
export async function markNotificationsSeen(): Promise<void> {
  syncOwner();
  const newest = state.items.reduce((m, n) => Math.max(m, n.timestamp || 0), 0);
  if (state.unseen === 0 || newest <= seenUntil) return;
  seenUntil = newest;
  set({ unseen: 0 });
  await post('/seen', { until: newest });
}

// Polling runs once for all subscribers and pauses while the tab is hidden.
let pollTimer: ReturnType<typeof setInterval> | null = null;

function onVisible() {
  if (document.visibilityState === 'visible') void refreshNotificationCount();
}

function onProfileChange() {
  const hadList = state.loaded;
  syncOwner();
  void refreshNotificationCount(true);
  if (hadList) void refreshNotifications(true);
}

// Another tab switched profiles (tabs share localStorage)
function onStorage(e: StorageEvent) {
  if (e.key === PROFILE_KEY || e.key === null) onProfileChange();
}

function startPolling() {
  if (pollTimer || typeof window === 'undefined') return;
  pollTimer = setInterval(() => {
    if (document.visibilityState === 'visible') void refreshNotificationCount();
  }, REFRESH_MS);
  document.addEventListener('visibilitychange', onVisible);
  window.addEventListener(PROFILE_EVENT, onProfileChange);
  window.addEventListener('storage', onStorage);
}

function stopPolling() {
  if (!pollTimer) return;
  clearInterval(pollTimer);
  pollTimer = null;
  document.removeEventListener('visibilitychange', onVisible);
  window.removeEventListener(PROFILE_EVENT, onProfileChange);
  window.removeEventListener('storage', onStorage);
}

function subscribe(fn: () => void) {
  listeners.add(fn);
  startPolling();
  return () => {
    listeners.delete(fn);
    if (listeners.size === 0) stopPolling();
  };
}

const getSnapshot = () => state;
const getServerSnapshot = () => EMPTY;

/** Shared notification state; `withList` also loads the list (the page), otherwise only the badge count. */
export function useNotifications(withList = false): NotificationsState {
  const snapshot = useSyncExternalStore(subscribe, getSnapshot, getServerSnapshot);
  useEffect(() => {
    // The list response carries the counts too, so the page needs only one request
    if (withList) void refreshNotifications();
    else void refreshNotificationCount();
  }, [withList]);
  return snapshot;
}
