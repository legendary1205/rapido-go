// A found, real version-skew bug between two pinned dependencies, not a
// design choice - documented here rather than silenced.
//
// @testing-library/jest-dom@6.x's own vitest integration
// (node_modules/@testing-library/jest-dom/vitest.d.ts, loaded by
// setupTests.ts's `import "@testing-library/jest-dom/vitest"`) augments
// `declare module "vitest"`'s `Assertion` interface - correct for the vitest
// version current jest-dom releases actually target. This project pins the
// older vitest@0.34.6 on purpose (the plan is explicit: toolchain versions
// aren't upgraded this phase, only pages are rebuilt), and vitest@0.34
// declares its own `Assertion` interface under the separate `@vitest/expect`
// module instead (see node_modules/vitest/dist/reporters-*.d.ts) - a module
// jest-dom's own .d.ts never touches. The two declaration-merge targets
// never meet, so every jest-dom matcher (toBeInTheDocument, toHaveClass, ...)
// type-errors under `tsc --noEmit`/`npm run build` despite working correctly
// at runtime (expect.extend runs regardless of what TypeScript thinks).
//
// This file re-targets the same augmentation at the module vitest@0.34
// actually reads its `Assertion` interface from, importing the real matcher
// signatures from jest-dom itself so nothing here can drift out of sync with
// what jest-dom actually implements. The real fix is bumping one of the two
// packages once the "don't touch toolchain versions" constraint for this
// phase lifts - remove this file when it does.
import matchers = require("@testing-library/jest-dom/matchers");

declare module "@vitest/expect" {
  interface Assertion<T = any> extends matchers.TestingLibraryMatchers<any, T> {}
  interface AsymmetricMatchersContaining
    extends matchers.TestingLibraryMatchers<any, any> {}
}
