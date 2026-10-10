// Routes that no longer exist; old links and bookmarks land on Home
const REMOVED_ROUTES = [
  '/browse',
  '/browse/:path*',
  '/movie/:path*',
  '/tv-series',
  '/tv-series/:path*',
  '/now-playing',
  '/now-playing/:path*',
  '/dialog-demo',
  '/music',
  '/music/:path*',
  '/trailers',
  '/providers',
  '/providers/:path*',
];

/** @type {import('next').NextConfig} */
const nextConfig = {
  output: 'standalone',
  
  // Performance optimizations
  experimental: {
    optimizePackageImports: ['framer-motion', 'lucide-react'],
  },

  // Development configuration
  allowedDevOrigins: ['192.168.0.109', 'localhost', '127.0.0.1', '0.0.0.0'],

  // Compiler optimizations (errors and warnings stay visible in production)
  compiler: {
    removeConsole: process.env.NODE_ENV === 'production' ? { exclude: ['error', 'warn'] } : false,
  },

  // Turbopack (the default bundler) splits chunks itself; anime4k.js stays a lazy chunk via its dynamic import.
  // The `@/` alias comes from tsconfig paths.

  // Static optimization
  trailingSlash: false,
  poweredByHeader: false,

  // next/image is not used; images load via /tmdb-img (Go server cache), so the optimizer stays off
  images: {
    unoptimized: true,
  },

  // Headers for caching
  async headers() {
    return [
      {
        source: '/(.*)',
        headers: [
          {
            key: 'X-Content-Type-Options',
            value: 'nosniff',
          },
          {
            key: 'X-Frame-Options',
            value: 'DENY',
          },
          {
            key: 'X-XSS-Protection',
            value: '1; mode=block',
          },
        ],
      },
    ];
  },

  async redirects() {
    return [
      // My List became Favorites
      { source: '/my-list', destination: '/favorites', permanent: false },
      ...REMOVED_ROUTES.map((source) => ({ source, destination: '/', permanent: false })),
    ];
  },

  async rewrites() {
    const backend = process.env.BACKEND_INTERNAL_URL || 'http://localhost:8252';
    const tvApp = process.env.TV_APP_INTERNAL_URL || 'http://tvapp';
    return {
      // The TV app for the TV's own browser: http://tv.nextflix.local (or nextflix-tv.local)
      beforeFiles: [
        {
          source: '/:path*',
          has: [{ type: 'host', value: '(?:tv\\.nextflix|nextflix-tv)\\.local' }],
          destination: `${tvApp}/:path*`,
        },
      ],
      afterFiles: [
        // TMDB images go through the Go server's disk cache (browsers never resolve image.tmdb.org themselves)
        { source: '/tmdb-img/:path*', destination: `${backend}/tmdb-img/:path*` },
        { source: '/api/:path*', destination: `${backend}/api/:path*` },
      ],
    };
  },

  env: {
    // Only used for server-side fetches; browsers call the backend via getApiUrl()
    NEXT_PUBLIC_API_URL: process.env.NEXT_PUBLIC_API_URL || process.env.BACKEND_INTERNAL_URL || 'http://localhost:8252',
  },
};

module.exports = nextConfig;
