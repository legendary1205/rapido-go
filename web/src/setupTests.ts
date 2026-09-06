import "@testing-library/jest-dom/vitest";
// The real i18n instance, not a mock: resources are bundled JSON (see
// locales/i18n.ts's own comment on why), so this initializes synchronously
// with the actual English/Persian copy - no HTTP, no fake translation keys.
import "locales/i18n";

// jsdom implements no layout engine, so it has no ResizeObserver at all -
// Recharts' ResponsiveContainer (used on the Overview page) reads one to
// size its SVG and throws a ReferenceError without this. Tests don't need
// real resize behavior, just something that satisfies the interface.
class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}
globalThis.ResizeObserver = ResizeObserverStub;
