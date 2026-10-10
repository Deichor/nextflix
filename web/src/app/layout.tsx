import type { Metadata, Viewport } from "next";
import "./globals.css";
import { NavigationLoaderProvider } from "@/shared/navigation/NavigationLoaderContext";
import NavigationLoader from "@/shared/navigation/NavigationLoader";
import ImageGuard from "@/shared/ui/ImageGuard";
import ProfileGate from "@/features/profiles/components/ProfileGate";
import { I18nProvider } from "@/shared/i18n/I18nProvider";
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
      { url: '/favicon-16x16.png', type: 'image/png', sizes: '16x16' },
      { url: '/favicon-32x32.png', type: 'image/png', sizes: '32x32' },
    ],
    apple: [{ url: '/apple-touch-icon.png', sizes: '180x180', type: 'image/png' }],
  },
  manifest: '/site.webmanifest',
  // "Add to Home Screen" opens like an app (status bar text over the dark page)
  appleWebApp: {
    capable: true,
    title: 'Nextflix',
    statusBarStyle: 'black',
  },
};

export const viewport: Viewport = {
  width: 'device-width',
  initialScale: 1,
  // Draw under the notch / home indicator; the shell pads with env(safe-area-inset-*)
  viewportFit: 'cover',
  themeColor: '#141414',
  colorScheme: 'dark',
};

export default function RootLayout({
  children,
}: {
  children: React.ReactNode;
}) {
  return (
    <html lang="en" className="dark" suppressHydrationWarning>
      <head>
        {/* Self-hosted Inter font */}
        <link rel="preload" href="/fonts/Inter/InterVariable.woff2" as="font" type="font/woff2" crossOrigin="anonymous" />

        {/* Older iOS versions only read the apple- prefixed name */}
        <meta name="apple-mobile-web-app-capable" content="yes" />

        {/* Prevent FOUC */}
        <script
          dangerouslySetInnerHTML={{
            __html: `
              (function() {
                try {
                  var theme = localStorage.getItem('theme') || 'dark';
                  document.documentElement.classList.add(theme);
                } catch (e) {}
                try {
                  var p = JSON.parse(localStorage.getItem('hfx-profile') || 'null');
                  var lang = (p && p.language) || localStorage.getItem('hfx-lang') ||
                    ((navigator.language || '').toLowerCase().indexOf('tr') === 0 ? 'tr' : 'en');
                  if (lang === 'en' || lang === 'tr') document.documentElement.lang = lang;
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
                  // Firefox keeps the style accessors on CSS2Properties.prototype
                  var styleProtos = [CSSStyleDeclaration.prototype];
                  if (typeof CSS2Properties !== 'undefined') styleProtos.push(CSS2Properties.prototype);
                  styleProtos.forEach(function(proto) {
                    ['backgroundImage', 'background', 'background-image', 'cssText'].forEach(function(p) { wrap(proto, p); });
                  });
                  var setProp = CSSStyleDeclaration.prototype.setProperty;
                  CSSStyleDeclaration.prototype.setProperty = function(n, v, pr) { return setProp.call(this, n, fix(v), pr); };
                } catch (e) {}
              })();
            `,
          }}
        />
      </head>
      <body className="bg-[#141414] text-white antialiased font-sans" style={{ fontFamily: "'Inter', -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif" }}>
        <I18nProvider>
          <NavigationLoaderProvider>
            <Suspense fallback={<div className="min-h-screen bg-[#141414]" />}>
              <ImageGuard />
              <NavigationLoader />
              <ProfileGate>{children}</ProfileGate>
            </Suspense>
          </NavigationLoaderProvider>
        </I18nProvider>
      </body>
    </html>
  );
}
