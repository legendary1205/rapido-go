/** @type {import('tailwindcss').Config} */
module.exports = {
  // The whole dashboard is this UI - every page that can render a Tailwind
  // class needs to be listed here or its classes get purged from the build.
  content: [
    "./src/rapido-ui/**/*.{ts,tsx}",
    "./src/pages/RapidoHome.tsx",
    "./src/pages/OverviewNew.tsx",
    "./src/pages/UsersPage.tsx",
    "./src/pages/HostsPage.tsx",
    "./src/pages/AdminsPage.tsx",
    "./src/pages/IntegrationsPage.tsx",
    "./src/pages/UserTemplatesPage.tsx",
    "./src/pages/Login.tsx",
  ],
  theme: {
    extend: {
      // Same list as --rapido-font-sans (src/index.scss). This exists so
      // `font-sans` means the right thing wherever it is used, rather than
      // leaving it to whatever the browser's default sans-serif is.
      fontFamily: {
        sans: [
          "Inter",
          "Vazirmatn",
          "-apple-system",
          "BlinkMacSystemFont",
          "Segoe UI",
          "Roboto",
          "Oxygen",
          "Ubuntu",
          "Cantarell",
          "Fira Sans",
          "Droid Sans",
          "Helvetica Neue",
          "Tahoma",
          "sans-serif",
        ],
      },
      colors: {
        // Control-room palette: a cold slate ground (not neutral gray - it
        // carries a faint blue cast, like light off a monitor) with cyan as
        // the one accent doing exactly one job - brand and interactivity.
        // Health/status is never conveyed by this color; that's Tailwind's
        // own emerald/amber/rose, kept deliberately separate so "this is
        // clickable" and "this is healthy" never collide into one meaning.
        rapido: {
          bg: "#0a1116",
          surface: "#111c22",
          // A third surface tier the old two-tone system didn't have -
          // modals, hovers, and anything that should read as "lifted" above
          // a card without resorting to a shadow (a console's screens don't
          // cast them).
          raised: "#16232b",
          border: "#223039",
          accent: "#22d3ee",
          text: "#e7f1f4",
          muted: "#7e97a0",
        },
      },
      borderRadius: {
        xl2: "1.25rem",
      },
    },
  },
  plugins: [],
};
