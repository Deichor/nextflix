import { getApiUrl } from '@/shared/lib/api';
import type { Profile } from '@/shared/lib/profile';

export const MAX_PROFILES = 5;
export const MAX_PROFILE_NAME = 20;

export type ProfileInput = Pick<Profile, 'name' | 'avatar' | 'color'> & Pick<Partial<Profile>, 'language'>;

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${getApiUrl()}${path}`, {
    ...init,
    headers: { 'Content-Type': 'application/json', ...(init?.headers || {}) },
    cache: 'no-store',
  });
  if (!res.ok) {
    let message = `Request failed (${res.status})`;
    try {
      const body = await res.json();
      if (body?.error) message = body.error;
    } catch {
      /* non-JSON error body */
    }
    throw new Error(message);
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

export async function listProfiles(signal?: AbortSignal): Promise<Profile[]> {
  const data = await request<Profile[] | null>('/api/profiles', { signal });
  return Array.isArray(data) ? data : [];
}

export function createProfile(input: ProfileInput): Promise<Profile> {
  return request<Profile>('/api/profiles', { method: 'POST', body: JSON.stringify(input) });
}

export function updateProfile(id: number, input: Partial<ProfileInput>): Promise<Profile> {
  return request<Profile>(`/api/profiles/${id}`, { method: 'PUT', body: JSON.stringify(input) });
}

export function deleteProfile(id: number): Promise<void> {
  return request<void>(`/api/profiles/${id}`, { method: 'DELETE' });
}
