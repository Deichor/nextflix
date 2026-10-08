import type { Metadata } from "next";
import "./globals.css";
import { NavigationLoaderProvider } from "@/shared/navigation/NavigationLoaderContext";
import NavigationLoader from "@/shared/navigation/NavigationLoader";
import ImageGuard from "@/shared/ui/ImageGuard";
import ProfileGate from "@/features/profiles/components/ProfileGate";
import { Suspense } from "react";

export const metadata: Metadata = {
  title: {
    template: "%s | Nextflix",
    default: "Nextflix",
  },
  description: "Your personal Netflix-style streaming platform",
  applicationName: "Nextflix",
  formatDetection: {
    email: false,
    address: false,
    telephone: false,
  },
  icons: {
    icon: [
      { url: '/favicon.ico', sizes: 'any' },
      { url: '/favicon.svg', type: 'image/svg+xml' },
      { url: '/favicon-16x16.png', type: 'image/png', sizes: '16x16' },
      { url: '/favicon-32x32.png', type: 'image/png', sizes: '32x32' },
    ],
    apple: [{ url: '/apple-touch-icon.png', sizes: '180x180', type: 'image/png' }],
  },
  manifest: '/site.webmanifest',
};

export const viewport = {
  width: 'device-width',
  initialScale: 1,
  maximumScale: 1,
  themeColor: '#141414',
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en" className="dark">
      <head>
        {/* Self-hosted Inter font */}
        <link rel="preload" href="/fonts/Inter/InterVariable.woff2" as="font" type="font/woff2" crossOrigin="anonymous" />

        {/* Performance hints */}
        <meta name="color-scheme" content="dark" />

        {/* Prevent FOUC */}
        <script
          dangerouslySetInnerHTML={{
            __html: `
              (function() {
                try {
                  var theme = localStorage.getItem('theme') || 'dark';
                  document.documentElement.classList.add(theme);
                } catch (e) {}
              })();
            `,
          }}
        />

        {/* Route TMDB image URLs through /tmdb-img before the browser starts loading them
            (browsers resolve image.tmdb.org with plain DNS, which the ISP hijacks) */}
        <script
          dangerouslySetInnerHTML={{
            __html: `
              (function() {
                var T = 'https://image.tmdb.org/', P = '/tmdb-img/';
                function fix(v) { return typeof v === 'string' && v.indexOf(T) !== -1 ? v.split(T).join(P) : v; }
                function wrap(proto, prop) {
                  var d = Object.getOwnPropertyDescriptor(proto, prop);
                  if (!d || !d.set) return;
                  Object.defineProperty(proto, prop, {
                    configurable: true, enumerable: d.enumerable, get: d.get,
                    set: function(v) { d.set.call(this, fix(v)); }
                  });
                }
                try {
                  var setAttr = Element.prototype.setAttribute;
                  Element.prototype.setAttribute = function(n, v) {
                    if (n === 'src' || n === 'srcset' || n === 'style' || n === 'poster') v = fix(v);
                    return setAttr.call(this, n, v);
                  };
                  wrap(HTMLImageElement.prototype, 'src');
                  wrap(HTMLImageElement.prototype, 'srcset');
                  wrap(HTMLVideoElement.prototype, 'poster');
                  ['backgroundImage', 'background'].forEach(function(p) { wrap(CSSStyleDeclaration.prototype, p); });
                  var setProp = CSSStyleDeclaration.prototype.setProperty;
                  CSSStyleDeclaration.prototype.setProperty = function(n, v, pr) { return setProp.call(this, n, fix(v), pr); };
                } catch (e) {}
              })();
            `,
          }}
        />
      </head>
      <body className="bg-[#141414] text-white antialiased font-sans" style={{ fontFamily: "'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif" }}>
        <NavigationLoaderProvider>
          <Suspense fallback={<div className="min-h-screen bg-[#141414]" />}>
            <ImageGuard />
            <NavigationLoader />
            <ProfileGate>{children}</ProfileGate>
          </Suspense>
        </NavigationLoaderProvider>
      </body>
    </html>
  );
}
