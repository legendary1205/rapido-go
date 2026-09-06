import dayjs from "dayjs";
import Duration from "dayjs/plugin/duration";
import LocalizedFormat from "dayjs/plugin/localizedFormat";
import RelativeTime from "dayjs/plugin/relativeTime";
import Timezone from "dayjs/plugin/timezone";
import utc from "dayjs/plugin/utc";
import "locales/i18n";
import React from "react";
import ReactDOM from "react-dom/client";
import { QueryClientProvider } from "@tanstack/react-query";
import { queryClient } from "utils/queryClient";
import App from "./App";
import "index.scss";
import "rapido-ui/tailwind.css";

dayjs.extend(Timezone);
dayjs.extend(LocalizedFormat);
dayjs.extend(utc);
dayjs.extend(RelativeTime);
dayjs.extend(Duration);

// The browser chrome colour is a constant - one dark palette, no light/dark
// mode switch.
document
  .querySelector('meta[name="theme-color"]')
  ?.setAttribute("content", "#0a1116");

ReactDOM.createRoot(document.getElementById("root") as HTMLElement).render(
  <React.StrictMode>
    {/* One root QueryClient for the whole app (see utils/queryClient.ts) -
        the old dashboard had two: this app-wide one and OverviewNew.tsx's
        own private instance, which meant a write on the Users page could
        never invalidate Overview's cached stats. */}
    <QueryClientProvider client={queryClient}>
      <App />
    </QueryClientProvider>
  </React.StrictMode>
);
