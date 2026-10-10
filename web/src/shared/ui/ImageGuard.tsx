"use client";

import { useEffect } from "react";
import { createFailCounter } from "@/shared/ui/imageFailCounter";

const TMDB = "https://image.tmdb.org/";
const PROXY = "/tmdb-img/";
const MAX_FAILS = 2;

// Routes TMDB images (including URLs that come from the backend) through the
// frontend proxy, and stops onError fallbacks that retry a broken URL forever.
// Style updates are not observed (they fire on every animation frame); the <head>
// script in app/layout.tsx already patches the style setters, and new nodes are scanned.
export default function ImageGuard() {
  useEffect(() => {
    const fails = createFailCounter(MAX_FAILS);

    const proxify = (el: Element) => {
      const src = el.getAttribute("src");
      if (src?.startsWith(TMDB)) el.setAttribute("src", PROXY + src.slice(TMDB.length));
      const srcset = el.getAttribute("srcset");
      if (srcset?.includes(TMDB)) el.setAttribute("srcset", srcset.split(TMDB).join(PROXY));
      const style = el.getAttribute("style");
      if (style?.includes(TMDB)) el.setAttribute("style", style.split(TMDB).join(PROXY));
    };

    const scan = (root: Element) => {
      proxify(root);
      root.querySelectorAll("[src], [srcset], [style]").forEach(proxify);
    };

    const onLoad = (e: Event) => {
      if (e.target instanceof HTMLImageElement) fails.loaded(e.target);
    };

    const onError = (e: Event) => {
      const img = e.target;
      if (!(img instanceof HTMLImageElement)) return;
      if (fails.fail(img)) {
        // Runs in the capture phase, so the component's own onError never fires
        e.stopImmediatePropagation();
        img.removeAttribute("srcset");
        img.removeAttribute("src");
      }
    };

    scan(document.body);
    const observer = new MutationObserver(mutations => {
      for (const m of mutations) {
        if (m.type === "attributes") proxify(m.target as Element);
        m.addedNodes.forEach(n => { if (n instanceof Element) scan(n); });
      }
    });
    observer.observe(document.body, { subtree: true, childList: true, attributes: true, attributeFilter: ["src", "srcset"] });
    window.addEventListener("error", onError, true);
    window.addEventListener("load", onLoad, true);

    return () => {
      observer.disconnect();
      window.removeEventListener("error", onError, true);
      window.removeEventListener("load", onLoad, true);
    };
  }, []);

  return null;
}
