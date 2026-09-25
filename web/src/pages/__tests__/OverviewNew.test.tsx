import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, waitFor } from "@testing-library/react";
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

const stats = {
  total_user: 9400,
  online_users: 120,
  users_active: 9000,
  users_on_hold: 0,
  users_disabled: 0,
  users_expired: 0,
  users_limited: 0,
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

const answer = (isSudo: boolean) => (url: string) => {
  switch (url) {
    case "/admin":
      return Promise.resolve({ username: "someone", is_sudo: isSudo, is_owner: false });
    case "/system":
      return Promise.resolve(stats);
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
});
afterEach(() => {
  vi.clearAllMocks();
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
