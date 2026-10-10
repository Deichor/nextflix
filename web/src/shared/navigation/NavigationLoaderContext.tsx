"use client";

import React, { createContext, useContext, useState, useCallback, useEffect, useRef, Suspense } from 'react';
import { usePathname, useSearchParams } from 'next/navigation';
import { isCurrentUrl } from '@/shared/navigation/isCurrentUrl';

// Clears the overlay if a navigation never changes the URL (same page, failed push, ...).
const SAFETY_TIMEOUT_MS = 10_000;

interface NavigationLoaderContextType {
  isLoading: boolean;
  /** Shows the overlay; pass the target href to skip it when it is the current URL. */
  startLoading: (href?: string) => void;
  stopLoading: () => void;
}

const NavigationLoaderContext = createContext<NavigationLoaderContextType>({
  isLoading: false,
  startLoading: () => {},
  stopLoading: () => {},
});

export const useNavigationLoader = () => {
  const context = useContext(NavigationLoaderContext);
  if (!context) {
    throw new Error('useNavigationLoader must be used within NavigationLoaderProvider');
  }
  return context;
};

// Inner component that uses useSearchParams
const NavigationLoaderInner: React.FC<{
  children: React.ReactNode;
  stopLoading: () => void;
}> = ({ children, stopLoading }) => {
  const pathname = usePathname();
  const searchParams = useSearchParams();

  // Stop loading when route changes complete
  useEffect(() => {
    stopLoading();
  }, [pathname, searchParams, stopLoading]);

  return <>{children}</>;
};

export const NavigationLoaderProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const [isLoading, setIsLoading] = useState(false);
  const timerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const stopLoading = useCallback(() => {
    if (timerRef.current) {
      clearTimeout(timerRef.current);
      timerRef.current = null;
    }
    setIsLoading(false);
  }, []);

  const startLoading = useCallback((href?: string) => {
    if (href !== undefined && isCurrentUrl(href)) return;
    if (timerRef.current) clearTimeout(timerRef.current);
    timerRef.current = setTimeout(() => {
      timerRef.current = null;
      setIsLoading(false);
    }, SAFETY_TIMEOUT_MS);
    setIsLoading(true);
  }, []);

  useEffect(() => () => {
    if (timerRef.current) clearTimeout(timerRef.current);
  }, []);

  return (
    <NavigationLoaderContext.Provider value={{ isLoading, startLoading, stopLoading }}>
      <Suspense fallback={children}>
        <NavigationLoaderInner stopLoading={stopLoading}>
          {children}
        </NavigationLoaderInner>
      </Suspense>
    </NavigationLoaderContext.Provider>
  );
};
