import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen, waitFor, within } from "@testing-library/react";
import i18n from "locales/i18n";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { queryKeys } from "utils/queryClient";

const fetchMock = vi.hoisted(() => vi.fn());

vi.mock("service/http", () => ({ fetch: fetchMock, fetcher: fetchMock }));

import { OverviewNew } from "../OverviewNew";

beforeAll(async () => {
  await i18n.changeLanguage("en");
});

// The five statuses add up to total_user, as they do on a real panel.
const stats = {
  total_user: 9400,
  online_users: 120,
  users_active: 9000,
  users_on_hold: 200,
  users_disabled: 20,
  users_expired: 80,
  users_limited: 100,
  incoming_bandwidth: 0,
  outgoing_bandwidth: 0,
};

const snapshot = {
  generated_at: "2026-09-25T00:00:00Z",
  hosts: [
    {
      node_id: 1,
      name: "de-node",
      address: "10.0.0.1",
      reachable: true,
      has_metrics: true,
      collected_at: "2026-09-25T00:00:00Z",
      stale: false,
      cpu_percent: 10,
      mem_percent: 20,
      disk_percent: 30,
      rx_rate: 0,
      tx_rate: 0,
      connections: 5,
      tunnels_up: null,
      tunnels_total: null,
      tunnels: null,
      healthy: true,
    },
  ],
};

let currentStats: typeof stats = stats;

const answer = (isSudo: boolean) => (url: string) => {
  switch (url) {
    case "/admin":
      return Promise.resolve({ username: "someone", is_sudo: isSudo, is_owner: false });
    case "/system":
      return Promise.resolve(currentStats);
    case "/system/usage-history?days=14":
      return Promise.resolve([]);
    case "/monitoring":
      return Promise.resolve(snapshot);
    default:
      return Promise.reject(new Error(`unexpected request ${url}`));
  }
};

const requested = () => fetchMock.mock.calls.map((c) => c[0] as string);

const renderOverview = () => {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <OverviewNew />
      </MemoryRouter>
    </QueryClientProvider>
  );
  return queryClient;
};

// Braces matter: a function returned from beforeEach is run as its cleanup hook.
beforeEach(() => {
  fetchMock.mockReset();
  currentStats = stats;
});
afterEach(() => {
  vi.clearAllMocks();
  vi.useRealTimers();
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  delete (document as any).visibilityState;
});

describe("Overview page and sudo-only endpoints", () => {
  it("never requests /monitoring for a reseller admin, and hides the fleet sections", async () => {
    fetchMock.mockImplementation(answer(false));
    const queryClient = renderOverview();

    await waitFor(() => expect(queryClient.getQueryState(queryKeys.currentAdmin)?.status).toBe("success"));
    await waitFor(() => expect(requested()).toContain("/system"));
    // Give any (wrongly) enabled query the chance to fire before asserting.
    await new Promise((resolve) => setTimeout(resolve, 100));

    expect(requested()).not.toContain("/monitoring");
    expect(screen.queryByText("Fleet status")).not.toBeInTheDocument();
    expect(screen.queryByText("Node resource status")).not.toBeInTheDocument();
    // The parts a reseller is allowed to see are still there.
    expect(screen.getByText("Total Users")).toBeInTheDocument();
  });

  it("does not request /monitoring while the admin lookup is still pending", async () => {
    fetchMock.mockImplementation((url: string) =>
      url === "/admin" ? new Promise(() => {}) : answer(true)(url)
    );
    renderOverview();

    await waitFor(() => expect(requested()).toContain("/system"));
    await new Promise((resolve) => setTimeout(resolve, 100));

    expect(requested()).not.toContain("/monitoring");
  });

  it("requests /monitoring and shows the fleet sections for a sudo admin", async () => {
    fetchMock.mockImplementation(answer(true));
    renderOverview();

    await waitFor(() => expect(requested()).toContain("/monitoring"));
    expect(await screen.findByText("Fleet status")).toBeInTheDocument();
    expect(await screen.findByText("Node resource status")).toBeInTheDocument();
    expect((await screen.findAllByText("de-node")).length).toBeGreaterThan(0);
  });
});

describe("Overview user status legend", () => {
  const legend = async () => within(await screen.findByRole("list", { name: "User status breakdown" }));

  it("lists every status next to the donut with its swatch, label, count and percentage", async () => {
    fetchMock.mockImplementation(answer(true));
    renderOverview();

    const items = (await legend()).getAllByRole("listitem");
    expect(items).toHaveLength(5);
    expect(items.map((li) => li.textContent)).toEqual([
      "Active9,00096%",
      "On Hold2002.1%",
      "Limited1001.1%",
      "Expired800.9%",
      "Disabled200.2%",
    ]);
    // A swatch per row, in the same colour the donut slice uses.
    const swatches = items.map((li) => (li.querySelector("[aria-hidden]") as HTMLElement).style.backgroundColor);
    expect(new Set(swatches).size).toBe(5);
    expect(swatches[0]).toBe("rgb(52, 211, 153)"); // #34d399, active
  });

  it("sits in the same wrapping flex row as the donut, so it drops under it on a narrow card", async () => {
    fetchMock.mockImplementation(answer(true));
    renderOverview();
    const list = await screen.findByRole("list", { name: "User status breakdown" });
    const row = list.parentElement as HTMLElement;
    expect(row.className).toContain("flex-wrap");
    // The donut box is the legend's sibling, not its ancestor.
    expect(row.querySelector(":scope > div[dir='ltr']")).not.toBeNull();
  });

  it("keeps a status with no users in the list, dimmed, so the list keeps its shape", async () => {
    currentStats = { ...stats, users_active: 9400, users_on_hold: 0, users_limited: 0, users_expired: 0, users_disabled: 0 };
    fetchMock.mockImplementation(answer(true));
    renderOverview();

    const items = (await legend()).getAllByRole("listitem");
    expect(items).toHaveLength(5);
    expect(items[0].textContent).toBe("Active9,400100%");
    expect(items[1].textContent).toBe("On Hold00%");
    expect(items[1].className).toContain("opacity-50");
    expect(items[0].className).not.toContain("opacity-50");
  });

  it("translates the labels", async () => {
    await i18n.changeLanguage("ru");
    try {
      fetchMock.mockImplementation(answer(true));
      renderOverview();
      const list = await screen.findByRole("list", { name: "Распределение пользователей по статусам" });
      expect(within(list).getAllByRole("listitem")[0].textContent).toContain(i18n.getFixedT("ru")("status.active"));
    } finally {
      await i18n.changeLanguage("en");
    }
  });
});

describe("Overview online count", () => {
  const advance = (ms: number) =>
    act(async () => {
      await vi.advanceTimersByTimeAsync(ms);
    });
  const systemCalls = () => requested().filter((u) => u === "/system").length;

  it("shows online_users from /system as the live count", async () => {
    fetchMock.mockImplementation(answer(true));
    renderOverview();
    const card = (await screen.findByText("Online Now")).closest("div.rounded-xl2") as HTMLElement;
    expect(await within(card).findByText("120")).toBeInTheDocument();
    expect(within(card).getByText("connected right now")).toBeInTheDocument();
  });

  it("refetches /system every 5 s, so the count follows connections as they open and close", async () => {
    vi.useFakeTimers();
    fetchMock.mockImplementation(answer(true));
    renderOverview();
    await advance(0);
    expect(systemCalls()).toBe(1);
    expect(screen.getByText("120")).toBeInTheDocument();

    currentStats = { ...stats, online_users: 121 };
    await advance(4900);
    expect(systemCalls()).toBe(1);
    await advance(200);
    expect(systemCalls()).toBe(2);
    expect(screen.getByText("121")).toBeInTheDocument();

    currentStats = { ...stats, online_users: 95 };
    await advance(5000);
    expect(systemCalls()).toBe(3);
    expect(screen.getByText("95")).toBeInTheDocument();
  });

  it("does not poll while the tab is hidden", async () => {
    vi.useFakeTimers();
    fetchMock.mockImplementation(answer(true));
    renderOverview();
    await advance(0);
    expect(systemCalls()).toBe(1);

    Object.defineProperty(document, "visibilityState", { configurable: true, get: () => "hidden" });
    await advance(30_000);
    expect(systemCalls()).toBe(1);
  });
});
