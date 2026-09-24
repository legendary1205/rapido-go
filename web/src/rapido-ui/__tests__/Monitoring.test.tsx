import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import i18n from "locales/i18n";
import { beforeAll, describe, expect, it, vi } from "vitest";
import { MonitoringHost, MonitoringTunnel } from "types/Monitoring";

const state: { hosts: MonitoringHost[] } = { hosts: [] };

vi.mock("hooks/useMonitoringQuery", () => ({
  useMonitoringQuery: () => ({
    data: { hosts: state.hosts, generated_at: "2026-09-25T00:00:00Z" },
    isLoading: false,
    isError: false,
  }),
  useMonitoringHistoryQuery: () => ({ data: undefined, isLoading: false }),
}));

import { Monitoring } from "../Monitoring";

beforeAll(async () => {
  await i18n.changeLanguage("en");
});

const tunnel = (o: Partial<MonitoringTunnel> = {}): MonitoringTunnel => ({
  name: "wg0",
  up: true,
  present: true,
  rx_bytes: 1024 * 1024,
  tx_bytes: 2048,
  last_handshake: 0,
  handshake_age_seconds: 12,
  probe_ms: 23.4,
  fallback_active: false,
  ...o,
});

const host = (name: string, tunnels: MonitoringTunnel[] | null, o: Partial<MonitoringHost> = {}): MonitoringHost => ({
  node_id: 1,
  name,
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
  tunnels_up: tunnels ? tunnels.filter((t) => t.up && t.present).length : null,
  tunnels_total: tunnels ? tunnels.length : null,
  tunnels,
  healthy: true,
  ...o,
});

const card = (name: string) => screen.getByText(name, { selector: "span.truncate" }).closest("div.rounded-xl2") as HTMLElement;

describe("Monitoring tunnels", () => {
  it("shows every tunnel with its status, handshake age, latency and traffic", () => {
    state.hosts = [host("de", [tunnel({ name: "wg-de" })])];
    render(<Monitoring />);
    const c = within(card("de"));
    expect(c.getByText("wg-de")).toBeInTheDocument();
    expect(c.getByText("Up")).toBeInTheDocument();
    expect(c.getByText("12 s ago")).toBeInTheDocument();
    expect(c.getByText("23 ms")).toBeInTheDocument();
    expect(c.getByText("↓ 1 MB")).toBeInTheDocument();
    expect(c.getByText("↑ 2 KB")).toBeInTheDocument();
    expect(c.getByText("1/1")).toBeInTheDocument();
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
  });

  it("orders down and missing tunnels before healthy ones, then by name", () => {
    state.hosts = [
      host("de", [
        tunnel({ name: "wg-a" }),
        tunnel({ name: "wg-z", up: false }),
        tunnel({ name: "wg-m", up: false, present: false }),
      ]),
    ];
    render(<Monitoring />);
    const names = within(card("de"))
      .getAllByRole("listitem")
      .map((li) => li.querySelector("span.truncate")?.textContent);
    expect(names).toEqual(["wg-m", "wg-z", "wg-a"]);
  });

  it("labels a missing interface, a plain down tunnel and a down tunnel on the direct fallback differently", () => {
    state.hosts = [
      host("de", [
        tunnel({ name: "gone", up: false, present: false }),
        tunnel({ name: "dead", up: false }),
        tunnel({ name: "covered", up: false, fallback_active: true }),
      ]),
    ];
    render(<Monitoring />);
    const c = within(card("de"));
    expect(c.getByText("Interface missing")).toBeInTheDocument();
    expect(c.getByText("Down")).toBeInTheDocument();
    expect(c.getByText("Down - using direct fallback")).toBeInTheDocument();
    expect(c.getByText("Interface missing").className).toMatch(/red/);
    expect(c.getByText("Down - using direct fallback").className).toMatch(/yellow/);
    // A missing interface has no handshake or traffic to report.
    expect(c.getAllByText(/Last handshake/)).toHaveLength(2);
  });

  it("says never when there has been no handshake, and shows the error as text and tooltip", () => {
    state.hosts = [
      host("de", [tunnel({ up: false, handshake_age_seconds: null, last_handshake: 0, probe_ms: null, error: "probe timed out" })]),
    ];
    render(<Monitoring />);
    const c = within(card("de"));
    expect(c.getByText("never")).toBeInTheDocument();
    expect(c.getByText("probe timed out")).toBeInTheDocument();
    expect(c.getByText("Down")).toHaveAttribute("title", "probe timed out");
    expect(c.queryByText(/Latency/)).not.toBeInTheDocument();
  });

  it("humanises minutes", () => {
    state.hosts = [host("de", [tunnel({ handshake_age_seconds: 200 })])];
    render(<Monitoring />);
    expect(screen.getByText("3 min ago")).toBeInTheDocument();
  });

  it("shows no tunnel list for a host with no tunnels, and keeps the bare N/M for one without detail", () => {
    state.hosts = [
      host("Panel", null, { node_id: null, address: null }),
      host("old", null, { tunnels_up: 1, tunnels_total: 2 }),
    ];
    render(<Monitoring />);
    expect(within(card("Panel")).queryByText("WireGuard tunnels")).not.toBeInTheDocument();
    expect(within(card("old")).queryAllByRole("listitem")).toHaveLength(0);
    expect(within(card("old")).getByText("1/2")).toBeInTheDocument();
  });

  describe("fleet warning", () => {
    it("lists node: tunnel for every tunnel that is not up, in red when one has no fallback", () => {
      state.hosts = [
        host("de", [tunnel({ name: "wg1", up: false }), tunnel({ name: "wg2" })]),
        host("nl", [tunnel({ name: "wg9", up: false, fallback_active: true })], { node_id: 2 }),
      ];
      render(<Monitoring />);
      const alert = screen.getByRole("alert");
      expect(alert).toHaveTextContent("Tunnels not up: 2 of 3");
      expect(within(alert).getByText("de: wg1")).toBeInTheDocument();
      expect(within(alert).getByText("nl: wg9")).toBeInTheDocument();
      expect(alert.className).toMatch(/red/);
      expect(alert).toHaveTextContent("1 of them are carried by the direct fallback");
      expect(alert).toHaveTextContent("1 have no fallback");
    });

    it("is amber when every down tunnel is covered by the direct fallback", () => {
      state.hosts = [host("de", [tunnel({ up: false, fallback_active: true })])];
      render(<Monitoring />);
      const alert = screen.getByRole("alert");
      expect(alert.className).toMatch(/amber/);
      expect(alert).not.toHaveTextContent("have no fallback");
      expect(screen.getByText("1 using direct fallback")).toBeInTheDocument();
    });

    it("collapses a long list behind a show-all toggle", async () => {
      state.hosts = [host("de", Array.from({ length: 9 }, (_, i) => tunnel({ name: `wg${i}`, up: false })))];
      render(<Monitoring />);
      const alert = screen.getByRole("alert");
      expect(within(alert).getAllByRole("listitem")).toHaveLength(6);
      await userEvent.click(within(alert).getByRole("button", { name: "Show all (9)" }));
      expect(within(alert).getAllByRole("listitem")).toHaveLength(9);
      await userEvent.click(within(alert).getByRole("button", { name: "Show fewer" }));
      expect(within(alert).getAllByRole("listitem")).toHaveLength(6);
    });
  });
});
