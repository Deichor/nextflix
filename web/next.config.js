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

  // Compiler optimizations
  compiler: {
    removeConsole: process.env.NODE_ENV === 'production',
  },

  // Bundle optimization
  webpack: (config, { dev, isServer }) => {
    // Optimize bundle splitting
    if (!dev && !isServer) {
      config.optimization.splitChunks = {
        chunks: 'all',
        cacheGroups: {
          vendor: {
            // anime4k.js (large shaders) stays in its own lazy chunk, loaded only when upscaling is on
            test: /[\\/]node_modules[\\/](?!anime4k\.js[\\/])/,
            name: 'vendors',
            chunks: 'all',
          },
          common: {
            name: 'common',
            minChunks: 2,
            chunks: 'all',
            enforce: true,
          },
        },
      };
    }

    // Optimize imports
    config.resolve.alias = {
      ...config.resolve.alias,
      '@': require('path').resolve(__dirname, 'src'),
    };

    return config;
  },

  // Static optimization
  trailingSlash: false,
  poweredByHeader: false,
  
  eslint: {
    ignoreDuringBuilds: true,
  },

  // Image optimization
  images: {
    formats: ['image/webp', 'image/avif'],
    minimumCacheTTL: 31536000, // 1 year
    dangerouslyAllowSVG: true,
    contentSecurityPolicy: "default-src 'self'; script-src 'none'; sandbox;",
    remotePatterns: [
      {
        protocol: 'https',
        hostname: 'image.tmdb.org',
        pathname: '/t/p/**',
      },
    ],
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
      {
        source: '/_next/static/(.*)',
        headers: [
          {
            key: 'Cache-Control',
            value: 'public, max-age=31536000, immutable',
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
