import react from "@vitejs/plugin-react";
import { splitVendorChunkPlugin } from "vite";
// vitest/config re-exports defineConfig with the `test` field typed in,
// on top of vite's own config shape - the dev server and build are
// unaffected, this only makes `test` below type-check.
import { defineConfig } from "vitest/config";
import svgr from "vite-plugin-svgr";
import { visualizer } from "rollup-plugin-visualizer";
import tsconfigPaths from "vite-tsconfig-paths";

// https://vitejs.dev/config/
export default defineConfig({
  // The Go panel serves this build's output at /dashboard/ (see
  // internal/httpapi/dashboard.go's MountDashboardStatic), not at the
  // origin root - every asset URL, and the locale JSON fetch path derived
  // from import.meta.env.BASE_URL in locales/i18n.ts, has to carry that
  // prefix or it 404s once this is actually deployed behind the panel.
  // Set here (not just at build time) so `npm run dev` matches production
  // instead of silently working at "/" and breaking on deploy.
  base: "/dashboard/",
  test: {
    environment: "jsdom",
    setupFiles: ["./src/setupTests.ts"],
    css: false,
    globals: true,
  },
  define: {
    // Locale JSON is served from a stable, unhashed URL, unlike every other
    // asset. A browser that cached it once keeps serving the old copy after a
    // deploy, so strings added in that deploy render as raw keys
    // ("rapido.tickets.title") until someone hard-reloads. Stamping the build
    // time into the request URL makes each deploy a cache miss.
    __LOCALE_VERSION__: JSON.stringify(Date.now().toString(36)),
  },
  plugins: [
    tsconfigPaths(),
    react({
      include: "**/*.tsx",
      // Vitest doesn't load pages through a real browser dev session, so
      // the fast-refresh preamble this plugin's Babel transform expects
      // never gets injected and every render throws "can't detect
      // preamble" - fast refresh has nothing to refresh in a test run
      // anyway. process.env.VITEST is set automatically by the test
      // runner, so `npm run dev`'s real HMR is untouched.
      fastRefresh: !process.env.VITEST,
    }),
    svgr(),
    visualizer(),
    splitVendorChunkPlugin(),
  ],
  server: {
    // The Go panel (`go run ./cmd/panel`) listens on :8000 by default
    // (UVICORN_PORT) and answers every API route under /api, plus the
    // subscription routes at the bare /sub prefix (see
    // internal/httpapi/router.go) - proxied here so `npm run dev` talks to
    // a real backend without a CORS round-trip or a second base URL to
    // configure for dev vs. prod.
    proxy: {
      "/api": { target: "http://localhost:8000", changeOrigin: true },
      "/sub": { target: "http://localhost:8000", changeOrigin: true },
    },
  },
});
