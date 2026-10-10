"use client";

import React, { createContext, useCallback, useContext, useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { ImageKind, tmdbDefaultSrc, tmdbPath, tmdbPlaceholder, tmdbSrcSet } from '@/shared/lib/image';

// Start loading well before an image scrolls into view
const VIEWPORT_MARGIN = '150% 0px';
const ROW_MARGIN = '0px 150%';
const BLUR_DROP_MS = 400;
// Old browsers without IntersectionObserver load everything right away
const NO_OBSERVER = typeof window !== 'undefined' && typeof IntersectionObserver === 'undefined';

interface RowGate {
  /** The row is within VIEWPORT_MARGIN of the viewport */
  near: boolean;
  /** The row's horizontal scroller; cards observe against it */
  root: Element | null;
}

const RowGateContext = createContext<RowGate | null>(null);

/** Becomes true (and stays true) once `el` comes within `margin` of `root` (null = viewport). */
export function useNearViewport(
  el: Element | null,
  enabled = true,
  margin = VIEWPORT_MARGIN,
  root: Element | null = null,
): boolean {
  const [near, setNear] = useState(false);
  useEffect(() => {
    if (!enabled || near || !el || NO_OBSERVER) return;
    const io = new IntersectionObserver(entries => {
      if (entries.some(e => e.isIntersecting)) {
        setNear(true);
        io.disconnect();
      }
    }, { root, rootMargin: margin });
    io.observe(el);
    return () => io.disconnect();
  }, [el, enabled, near, margin, root]);
  // el is only set after mount, so the server and hydration pass still render "not near"
  return near || (NO_OBSERVER && enabled && !!el);
}

/**
 * For horizontal rows: images inside load once the row is near the viewport and the card is
 * within 1.5 row widths of the visible part. Put `sectionRef` on the row and `scrollerRef`
 * on the scroller, and wrap the cards in <ImageRowGate value={gate.value}>.
 */
export function useImageRowGate<T extends HTMLElement>(scrollRef?: React.RefObject<T | null>) {
  const [section, sectionRef] = useState<HTMLElement | null>(null);
  const [root, setRoot] = useState<T | null>(null);
  const near = useNearViewport(section);
  const scrollerRef = useCallback((el: T | null) => {
    if (scrollRef) scrollRef.current = el;
    setRoot(el);
  }, [scrollRef]);
  const value = useMemo<RowGate>(() => ({ near, root }), [near, root]);
  return { sectionRef, scrollerRef, value };
}

export function ImageRowGate({ value, children }: { value: RowGate; children: React.ReactNode }) {
  return <RowGateContext.Provider value={value}>{children}</RowGateContext.Provider>;
}

/** For card extras (badges): true once the card's row is near the viewport, or `el` itself outside a row. */
export function useRowNear(el: Element | null): boolean {
  const row = useContext(RowGateContext);
  const near = useNearViewport(el, !row);
  return row ? row.near : near;
}

interface TmdbImageProps {
  /** TMDB path ("/abc.jpg"), /tmdb-img URL or image.tmdb.org URL; other URLs are used as-is */
  src?: string | null;
  kind: ImageKind;
  /** `sizes` for the srcset (CSS width of the image per breakpoint) */
  sizes?: string;
  alt?: string;
  /** high: billboard (fetchpriority=high); eager: load now; lazy: load when near the viewport */
  priority?: 'high' | 'eager' | 'lazy';
  /** Extra classes for the <img>, e.g. object-top */
  className?: string;
  /** Extra classes for the wrapper (absolute inset-0 by default) */
  wrapperClassName?: string;
  onLoad?: () => void;
  onError?: () => void;
}

// Skeleton (#2f2f2f + shimmer) -> blurred tiny placeholder -> decoded image fading in.
// Must sit inside a positioned box that has the image's aspect ratio.
export default function TmdbImage({
  src,
  kind,
  sizes,
  alt = '',
  priority = 'lazy',
  className = '',
  wrapperClassName = '',
  onLoad,
  onError,
}: TmdbImageProps) {
  const [wrap, setWrap] = useState<HTMLDivElement | null>(null);
  const imgRef = useRef<HTMLImageElement>(null);
  const row = useContext(RowGateContext);
  const lazy = priority === 'lazy';

  const nearViewport = useNearViewport(wrap, lazy && !row);
  const nearInRow = useNearViewport(wrap, lazy && !!row?.near && !!row.root, ROW_MARGIN, row?.root ?? null);
  const active = !lazy || (row ? row.near && (nearInRow || !row.root) : nearViewport);

  const path = tmdbPath(src);
  const main = path ? tmdbDefaultSrc(path, kind) : src || undefined;
  const srcSet = path ? tmdbSrcSet(path, kind) : undefined;
  const blur = path ? tmdbPlaceholder(path, kind) : undefined;

  const [loaded, setLoaded] = useState(false);
  const [blurReady, setBlurReady] = useState(false);
  const [blurGone, setBlurGone] = useState(false);
  const onLoadRef = useRef(onLoad);
  useLayoutEffect(() => {
    onLoadRef.current = onLoad;
  });

  // A new image starts over (state adjusted during render)
  const [prevMain, setPrevMain] = useState(main);
  if (main !== prevMain) {
    setPrevMain(main);
    setLoaded(false);
    setBlurReady(false);
    setBlurGone(false);
  }

  const reveal = useCallback((img: HTMLImageElement) => {
    let alive = true;
    const done = () => {
      if (!alive || imgRef.current !== img) return;
      setLoaded(true);
      onLoadRef.current?.();
    };
    // decode() keeps the fade from stuttering on big images; failures just show it directly
    if (typeof img.decode === 'function') img.decode().then(done, done);
    else done();
    return () => { alive = false; };
  }, []);

  // Images restored from the browser cache can finish before React attaches onLoad
  useEffect(() => {
    const img = imgRef.current;
    if (!active || !img?.complete || img.naturalWidth === 0) return undefined;
    return reveal(img);
  }, [active, main, reveal]);

  useEffect(() => {
    if (!loaded) return;
    const t = window.setTimeout(() => setBlurGone(true), BLUR_DROP_MS);
    return () => window.clearTimeout(t);
  }, [loaded]);

  return (
    <div ref={setWrap} className={`nf-img ${wrapperClassName}`} data-loaded={loaded || undefined}>
      {active && !!blur && !blurGone && (
        <img
          src={blur}
          alt=""
          aria-hidden
          decoding="async"
          draggable={false}
          onLoad={() => setBlurReady(true)}
          className={`nf-img-blur ${className}`}
          data-ready={blurReady || undefined}
        />
      )}
      {active && !!main && (
        <img
          ref={imgRef}
          fetchPriority={priority === 'high' ? 'high' : undefined}
          // sizes and srcset before src, so no browser starts fetching the fallback first
          sizes={srcSet ? sizes || '100vw' : undefined}
          srcSet={srcSet}
          src={main}
          alt={alt}
          decoding="async"
          draggable={false}
          onLoad={e => reveal(e.currentTarget)}
          onError={onError}
          className={`nf-img-main ${className}`}
        />
      )}
    </div>
  );
}
