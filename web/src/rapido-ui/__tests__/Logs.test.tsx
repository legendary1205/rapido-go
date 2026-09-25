import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import i18n from "locales/i18n";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { LogEntry, LogSource, LogsResponse } from "types/Logs";

const fetchMock = vi.hoisted(() => vi.fn());
const auth = vi.hoisted(() => ({ sudo: true }));

vi.mock("service/http", () => ({ fetch: fetchMock, fetcher: fetchMock }));
// The page's own gate (SudoOnly, in the router) is tested elsewhere; here only
// the component's "do not even ask" rule for a non-sudo admin matters.
vi.mock("hooks/useCurrentAdminQuery", () => ({ useIsSudo: () => auth.sudo }));

import { Logs } from "../Logs";

beforeAll(async () => {
  await i18n.changeLanguage("en");
});

// ---- a tiny fake of GET /logs and GET /logs/sources ------------------------

const T0 = Date.parse("2026-09-25T04:00:00Z");

/** A panel-style line: one slog JSON object. */
const jsonEntry = (n: number, level: LogEntry["level"] = "info", msg = `msg-${n}`, extra: object = {}): LogEntry => ({
  id: (T0 + n) * 1000,
  ts: new Date(T0 + n).toISOString(),
  level,
  line: JSON.stringify({ time: new Date(T0 + n).toISOString(), level: level.toUpperCase(), msg, ...extra }),
});

/** A node-style line: plain text. */
const textEntry = (n: number, level: LogEntry["level"], text: string): LogEntry => ({
  id: (T0 + n) * 1000,
  ts: new Date(T0 + n).toISOString(),
  level,
  line: text,
});

const sources: LogSource[] = [
  { id: "panel", label: "Panel API", kind: "panel" },
  { id: "backend", label: "Backend jobs", kind: "backend" },
  { id: "node:3", label: "nod1", kind: "node", status: "connected" },
];

type Query = { source: string; after: number; limit: number };
const store: Record<string, LogEntry[]> = {};
let streaming = true;
let feed: (q: Query) => LogsResponse | Promise<LogsResponse>;
let sourcesAnswer: () => Promise<LogSource[]>;

const serve = ({ source, after, limit }: Query): LogsResponse => {
  const all = store[source] ?? [];
  const entries = after === 0 ? all.slice(-limit) : all.filter((e) => e.id > after).slice(0, limit);
  return { entries, next: entries.length ? entries[entries.length - 1].id : after, streaming };
};

const httpError = (status: number) => Object.assign(new Error(`HTTP ${status}`), { response: { status } });

const logCalls = (): Query[] => fetchMock.mock.calls.filter((c) => c[0] === "/logs").map((c) => c[1].query);

beforeEach(() => {
  vi.useFakeTimers();
  auth.sudo = true;
  streaming = true;
  for (const k of Object.keys(store)) delete store[k];
  feed = serve;
  sourcesAnswer = () => Promise.resolve(sources);
  fetchMock.mockReset();
  fetchMock.mockImplementation((url: string, opts?: { query?: Query }) => {
    if (url === "/logs/sources") return sourcesAnswer();
    if (url === "/logs") return Promise.resolve(feed(opts!.query!));
    return Promise.reject(new Error(`unexpected request ${url}`));
  });
});

afterEach(async () => {
  vi.useRealTimers();
  vi.restoreAllMocks();
  await i18n.changeLanguage("en");
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  delete (document as any).visibilityState;
});

const tick = (ms: number) =>
  act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });

const renderLogs = async () => {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = render(
    <QueryClientProvider client={queryClient}>
      <Logs />
    </QueryClientProvider>
  );
  await tick(0); // the first poll and the source list
  return view;
};

const viewport = () => screen.getByTestId("log-viewport");
const rowsOf = () => Array.from(viewport().querySelectorAll("[data-level]")) as HTMLElement[];
const rowTexts = () => rowsOf().map((r) => r.textContent ?? "");
const click = (el: HTMLElement) => fireEvent.click(el);

describe("Logs page polling", () => {
  it("asks for the newest 300 first, then only what is newer than the cursor, every 1.5 s", async () => {
    store.panel = [jsonEntry(1), jsonEntry(2)];
    await renderLogs();

    expect(logCalls()).toEqual([{ source: "panel", after: 0, limit: 300 }]);
    expect(screen.getByText("msg-1")).toBeInTheDocument();
    expect(screen.getByText("msg-2")).toBeInTheDocument();

    await tick(1499);
    expect(logCalls()).toHaveLength(1);
    store.panel.push(jsonEntry(3));
    await tick(1);

    expect(logCalls()).toHaveLength(2);
    expect(logCalls()[1]).toEqual({ source: "panel", after: jsonEntry(2).id, limit: 500 });
    expect(screen.getByText("msg-3")).toBeInTheDocument();
    // Not appended twice.
    expect(screen.getAllByText("msg-2")).toHaveLength(1);
  });

  it("never has two requests in flight: the next poll is scheduled when the previous one ends", async () => {
    let release: (r: LogsResponse) => void = () => {};
    feed = () => new Promise<LogsResponse>((resolve) => (release = resolve));
    await renderLogs();
    expect(logCalls()).toHaveLength(1);

    await tick(10_000); // a slow answer must not stack up requests behind it
    expect(logCalls()).toHaveLength(1);

    release({ entries: [], next: 0, streaming: true });
    await tick(1500);
    expect(logCalls()).toHaveLength(2);
  });

  it("stops polling when the page unmounts", async () => {
    const view = await renderLogs();
    expect(logCalls().length).toBeGreaterThan(0);
    view.unmount();
    const before = fetchMock.mock.calls.length;

    await tick(30_000);
    expect(fetchMock.mock.calls.length).toBe(before);
  });

  it("does not poll while the tab is hidden and catches up as soon as it is shown", async () => {
    Object.defineProperty(document, "visibilityState", { configurable: true, get: () => "hidden" });
    store.panel = [jsonEntry(1)];
    await renderLogs();
    await tick(10_000);
    expect(logCalls()).toHaveLength(0);

    Object.defineProperty(document, "visibilityState", { configurable: true, get: () => "visible" });
    await act(async () => {
      document.dispatchEvent(new Event("visibilitychange"));
    });
    await tick(0);
    expect(logCalls()).toHaveLength(1);
    expect(screen.getByText("msg-1")).toBeInTheDocument();
  });

  it("fires no request at all for a non-sudo admin", async () => {
    auth.sudo = false;
    await renderLogs();
    await tick(30_000);
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("Logs page rendering", () => {
  it("shows a JSON line as its message plus dim key=value attributes, and any other line as-is", async () => {
    store.panel = [jsonEntry(1, "warn", "request slow", { status: 504, path: "/api/users", note: "two words" })];
    await renderLogs();

    expect(screen.getByText("request slow")).toBeInTheDocument();
    expect(screen.getByText("status=")).toBeInTheDocument();
    expect(screen.getByText("504")).toBeInTheDocument();
    expect(screen.getByText("path=")).toBeInTheDocument();
    expect(screen.getByText("/api/users")).toBeInTheDocument();
    expect(screen.getByText('"two words"')).toBeInTheDocument();
    // time, level and msg are not repeated as attributes.
    expect(screen.queryByText("msg=")).not.toBeInTheDocument();
    expect(screen.queryByText("time=")).not.toBeInTheDocument();
    expect(rowsOf()[0].dataset.level).toBe("warn");
    expect(rowsOf()[0].textContent).toMatch(/^\d\d:\d\d:\d\d\.\d{3}warn/);
  });

  it("falls back to the raw text for a line that is not JSON", async () => {
    store["node:3"] = [textEntry(1, "error", "inbound/vless[main#20001]: process connection from 1.2.3.4:5: EOF")];
    await renderLogs();
    click(screen.getByRole("button", { name: /nod1/ }));
    await tick(0);

    expect(screen.getByText("inbound/vless[main#20001]: process connection from 1.2.3.4:5: EOF")).toBeInTheDocument();
  });

  it("cuts a pathologically long line short on screen, so one line cannot widen every row", async () => {
    store["node:3"] = [textEntry(1, "info", "x".repeat(10_000))];
    await renderLogs();
    click(screen.getByRole("button", { name: /nod1/ }));
    await tick(0);

    const shown = rowsOf()[0].textContent ?? "";
    expect(shown.length).toBeLessThan(4200);
    expect(shown.endsWith("…")).toBe(true);
  });

  it("keeps the log text left-to-right in the Persian (right-to-left) UI", async () => {
    await i18n.changeLanguage("fa");
    store.panel = [jsonEntry(1)];
    await renderLogs();

    expect(viewport()).toHaveAttribute("dir", "ltr");
    // The chrome around it is translated.
    expect(screen.getByText("زنده")).toBeInTheDocument();
  });

  it("holds at most 2000 entries and only puts the rows in view into the DOM", async () => {
    store.panel = Array.from({ length: 300 }, (_, i) => jsonEntry(i + 1));
    await renderLogs();

    for (let batch = 0; batch < 4; batch++) {
      const base = 300 + batch * 500;
      store.panel.push(...Array.from({ length: 500 }, (_, i) => jsonEntry(base + i + 1)));
      await tick(1500);
    }

    expect(screen.getByText(/2000 of 2000 lines/)).toBeInTheDocument();
    expect(rowsOf().length).toBeGreaterThan(0);
    expect(rowsOf().length).toBeLessThan(80);
    // Pinned to the newest line.
    expect(screen.getByText("msg-2300")).toBeInTheDocument();
    expect(screen.queryByText("msg-301")).not.toBeInTheDocument();
  });
});

describe("Logs toolbar", () => {
  const mixed = () => {
    store.panel = [
      jsonEntry(1, "debug", "delta cache"),
      jsonEntry(2, "info", "alpha started"),
      jsonEntry(3, "warn", "beta slow"),
      jsonEntry(4, "error", "gamma failed"),
    ];
  };

  it("filters by minimum level", async () => {
    mixed();
    await renderLogs();
    expect(rowsOf()).toHaveLength(4);

    fireEvent.change(screen.getByLabelText("Minimum level"), { target: { value: "warn" } });
    expect(rowTexts().join("|")).toMatch(/beta slow.*gamma failed/);
    expect(screen.queryByText("alpha started")).not.toBeInTheDocument();
    expect(screen.queryByText("delta cache")).not.toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Minimum level"), { target: { value: "error" } });
    expect(rowsOf()).toHaveLength(1);
    expect(screen.getByText("gamma failed")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Minimum level"), { target: { value: "all" } });
    expect(rowsOf()).toHaveLength(4);
  });

  it("searches the log text, case-insensitively, and says so when nothing matches", async () => {
    mixed();
    await renderLogs();
    const search = screen.getByPlaceholderText("Search in log lines");

    fireEvent.change(search, { target: { value: "BETA" } });
    expect(rowsOf()).toHaveLength(1);
    expect(screen.getByText("beta slow")).toBeInTheDocument();
    expect(screen.getByText(/1 of 4 lines/)).toBeInTheDocument();

    fireEvent.change(search, { target: { value: "no such text" } });
    expect(rowsOf()).toHaveLength(0);
    expect(screen.getByText("No lines match the current filters.")).toBeInTheDocument();
  });

  it("pauses the live tail without losing the view, and resumes from the same cursor", async () => {
    store.panel = [jsonEntry(1), jsonEntry(2)];
    await renderLogs();
    expect(screen.getByText("LIVE")).toBeInTheDocument();

    click(screen.getByRole("button", { name: "Pause" }));
    expect(screen.getByText("Paused")).toBeInTheDocument();
    const callsWhenPaused = logCalls().length;
    store.panel.push(jsonEntry(3));

    await tick(10_000);
    expect(logCalls()).toHaveLength(callsWhenPaused);
    expect(screen.queryByText("msg-3")).not.toBeInTheDocument();
    expect(screen.getByText("msg-2")).toBeInTheDocument(); // the view is intact

    click(screen.getByRole("button", { name: "Resume" }));
    await tick(0);
    expect(logCalls()).toHaveLength(callsWhenPaused + 1);
    expect(logCalls()[callsWhenPaused].after).toBe(jsonEntry(2).id);
    expect(screen.getByText("msg-3")).toBeInTheDocument();
    expect(screen.getAllByText("msg-2")).toHaveLength(1);
    expect(screen.getByText("LIVE")).toBeInTheDocument();
  });

  it("clears the view but does not bring the cleared lines back on the next poll", async () => {
    store.panel = [jsonEntry(1), jsonEntry(2)];
    await renderLogs();
    expect(rowsOf()).toHaveLength(2);

    click(screen.getByRole("button", { name: "Clear view" }));
    expect(rowsOf()).toHaveLength(0);
    expect(screen.getByText("No log lines yet.")).toBeInTheDocument();

    store.panel.push(jsonEntry(3));
    await tick(1500);
    expect(rowTexts().join("|")).toContain("msg-3");
    expect(screen.queryByText("msg-1")).not.toBeInTheDocument();
    expect(rowsOf()).toHaveLength(1);
  });

  it("switches source: resets the view, starts from after=0 and stops asking for the old one", async () => {
    store.panel = [jsonEntry(1, "info", "panel line")];
    store["node:3"] = [textEntry(1, "info", "node line")];
    await renderLogs();
    expect(screen.getByText("panel line")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Panel API/ })).toHaveAttribute("aria-pressed", "true");

    const before = logCalls().length;
    click(screen.getByRole("button", { name: /nod1/ }));
    await tick(0);

    expect(logCalls()[before]).toEqual({ source: "node:3", after: 0, limit: 300 });
    expect(screen.getByText("node line")).toBeInTheDocument();
    expect(screen.queryByText("panel line")).not.toBeInTheDocument();
    expect(screen.getByRole("button", { name: /nod1/ })).toHaveAttribute("aria-pressed", "true");

    await tick(6000);
    expect(logCalls().slice(before).every((q) => q.source === "node:3")).toBe(true);
    expect(logCalls().length).toBeGreaterThan(before + 1);
  });

  it("shows every source with its status dot, and tells a silent node from a live one", async () => {
    store["node:3"] = [];
    streaming = false;
    await renderLogs();
    expect(screen.getByRole("button", { name: /Panel API/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Backend jobs/ })).toBeInTheDocument();
    const nodeChip = screen.getByRole("button", { name: /nod1/ });
    expect(nodeChip.querySelector(".bg-emerald-400")).not.toBeNull();

    click(nodeChip);
    await tick(0);
    expect(screen.getByRole("status")).toHaveTextContent("Node not streaming yet");
    expect(screen.getByText(/asked to start streaming its log/)).toBeInTheDocument();

    streaming = true;
    store["node:3"].push(textEntry(1, "info", "hello from the node"));
    await tick(1500);
    expect(screen.getByRole("status")).toHaveTextContent("LIVE");
    expect(screen.getByText("hello from the node")).toBeInTheDocument();
  });

  it("keeps the panel and backend reachable when the source list cannot be loaded", async () => {
    sourcesAnswer = () => Promise.reject(httpError(500));
    await renderLogs();

    expect(screen.getByRole("button", { name: /Panel API/ })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /Backend jobs/ })).toBeInTheDocument();
    expect(screen.getByText(/Could not load the list of nodes/)).toBeInTheDocument();
    expect(logCalls().length).toBeGreaterThan(0);
  });
});

describe("Logs auto-follow", () => {
  const ROW = 22;

  it("stays on the newest line, lets go when the user scrolls up, and jumps back on request", async () => {
    store.panel = Array.from({ length: 100 }, (_, i) => jsonEntry(i + 1));
    await renderLogs();

    // Pinned to the bottom; only the window near it is in the DOM.
    expect(viewport().scrollTop).toBe(100 * ROW);
    expect(screen.getByText("msg-100")).toBeInTheDocument();
    expect(screen.queryByText("msg-1")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Jump to latest/ })).not.toBeInTheDocument();

    // New lines while following keep it pinned.
    store.panel.push(jsonEntry(101));
    await tick(1500);
    expect(viewport().scrollTop).toBe(101 * ROW);
    expect(screen.getByText("msg-101")).toBeInTheDocument();

    // The user scrolls to the top: follow lets go and the button appears.
    act(() => {
      viewport().scrollTop = 0;
      fireEvent.scroll(viewport());
    });
    expect(screen.getByText("msg-1")).toBeInTheDocument();
    const jump = screen.getByRole("button", { name: /Jump to latest/ });

    // New lines while scrolled up do not yank the view away.
    store.panel.push(jsonEntry(102), jsonEntry(103));
    await tick(1500);
    expect(viewport().scrollTop).toBe(0);
    expect(screen.getByText(/103 of 103 lines/)).toBeInTheDocument();

    click(jump);
    expect(viewport().scrollTop).toBe(103 * ROW);
    expect(screen.getByText("msg-103")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /Jump to latest/ })).not.toBeInTheDocument();

    // And it follows again.
    store.panel.push(jsonEntry(104));
    await tick(1500);
    expect(viewport().scrollTop).toBe(104 * ROW);
  });
});

describe("Logs errors", () => {
  it("explains a 503 in plain words, keeps trying more slowly, and recovers on its own", async () => {
    feed = () => Promise.reject(httpError(503));
    await renderLogs();

    expect(screen.getByRole("alert")).toHaveTextContent("Log storage is unavailable right now. Trying again automatically.");
    expect(screen.getByRole("status")).toHaveTextContent("Disconnected");
    expect(logCalls()).toHaveLength(1);

    // Backs off: nothing at the normal 1.5 s cadence ...
    store.panel = [jsonEntry(1, "info", "back again")];
    feed = serve;
    await tick(3000);
    expect(logCalls()).toHaveLength(1);
    // ... the retry comes at 5 s, and clears the message.
    await tick(2000);
    expect(logCalls()).toHaveLength(2);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(screen.getByText("back again")).toBeInTheDocument();
    expect(screen.getByRole("status")).toHaveTextContent("LIVE");
  });

  it("explains a 404 and stops polling that source", async () => {
    feed = () => Promise.reject(httpError(404));
    await renderLogs();

    expect(screen.getByRole("alert")).toHaveTextContent("This log source no longer exists.");
    await tick(30_000);
    expect(logCalls()).toHaveLength(1);
  });

  it("shows a generic message for anything else", async () => {
    feed = () => Promise.reject(new Error("network down"));
    await renderLogs();
    expect(screen.getByRole("alert")).toHaveTextContent("Could not load the logs. Trying again automatically.");
  });
});

// The download builds a real Blob and clicks a real link, which needs the
// browser's own async Blob reader - so this one runs on the real clock.
describe("Logs download", () => {
  beforeEach(() => {
    vi.useRealTimers();
  });

  it("saves the lines currently shown as a .txt file, one per line", async () => {
    store.panel = [jsonEntry(1, "info", "keep me"), jsonEntry(2, "error", "and me"), jsonEntry(3, "debug", "not me")];
    const createObjectURL = vi.fn(() => "blob:logs");
    const revokeObjectURL = vi.fn();
    Object.assign(URL, { createObjectURL, revokeObjectURL });
    const clicked: HTMLAnchorElement[] = [];
    vi.spyOn(HTMLAnchorElement.prototype, "click").mockImplementation(function (this: HTMLAnchorElement) {
      clicked.push(this);
    });

    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const view = render(
      <QueryClientProvider client={queryClient}>
        <Logs />
      </QueryClientProvider>
    );
    await waitFor(() => expect(screen.getByText("keep me")).toBeInTheDocument());

    fireEvent.change(screen.getByLabelText("Minimum level"), { target: { value: "info" } });
    click(screen.getByRole("button", { name: "Download .txt" }));

    expect(createObjectURL).toHaveBeenCalledTimes(1);
    const blob = (createObjectURL.mock.calls[0] as unknown as [Blob])[0];
    expect(blob.type).toContain("text/plain");
    expect(clicked).toHaveLength(1);
    expect(clicked[0].download).toMatch(/^rapido-logs-panel-.*\.txt$/);
    expect(revokeObjectURL).toHaveBeenCalledWith("blob:logs");

    const text = await new Promise<string>((resolve) => {
      const reader = new FileReader();
      reader.onload = () => resolve(String(reader.result));
      reader.readAsText(blob);
    });
    const lines = text.trimEnd().split("\n");
    expect(lines).toHaveLength(2);
    expect(lines[0]).toContain("INFO");
    expect(lines[0]).toContain("keep me");
    expect(lines[1]).toContain("ERROR");
    expect(text).not.toContain("not me");
    view.unmount();
  });
});
