import { cleanup, fireEvent, render, screen } from "@testing-library/react";
import i18n from "locales/i18n";
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { Host, HostsMap } from "types/Host";
import { HostLoadEntry, HostsLoad } from "types/HostLoad";

const state = vi.hoisted(() => ({
  hosts: {} as Record<string, unknown[]>,
  load: undefined as unknown,
}));

vi.mock("hooks/useHostsQuery", () => ({
  useHostsQuery: () => ({ data: state.hosts, isLoading: false, isError: false, refetch: vi.fn() }),
  useSaveHostsMutation: () => ({ mutate: vi.fn(), isPending: false }),
}));
vi.mock("hooks/useInboundsQuery", () => ({
  useInboundsQuery: () => ({ data: { vless: ["main"] } }),
}));
vi.mock("hooks/useHostsLoadQuery", () => ({
  useHostsLoadQuery: () => ({ data: state.load }),
}));

import { HostLoadPill } from "../HostLoadPill";
import { HostsAdmin } from "../HostsAdmin";

beforeAll(async () => {
  await i18n.changeLanguage("en");
});

const entry = (o: Partial<HostLoadEntry> = {}): HostLoadEntry => ({
  host_id: 266,
  remark: "video",
  address: "video.ts01.ir",
  port: 20001,
  conns: 312,
  percent: 31,
  level: "free",
  node_ids: [4],
  ...o,
});

describe("HostLoadPill", () => {
  it("shows the percent and the level word, with the open-connection count in the tooltip", () => {
    render(<HostLoadPill load={entry()} capacity={1000} />);
    expect(screen.getByText("31%")).toBeInTheDocument();
    expect(screen.getByText("Free")).toBeInTheDocument();
    expect(screen.getByTitle("312 open connections of 1000 (31%)")).toBeInTheDocument();
  });

  it("uses the singular for one connection", () => {
    render(<HostLoadPill load={entry({ conns: 1, percent: 0 })} capacity={1000} />);
    expect(screen.getByTitle("1 open connection of 1000 (0%)")).toBeInTheDocument();
  });

  it.each([
    ["free", "Free", "text-emerald-400"],
    ["normal", "Normal", "text-yellow-400"],
    ["busy", "Busy", "text-orange-400"],
    ["full", "Full", "text-red-400"],
  ] as const)("colours the %s level", (level, word, cls) => {
    render(<HostLoadPill load={entry({ level, percent: 50 })} capacity={1000} />);
    const pill = screen.getByText(word).closest("[data-level]") as HTMLElement;
    expect(pill.dataset.level).toBe(level);
    expect(pill.className).toContain(cls);
  });

  it("draws the fill as wide as the percent, and never wider than the pill", () => {
    const { container, rerender } = render(<HostLoadPill load={entry({ percent: 42 })} capacity={1000} />);
    const fill = () => container.querySelector("span[aria-hidden]") as HTMLElement;
    expect(fill().style.width).toBe("42%");
    rerender(<HostLoadPill load={entry({ percent: 250 })} capacity={1000} />);
    expect(fill().style.width).toBe("100%");
    expect(screen.getByText("100%")).toBeInTheDocument();
  });

  it("degrades to a neutral 'no data' pill for an unknown level or a missing percent", () => {
    const { rerender } = render(<HostLoadPill load={entry({ level: "unknown" })} capacity={1000} />);
    expect(screen.getByText("No data")).toBeInTheDocument();
    expect(screen.getByText("—")).toBeInTheDocument();
    expect(screen.getByTitle("No load data for this host yet")).toBeInTheDocument();

    // A level from a newer backend must not crash the page.
    rerender(<HostLoadPill load={entry({ level: "scorching" as HostLoadEntry["level"] })} capacity={1000} />);
    expect(screen.getByText("No data")).toBeInTheDocument();

    rerender(<HostLoadPill load={entry({ percent: NaN })} capacity={1000} />);
    expect(screen.getByText("No data")).toBeInTheDocument();
  });
});

describe("HostLoadPill limit tooltip", () => {
  const titleOf = (load: HostLoadEntry, capacity = 10000) => {
    const { container } = render(<HostLoadPill load={load} capacity={capacity} />);
    return (container.querySelector("[data-level]") as HTMLElement).getAttribute("title");
  };

  it("says the percentage comes from the node when the node is the busier limit", () => {
    expect(titleOf(entry({ conns: 312, percent: 45, node_percent: 45, port_percent: 3 }))).toBe(
      "312 open connections on this config\nThe percentage comes from its node (45%), which every config on it shares; this config alone is at 3%."
    );
  });

  it("says it comes from the config itself when that is the busier limit, and gives the node's own figure", () => {
    expect(titleOf(entry({ conns: 4300, percent: 72, node_percent: 26, port_percent: 72 }))).toBe(
      "4300 open connections on this config\nThe percentage comes from this config itself (72%); its node is at 26%."
    );
  });

  it("gives a tie to the config", () => {
    expect(titleOf(entry({ percent: 30, node_percent: 30, port_percent: 30 }))).toContain("comes from this config itself (30%)");
    cleanup();
    expect(titleOf(entry({ percent: 0, node_percent: 0, port_percent: 0, conns: 0 }))).toContain("comes from this config itself (0%)");
  });

  it("rounds a fractional percent the way the pill does", () => {
    expect(titleOf(entry({ percent: 45.6, node_percent: 45.6, port_percent: 2.4 }))).toContain(
      "its node (46%), which every config on it shares; this config alone is at 2%."
    );
  });

  it("uses the singular for one connection", () => {
    expect(titleOf(entry({ conns: 1, percent: 0, node_percent: 0, port_percent: 0 }))).toMatch(/^1 open connection on this config\n/);
  });

  it("stays the plain 'N of capacity' tooltip when the panel does not send both numbers", () => {
    expect(titleOf(entry())).toBe("312 open connections of 10000 (31%)");
    cleanup();
    expect(titleOf(entry({ node_percent: 40 }))).toBe("312 open connections of 10000 (31%)");
    cleanup();
    expect(titleOf(entry({ port_percent: 40 }))).toBe("312 open connections of 10000 (31%)");
    cleanup();
    expect(titleOf(entry({ node_percent: NaN, port_percent: 5 }))).toBe("312 open connections of 10000 (31%)");
  });

  it("does not change the pill itself: same percent, level word and colour as without the numbers", () => {
    const { container } = render(
      <HostLoadPill load={entry({ level: "busy", percent: 75, node_percent: 75, port_percent: 10 })} capacity={10000} />
    );
    const pill = container.querySelector("[data-level]") as HTMLElement;
    expect(pill.dataset.level).toBe("busy");
    expect(pill.className).toContain("text-orange-400");
    expect(pill).toHaveTextContent("75%");
    expect(pill).toHaveTextContent("Busy");
  });

  it("keeps the 'no data' pill for a level it cannot back, whatever the two numbers say", () => {
    expect(titleOf(entry({ level: "unknown", node_percent: 50, port_percent: 10 }))).toBe("No load data for this host yet");
  });

  it("says it in every language", () => {
    for (const lang of ["fa", "ru", "zh"]) {
      const t = i18n.getFixedT(lang);
      const node = t("rapido.hosts.loadLimitNode", { node: 45, port: 3 });
      const config = t("rapido.hosts.loadLimitConfig", { node: 26, port: 72 });
      expect(node, lang).toMatch(/45.*3|3.*45/);
      expect(config, lang).toMatch(/72.*26|26.*72/);
      expect(node).not.toBe(config);
      expect(t("rapido.hosts.loadConns", { count: 312 })).toContain("312");
    }
  });
});

const host = (o: Partial<Host> = {}): Host => ({
  id: 266,
  remark: "video",
  address: "video.ts01.ir",
  port: 20001,
  security: "none",
  alpn: "",
  fingerprint: "",
  mux_enable: false,
  random_user_agent: false,
  use_sni_as_host: false,
  priority: 1,
  ...o,
});

const load = (o: Partial<HostsLoad> = {}): HostsLoad => ({
  capacity: 1000,
  indicator: true,
  sort_by_load: false,
  updated_at: "2026-09-25T04:00:00Z",
  hosts: [entry()],
  ...o,
});

describe("Hosts page load column", () => {
  beforeEach(() => {
    const map: HostsMap = {
      main: [host(), host({ id: 267, remark: "disabled one", is_disabled: true, priority: 2 }), host({ id: undefined, remark: "unsaved", priority: 3 })],
    };
    state.hosts = map;
    state.load = load();
  });

  it("puts the live load pill on the host it belongs to, and only there", () => {
    render(<HostsAdmin />);
    expect(screen.getAllByTitle(/open connections? of/)).toHaveLength(1);
    expect(screen.getByText("31%")).toBeInTheDocument();
  });

  it("explains {LOAD} when the indicator is on", () => {
    render(<HostsAdmin />);
    expect(screen.getByText(/in a host's Remark to choose where its live load indicator appears/)).toBeInTheDocument();
  });

  it("still shows the pills, but no hint, when the indicator is off", () => {
    state.load = load({ indicator: false });
    render(<HostsAdmin />);
    expect(screen.getByText("31%")).toBeInTheDocument();
    expect(screen.queryByText(/live load indicator appears/)).not.toBeInTheDocument();
  });

  it("shows no pill and no hint until the load data has arrived (or when it cannot be loaded)", () => {
    state.load = undefined;
    render(<HostsAdmin />);
    expect(screen.queryByText("31%")).not.toBeInTheDocument();
    expect(screen.queryByText(/live load indicator appears/)).not.toBeInTheDocument();
    // The page itself is unaffected.
    expect(screen.getAllByText("main").length).toBeGreaterThan(0);
  });

  it("lists the load variables in the variables reference", () => {
    render(<HostsAdmin />);
    fireEvent.click(screen.getByRole("button", { name: "Show variables" }));
    expect(screen.getByText("{LOAD}")).toBeInTheDocument();
    expect(screen.getByText("{LOAD_EMOJI}")).toBeInTheDocument();
    expect(screen.getByText("{LOAD_PERCENT}")).toBeInTheDocument();
    expect(screen.getByText("{LOAD_LEVEL}")).toBeInTheDocument();
  });
});
