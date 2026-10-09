"use client";

import React from "react";
import { usePathname } from "next/navigation";
import { Film, Heart, House, Sparkles, Tv, type LucideIcon } from "lucide-react";
import NavigationLink from "@/shared/navigation/NavigationLink";
import { useT, type MessageKey } from "@/shared/i18n";

const TABS: { label: MessageKey; href: string; icon: LucideIcon }[] = [
  { label: "nav.home", href: "/", icon: House },
  { label: "nav.tvShows", href: "/tv-shows", icon: Tv },
  { label: "nav.movies", href: "/movies", icon: Film },
  { label: "nav.newPopularShort", href: "/new-popular", icon: Sparkles },
  { label: "nav.favorites", href: "/favorites", icon: Heart },
];

const isActive = (pathname: string | null, href: string) =>
  href === "/" ? pathname === "/" : !!pathname && (pathname === href || pathname.startsWith(`${href}/`));

// Phone navigation (below md), like the Netflix app; the top bar keeps search, notifications and profile
export default function BottomTabBar() {
  const t = useT();
  const pathname = usePathname();

  return (
    <nav
      aria-label={t("nav.tabBar")}
      className="nf-tabbar fixed inset-x-0 bottom-0 z-40 border-t border-white/10 bg-[#141414]/95 pb-[env(safe-area-inset-bottom)] pl-[env(safe-area-inset-left)] pr-[env(safe-area-inset-right)] backdrop-blur-md md:hidden"
    >
      <ul className="flex h-14">
        {TABS.map(({ label, href, icon: Icon }) => {
          const active = isActive(pathname, href);
          return (
            <li key={href} className="min-w-0 flex-1">
              <NavigationLink
                href={href}
                aria-current={active ? "page" : undefined}
                onClick={e => {
                  // Re-tapping the current tab scrolls to the top
                  if (pathname === href) {
                    e.preventDefault();
                    window.scrollTo({ top: 0, behavior: "smooth" });
                  }
                }}
                className={`flex h-full flex-col items-center justify-center gap-1 px-1 text-[10px] leading-none ${
                  active ? "font-semibold text-white" : "text-[#9a9a9a]"
                }`}
              >
                <Icon className={`h-[22px] w-[22px] ${active && href === "/favorites" ? "fill-current" : ""}`} strokeWidth={active ? 2.25 : 1.75} aria-hidden />
                <span className="max-w-full truncate">{t(label)}</span>
              </NavigationLink>
            </li>
          );
        })}
      </ul>
    </nav>
  );
}
