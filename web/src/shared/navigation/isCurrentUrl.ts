/** True when href resolves to the current pathname + search (hash ignored). */
export const isCurrentUrl = (href: string): boolean => {
  if (typeof window === 'undefined') return false;
  try {
    const target = new URL(href, window.location.href);
    const here = window.location;
    return target.origin === here.origin
      && target.pathname === here.pathname
      && target.search === here.search;
  } catch {
    return false;
  }
};
