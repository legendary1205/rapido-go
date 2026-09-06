/// <reference types="vite/client" />
/// <reference types="vite-plugin-svgr/client" />

// Injected by vite.config.ts `define` - the build stamp appended to locale
// requests so a deploy invalidates the browser's cached translation files.
declare const __LOCALE_VERSION__: string;
