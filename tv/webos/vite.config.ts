import { defineConfig, type Plugin } from 'vite';
import react from '@vitejs/plugin-react';
import pkg from './package.json';

// webOS starts packaged apps from file://, where module scripts and `crossorigin` fail to load.
// The build is one classic IIFE script, loaded with `defer` at the end of <body>.
function classicScript(): Plugin {
  return {
    name: 'nextflix-classic-script',
    apply: 'build',
    enforce: 'post',
    transformIndexHtml: {
      order: 'post',
      handler(html) {
        let src = '';
        html = html.replace(/<script type="module"[^>]*src="([^"]+)"[^>]*><\/script>\s*/, (_m, s: string) => {
          src = s;
          return '';
        });
        html = html.replace(/ crossorigin(="[^"]*")?/g, '');
        return src ? html.replace('</body>', `  <script defer src="${src}"></script>\n  </body>`) : html;
      },
    },
  };
}

export default defineConfig({
  base: './',
  define: { __APP_VERSION__: JSON.stringify(pkg.version) },
  plugins: [react(), classicScript()],
  build: {
    // webOS 4.x runs Chromium 53, webOS 5.x Chromium 68 (APIs are polyfilled in src/polyfills.ts)
    target: 'chrome53',
    cssTarget: 'chrome53',
    assetsDir: 'static',
    modulePreload: false,
    cssCodeSplit: false,
    sourcemap: false,
    rollupOptions: {
      output: {
        format: 'iife',
        inlineDynamicImports: true,
      },
    },
  },
  server: {
    // The source lives on the Windows drive: inotify events do not reach the container
    watch: { usePolling: true, interval: 300 },
  },
});
