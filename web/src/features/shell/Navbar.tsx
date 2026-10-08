"use client";

import React, { useCallback, useEffect, useRef, useState } from "react";
import { Bell, ChevronDown, Film, Search, Tv, X } from "lucide-react";
import { usePathname } from "next/navigation";
import NavigationLink from "@/shared/navigation/NavigationLink";
import NotificationDropdown from "@/features/notifications/components/NotificationDropdown";
import ProfileMenu from "@/features/profiles/components/ProfileMenu";
import { useNavigate } from "@/shared/navigation/useNavigate";
import { getApiUrl } from "@/shared/lib/api";
import { useNotifications } from "@/features/notifications/store";
import { useT, type MessageKey } from "@/shared/i18n";

interface NavbarProps {
  onSearch?: (query: string) => void;
}

interface TMDBSearchResult {
  id: number;
  title: string;
  overview: string;
  release_date: string;
  poster_path: string;
  vote_average: number;
  popularity: number;
  media_type: "movie" | "tv";
}

interface NavItem {
  label: MessageKey;
  href: string;
}

const MAIN_LINKS: NavItem[] = [
  { label: "nav.home", href: "/" },
  { label: "nav.tvShows", href: "/tv-shows" },
  { label: "nav.movies", href: "/movies" },
  { label: "nav.newPopular", href: "/new-popular" },
  { label: "nav.favorites", href: "/favorites" },
];

const SCROLL_SOLID_AT = 60;

const isActive = (pathname: string | null, href: string) =>
  href === "/" ? pathname === "/" : !!pathname && (pathname === href || pathname.startsWith(`${href}/`));

const yearOf = (date: string) => {
  const y = date ? new Date(date).getFullYear() : NaN;
  return Number.isFinite(y) ? String(y) : "";
};

// Closes a popover when clicking outside of `ref`
function useOutsideClose(ref: React.RefObject<HTMLElement | null>, open: boolean, close: () => void) {
  useEffect(() => {
    if (!open) return;
    const onDown = (e: MouseEvent) => {
      if (ref.current && !ref.current.contains(e.target as Node)) close();
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [ref, open, close]);
}

const Navbar: React.FC<NavbarProps> = ({ onSearch }) => {
  const t = useT();
  const navigate = useNavigate();
  const pathname = usePathname();
  const [isScrolled, setIsScrolled] = useState(false);

  const [isBrowseOpen, setIsBrowseOpen] = useState(false);
  const browseRef = useRef<HTMLDivElement>(null);

  const [isSearchOpen, setIsSearchOpen] = useState(false);
  const [searchQuery, setSearchQuery] = useState("");
  const [suggestions, setSuggestions] = useState<TMDBSearchResult[]>([]);
  const [isLoadingSuggestions, setIsLoadingSuggestions] = useState(false);
  const searchRef = useRef<HTMLDivElement>(null);
  const searchInputRef = useRef<HTMLInputElement>(null);

  const { unseen: notificationCount } = useNotifications();
  const [isNotificationOpen, setIsNotificationOpen] = useState(false);
  const notificationRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const onScroll = () => setIsScrolled(window.scrollY > SCROLL_SOLID_AT);
    onScroll();
    window.addEventListener("scroll", onScroll, { passive: true });
    return () => window.removeEventListener("scroll", onScroll);
  }, []);

  useEffect(() => {
    setIsBrowseOpen(false);
    setIsNotificationOpen(false);
  }, [pathname]);

  const closeBrowse = useCallback(() => setIsBrowseOpen(false), []);
  const closeNotifications = useCallback(() => setIsNotificationOpen(false), []);
  const closeSearch = useCallback(() => {
    setIsSearchOpen(false);
    setSuggestions([]);
    setSearchQuery("");
  }, []);
  const closeSearchIfEmpty = useCallback(() => {
    if (!searchQuery.trim()) closeSearch();
    else setSuggestions([]);
  }, [searchQuery, closeSearch]);

  useOutsideClose(browseRef, isBrowseOpen, closeBrowse);
  useOutsideClose(notificationRef, isNotificationOpen, closeNotifications);
  useOutsideClose(searchRef, isSearchOpen, closeSearchIfEmpty);

  // Debounced TMDB suggestions
  useEffect(() => {
    const q = searchQuery.trim();
    if (q.length < 2) {
      setSuggestions([]);
      return;
    }
    const ctrl = new AbortController();
    const timer = setTimeout(async () => {
      setIsLoadingSuggestions(true);
      try {
        const res = await fetch(`${getApiUrl()}/api/tmdb/suggestions?q=${encodeURIComponent(q)}`, { signal: ctrl.signal });
        const data = res.ok ? await res.json() : null;
        const results: TMDBSearchResult[] = Array.isArray(data?.results) ? data.results : [];
        results.sort((a, b) => (b.popularity || 0) - (a.popularity || 0));
        setSuggestions(results);
      } catch (e: any) {
        if (e?.name !== "AbortError") setSuggestions([]);
      } finally {
        if (!ctrl.signal.aborted) setIsLoadingSuggestions(false);
      }
    }, 300);
    return () => {
      clearTimeout(timer);
      ctrl.abort();
    };
  }, [searchQuery]);

  const openSearch = () => {
    setIsSearchOpen(true);
    setTimeout(() => searchInputRef.current?.focus(), 0);
  };

  const submitSearch = (e: React.FormEvent) => {
    e.preventDefault();
    const q = searchQuery.trim();
    if (!q) return;
    if (onSearch) onSearch(q);
    else navigate.push(`/search?q=${encodeURIComponent(q)}`);
    setSuggestions([]);
  };

  const pickSuggestion = (s: TMDBSearchResult) => {
    navigate.push(`/tmdb-movie/${s.id}?type=${s.media_type}`);
    closeSearch();
  };

  // Re-clicking the current page only scrolls up (a same-path push would leave the route loader spinning)
  const linkClick = (href: string, after?: () => void) => (e: React.MouseEvent<HTMLDivElement>) => {
    if (pathname === href) {
      e.preventDefault();
      window.scrollTo({ top: 0, behavior: "smooth" });
    }
    after?.();
  };

  const linkClass = (href: string) =>
    `text-sm transition-colors whitespace-nowrap ${isActive(pathname, href) ? "text-white font-bold" : "text-[#e5e5e5] hover:text-[#b3b3b3]"}`;

  const menuLink = (item: NavItem, onClick?: () => void) => (
    <NavigationLink
      key={item.href}
      href={item.href}
      onClick={linkClick(item.href, onClick)}
      className={`block w-full px-4 py-2.5 text-center text-sm transition-colors hover:bg-white/10 ${isActive(pathname, item.href) ? "text-white font-bold" : "text-[#b3b3b3] hover:text-white"}`}
    >
      {t(item.label)}
    </NavigationLink>
  );

  const showSuggestions = isSearchOpen && searchQuery.trim().length >= 2;

  return (
    <nav
      className={`fixed top-0 inset-x-0 z-40 transition-colors duration-500 ${
        isScrolled ? "bg-[#141414] shadow-[0_2px_10px_rgba(0,0,0,0.4)]" : "bg-gradient-to-b from-black/80 via-black/40 to-transparent"
      }`}
    >
      <div className="flex items-center h-16 md:h-[68px] px-4 md:px-12">
        {/* Logo */}
        <NavigationLink
          href="/"
          onClick={linkClick("/")}
          className="shrink-0 text-[#E50914] font-black tracking-tight text-[1.45rem] md:text-[1.9rem] leading-none select-none drop-shadow-[0_1px_2px_rgba(0,0,0,0.5)]"
        >
          NEXTFLIX
        </NavigationLink>

        {/* Desktop links */}
        <ul className="hidden lg:flex items-center gap-5 ml-10">
          {MAIN_LINKS.map(item => (
            <li key={item.href}>
              <NavigationLink href={item.href} onClick={linkClick(item.href)} className={linkClass(item.href)}>
                {t(item.label)}
              </NavigationLink>
            </li>
          ))}
        </ul>

        {/* Mobile "Browse" menu */}
        <div className={`relative lg:hidden ml-4 ${isSearchOpen ? "hidden sm:block" : ""}`} ref={browseRef}>
          <button
            onClick={() => setIsBrowseOpen(o => !o)}
            className="flex items-center gap-1 text-sm font-semibold text-white"
            aria-expanded={isBrowseOpen}
          >
            {t("nav.browse")}
            <ChevronDown className={`w-4 h-4 transition-transform ${isBrowseOpen ? "rotate-180" : ""}`} />
          </button>
          {isBrowseOpen && (
            <div className="absolute left-1/2 -translate-x-1/2 top-full mt-4 w-60 max-h-[75vh] overflow-y-auto bg-black/90 border border-[#333] border-t-2 border-t-white/80 py-1 z-[60]">
              {MAIN_LINKS.map(item => menuLink(item, closeBrowse))}
            </div>
          )}
        </div>

        {/* Right group */}
        <div className="ml-auto flex items-center gap-3 md:gap-5">
          {/* Search */}
          <div className="relative" ref={searchRef}>
            {isSearchOpen ? (
              <form
                onSubmit={submitSearch}
                className="flex items-center gap-2 h-9 px-2 bg-black/80 border border-white/90"
              >
                <Search className="w-5 h-5 text-white shrink-0" />
                <input
                  ref={searchInputRef}
                  type="text"
                  value={searchQuery}
                  onChange={e => setSearchQuery(e.target.value)}
                  onKeyDown={e => { if (e.key === "Escape") closeSearch(); }}
                  placeholder={t("nav.searchPlaceholder")}
                  className="w-[36vw] sm:w-[200px] lg:w-[240px] bg-transparent text-white text-sm outline-none placeholder:text-[#8c8c8c]"
                  aria-label={t("nav.search")}
                />
                <button type="button" onClick={closeSearch} className="text-white/70 hover:text-white" aria-label={t("nav.closeSearch")}>
                  <X className="w-4 h-4" />
                </button>
              </form>
            ) : (
              <button onClick={openSearch} className="flex items-center text-white hover:text-[#b3b3b3] transition-colors" aria-label={t("nav.search")}>
                <Search className="w-5 h-5 md:w-6 md:h-6" />
              </button>
            )}

            {showSuggestions && (
              <div className="absolute right-0 top-full mt-2 w-[min(380px,92vw)] max-h-[70vh] overflow-y-auto bg-black/95 border border-[#333] shadow-2xl z-[60]">
                {isLoadingSuggestions && suggestions.length === 0 ? (
                  <div className="flex items-center justify-center gap-2 p-4 text-sm text-[#b3b3b3]">
                    <span className="w-4 h-4 border-2 border-[#E50914] border-t-transparent rounded-full animate-spin" />
                    {t("nav.searching")}
                  </div>
                ) : suggestions.length > 0 ? (
                  <div className="py-1">
                    {suggestions.slice(0, 6).map(s => (
                      <button
                        key={`${s.media_type}-${s.id}`}
                        onClick={() => pickSuggestion(s)}
                        className="w-full flex items-center gap-3 px-3 py-2 text-left hover:bg-white/10 transition-colors"
                      >
                        <div className="w-10 h-14 shrink-0 rounded-sm overflow-hidden bg-[#2f2f2f] flex items-center justify-center">
                          {s.poster_path ? (
                            <img src={`/tmdb-img/t/p/w92${s.poster_path}`} alt="" loading="lazy" className="w-full h-full object-cover" />
                          ) : s.media_type === "tv" ? (
                            <Tv className="w-4 h-4 text-[#8c8c8c]" />
                          ) : (
                            <Film className="w-4 h-4 text-[#8c8c8c]" />
                          )}
                        </div>
                        <div className="min-w-0 flex-1">
                          <p className="text-white text-sm font-semibold truncate">{s.title}</p>
                          <p className="text-xs text-[#b3b3b3]">
                            {s.media_type === "tv" ? t("common.tvShow") : t("common.movie")}
                            {!!yearOf(s.release_date) && ` · ${yearOf(s.release_date)}`}
                            {s.vote_average > 0 && ` · ★ ${s.vote_average.toFixed(1)}`}
                          </p>
                        </div>
                      </button>
                    ))}
                    <button
                      onClick={() => {
                        navigate.push(`/search?q=${encodeURIComponent(searchQuery.trim())}`);
                        setSuggestions([]);
                      }}
                      className="w-full px-3 py-2.5 text-left text-sm text-white/80 hover:text-white hover:bg-white/10 border-t border-[#333]"
                    >
                      {t("nav.seeAllResults", { query: searchQuery.trim() })}
                    </button>
                  </div>
                ) : !isLoadingSuggestions ? (
                  <div className="p-4 text-center text-sm text-[#b3b3b3]">{t("nav.noResults", { query: searchQuery.trim() })}</div>
                ) : null}
              </div>
            )}
          </div>

          {/* Notifications */}
          <div className={`relative ${isSearchOpen ? "hidden sm:block" : ""}`} ref={notificationRef}>
            <button
              onClick={() => setIsNotificationOpen(o => !o)}
              className="relative flex items-center text-white hover:text-[#b3b3b3] transition-colors"
              aria-label={notificationCount > 0 ? t("nav.notificationsNew", { count: notificationCount }) : t("nav.notifications")}
              aria-expanded={isNotificationOpen}
            >
              <Bell className="w-5 h-5 md:w-6 md:h-6" />
              {notificationCount > 0 && (
                <span className="absolute -top-1.5 -right-1.5 min-w-[16px] h-4 px-1 rounded-full bg-[#E50914] text-[10px] font-bold leading-4 text-center text-white">
                  {notificationCount > 9 ? "9+" : notificationCount}
                </span>
              )}
            </button>
            <NotificationDropdown isOpen={isNotificationOpen} onClose={closeNotifications} />
          </div>

          <ProfileMenu />
        </div>
      </div>
    </nav>
  );
};

export default Navbar;
