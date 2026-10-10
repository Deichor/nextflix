/** A list loaded for one profile */
export interface ProfileList<T> {
  profileId: number;
  list: T[];
}

/** The list of the active profile; another profile's counts as empty. */
export function listFor<T>(loaded: ProfileList<T> | null, profileId: number | null, empty: T[]): T[] {
  return loaded && loaded.profileId === profileId ? loaded.list : empty;
}

/** A finished load replaces the list only if its profile is still the active one. */
export function storeLoad<T>(prev: ProfileList<T> | null, next: ProfileList<T>, activeId: number | null): ProfileList<T> | null {
  return next.profileId === activeId ? next : prev;
}
