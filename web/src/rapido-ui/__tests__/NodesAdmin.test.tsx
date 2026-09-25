import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import i18n from "locales/i18n";
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { Node, NodeCreateResult } from "types/Node";
import { HostsLoad, NodeLoadEntry } from "types/HostLoad";

const state: { nodes: Node[]; load: HostsLoad | undefined; loadFailed: boolean } = {
  nodes: [],
  load: undefined,
  loadFailed: false,
};
const createMutate = vi.fn();
const updateMutate = vi.fn();

vi.mock("hooks/useNodesQuery", () => ({
  useNodesQuery: () => ({ data: state.nodes, isLoading: false, isError: false }),
  useNodesUsageQuery: () => ({ data: { usages: [] }, isLoading: false, isError: false }),
  useCreateNodeMutation: () => ({ mutate: createMutate, isPending: false }),
  useUpdateNodeMutation: () => ({ mutate: updateMutate, isPending: false }),
  useDeleteNodeMutation: () => ({ mutate: vi.fn(), isPending: false }),
}));

vi.mock("hooks/useHostsLoadQuery", () => ({
  useHostsLoadQuery: () => ({ data: state.load, isError: state.loadFailed }),
}));

vi.mock("hooks/useInboundsQuery", () => ({
  useInboundsQuery: () => ({ data: { vless: ["alpha", "multi"], trojan: ["beta"] } }),
}));

import { NodesAdmin } from "../NodesAdmin";

beforeAll(async () => {
  await i18n.changeLanguage("en");
});

beforeEach(() => {
  createMutate.mockReset();
  updateMutate.mockReset();
  state.nodes = [];
  state.load = undefined;
  state.loadFailed = false;
});

const node = (extra: Partial<Node> = {}): Node => ({
  id: 1,
  name: "edge-1",
  address: "10.0.0.1",
  port: 62050,
  api_port: 62051,
  status: "connected",
  usage_coefficient: 1,
  ...extra,
});

const created = (): NodeCreateResult => ({
  node: node({ name: "fresh" }),
  setup_blob: "QUJDREVGRw==",
  certificate: "CERT",
  key: "KEY",
  ca_certificate: "CA",
  report_secret: "SECRET",
});

const click = (el: HTMLElement) => fireEvent.click(el);
const enter = (el: HTMLElement, value: string) => fireEvent.change(el, { target: { value } });

// Opens the create form and fills the two required fields.
const openCreateForm = () => {
  click(screen.getByRole("button", { name: /Add New Rapido Node/ }));
  enter(screen.getByLabelText("Name"), "fresh");
  enter(screen.getByLabelText("Address"), "203.0.113.5");
};

const openAdvanced = () => click(screen.getByText("Advanced"));

// Values interpolated into translated strings are wrapped in bidi isolates so
// they lay out correctly in Persian; what a person reads is the text without them.
const plain = (el: HTMLElement) => (el.textContent ?? "").replace(/[⁦⁩]/g, "");

const submitButton = () => screen.getByRole("button", { name: "Add Node" });
const portsField = () => screen.getByLabelText(/Listen ports/);
const overridesField = () => screen.getByLabelText(/Core overrides/);

describe("node profile badge", () => {
  it("marks only the nodes that have a custom profile, and says what is custom on hover", () => {
    state.nodes = [
      node({ id: 1, name: "plain" }),
      node({ id: 2, name: "custom", inbound_tags: ["alpha"], listen_ports: [443], core_overrides: { log_level: "debug" } }),
    ];
    render(<NodesAdmin />);
    const badges = screen.getAllByText("Custom profile");
    expect(badges).toHaveLength(1);
    expect(badges[0]).toHaveAttribute("title", "Inbounds: alpha · Ports: 443 · Overrides: log_level");
  });

  it("says 'all' for what is not restricted", () => {
    state.nodes = [node({ core_overrides: { sniff_enabled: false } })];
    render(<NodesAdmin />);
    expect(screen.getByText("Custom profile")).toHaveAttribute(
      "title",
      "Inbounds: all · Ports: all · Overrides: sniff_enabled"
    );
  });
});

describe("Advanced section of the node form", () => {
  it("creates a plain node with exactly the body it always had", () => {
    render(<NodesAdmin />);
    openCreateForm();
    click(submitButton());
    expect(createMutate).toHaveBeenCalledTimes(1);
    const body = createMutate.mock.calls[0][0];
    expect(body).toEqual({ name: "fresh", address: "203.0.113.5", port: 62050, api_port: 62051, usage_coefficient: 1 });
    expect(body).not.toHaveProperty("inbound_tags");
    expect(body).not.toHaveProperty("listen_ports");
    expect(body).not.toHaveProperty("core_overrides");
  });

  it("offers the inbound tags, ports and overrides, and sends what was chosen", () => {
    render(<NodesAdmin />);
    openCreateForm();
    openAdvanced();

    click(screen.getByLabelText("multi"));
    click(screen.getByLabelText("alpha"));
    enter(portsField(), "20002 20000");
    enter(overridesField(), '{"log_level": "debug"}');

    click(submitButton());
    expect(createMutate.mock.calls[0][0]).toMatchObject({
      inbound_tags: ["multi", "alpha"],
      listen_ports: [20002, 20000],
      core_overrides: { log_level: "debug" },
    });
  });

  it("blocks the save and explains a bad port, next to the field", () => {
    render(<NodesAdmin />);
    openCreateForm();
    openAdvanced();
    expect(submitButton()).toBeEnabled();

    enter(portsField(), "443 70000");
    expect(plain(screen.getByRole("alert"))).toBe("70000 is not a valid port (use 1-65535).");
    expect(submitButton()).toBeDisabled();

    enter(portsField(), "443 443");
    expect(plain(screen.getByRole("alert"))).toBe("443 is listed more than once.");

    enter(portsField(), "443 8443");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(submitButton()).toBeEnabled();
  });

  it("validates the overrides JSON inline", () => {
    render(<NodesAdmin />);
    openCreateForm();
    openAdvanced();

    enter(overridesField(), '{"log_level": ');
    expect(screen.getByRole("alert")).toHaveTextContent(/Not valid JSON/);
    expect(submitButton()).toBeDisabled();

    enter(overridesField(), "[]");
    expect(screen.getByRole("alert")).toHaveTextContent("The overrides must be a JSON object");

    enter(overridesField(), '{"outbounds_typo": []}');
    expect(plain(screen.getByRole("alert"))).toBe('Unknown key "outbounds_typo".');

    enter(overridesField(), '{"log_level": "loud"}');
    expect(screen.getByRole("alert")).toHaveTextContent("is not a log level");

    enter(overridesField(), '{"sniff_enabled": false}');
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(submitButton()).toBeEnabled();
  });

  it("does not submit a draft with a problem, and opens the section to show it", () => {
    render(<NodesAdmin />);
    openCreateForm();
    openAdvanced();
    enter(portsField(), "abc");
    click(screen.getByText("Advanced")); // fold it away again
    expect(screen.getByText("Advanced").closest("details")).not.toHaveAttribute("open");
    expect(submitButton()).toBeDisabled();
    expect(createMutate).not.toHaveBeenCalled();
  });

  it("opens itself and shows the panel's rejection under the field it is about", () => {
    createMutate.mockImplementation((_body, opts) =>
      opts.onError({ response: { _data: { detail: "core_overrides: routing rule targets unknown outbound: nope" } } })
    );
    render(<NodesAdmin />);
    openCreateForm();
    openAdvanced();
    enter(overridesField(), '{"routing_rules_first": [{"outbound_tag": "nope"}]}');
    click(screen.getByText("Advanced")); // fold it away: the rejection must reopen it
    click(submitButton());

    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("core_overrides: routing rule targets unknown outbound: nope");
    expect(alert).toHaveAttribute("dir", "ltr");
    // Shown once, under the field - not repeated in a second box at the bottom.
    expect(screen.getAllByText(/routing rule targets unknown outbound/)).toHaveLength(1);
    expect(screen.getByText("Advanced").closest("details")).toHaveAttribute("open");
  });

  it("names the field for a rejected port list and a rejected inbound tag too", () => {
    createMutate.mockImplementation((_body, opts) =>
      opts.onError({ response: { _data: { detail: "listen_ports: invalid port 0 (must be 1-65535)" } } })
    );
    render(<NodesAdmin />);
    openCreateForm();
    click(submitButton());
    expect(screen.getByRole("alert")).toHaveTextContent("listen_ports: invalid port 0");

    createMutate.mockImplementation((_body, opts) =>
      opts.onError({ response: { _data: { detail: "inbound_tags: unknown inbound ghost" } } })
    );
    click(submitButton());
    expect(screen.getByRole("alert")).toHaveTextContent("inbound_tags: unknown inbound ghost");
  });

  it("shows any other rejection in the box at the bottom, as before", () => {
    createMutate.mockImplementation((_body, opts) =>
      opts.onError({ response: { _data: { detail: "A node with this name already exists" } } })
    );
    render(<NodesAdmin />);
    openCreateForm();
    click(submitButton());
    expect(screen.getByText("A node with this name already exists")).toBeInTheDocument();
    expect(screen.getByText("Advanced").closest("details")).not.toHaveAttribute("open");
  });

  it("seeds an edit from the node, opens the section, and sends explicit values so a cleared field is cleared", () => {
    state.nodes = [node({ inbound_tags: ["alpha"], listen_ports: [8443], core_overrides: { log_level: "info" } })];
    render(<NodesAdmin />);
    click(screen.getByRole("button", { name: "Edit" }));

    expect(screen.getByText("Advanced").closest("details")).toHaveAttribute("open");
    expect(screen.getByLabelText("alpha")).toBeChecked();
    expect(screen.getByLabelText("multi")).not.toBeChecked();
    expect(portsField()).toHaveValue("8443");
    expect(overridesField()).toHaveValue(JSON.stringify({ log_level: "info" }, null, 2));

    click(screen.getByLabelText("alpha"));
    enter(portsField(), "");
    enter(overridesField(), "");
    click(screen.getByRole("button", { name: "Update Node" }));

    expect(updateMutate).toHaveBeenCalledTimes(1);
    expect(updateMutate.mock.calls[0][0]).toMatchObject({
      id: 1,
      body: { inbound_tags: [], listen_ports: [], core_overrides: {} },
    });
  });

  it("keeps a tag the node names but the panel no longer has, so it can be unticked", () => {
    state.nodes = [node({ inbound_tags: ["gone"] })];
    render(<NodesAdmin />);
    click(screen.getByRole("button", { name: "Edit" }));
    expect(screen.getByLabelText(/gone/)).toBeChecked();
    expect(screen.getByText("(no longer exists)")).toBeInTheDocument();
  });
});

describe("the reveal panel of a newly created node", () => {
  const command = () => `curl -fsSL ${window.location.origin}/install/node.sh | sudo bash -s -- 'QUJDREVGRw=='`;

  it("leads with the install command, built from this panel's origin and the setup blob", () => {
    createMutate.mockImplementation((_body, opts) => opts.onSuccess(created()));
    render(<NodesAdmin />);
    openCreateForm();
    click(submitButton());

    expect(screen.getByText("Install on the server")).toBeInTheDocument();
    const box = screen.getByLabelText("Install command") as HTMLTextAreaElement;
    expect(box.value).toBe(command());
    expect(box).toHaveAttribute("dir", "ltr");
    // The blob itself is still offered below it.
    expect(screen.getByLabelText("Setup blob")).toHaveValue("QUJDREVGRw==");
  });

  it("copies exactly that command", async () => {
    createMutate.mockImplementation((_body, opts) => opts.onSuccess(created()));
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    render(<NodesAdmin />);
    openCreateForm();
    click(submitButton());

    click(screen.getAllByRole("button", { name: "Copy" })[0]);
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(command()));
    await screen.findByText("Copied");
  });
});

// ---------------------------------------------------------------------------
// Per-node capacity

const capacityField = () => screen.getByLabelText(/Capacity \(client connections/);
const capacityHint = "Leave empty to use the panel default.";

const nodeLoad = (o: Partial<NodeLoadEntry> = {}): NodeLoadEntry => ({
  id: 1,
  name: "edge-1",
  conns: 3960,
  capacity: 15000,
  capacity_source: "node",
  percent: 26,
  ...o,
});

const loadOf = (o: Partial<HostsLoad> = {}): HostsLoad => ({
  capacity: 10000,
  indicator: true,
  sort_by_load: false,
  updated_at: "2026-09-25T04:00:00Z",
  hosts: [],
  nodes: [nodeLoad()],
  ...o,
});

describe("capacity field of the node form", () => {
  it("sits in the Advanced section, empty, with a one-line hint about the panel default", () => {
    render(<NodesAdmin />);
    openCreateForm();
    expect(screen.getByText("Advanced").closest("details")).not.toHaveAttribute("open");
    openAdvanced();
    expect(capacityField()).toHaveValue("");
    expect(capacityField()).toHaveAttribute("dir", "ltr");
    expect(screen.getByText(capacityHint)).toBeInTheDocument();
  });

  it("shows the panel default as the placeholder when the panel reports it", () => {
    state.load = loadOf({ capacity: 10000 });
    render(<NodesAdmin />);
    openCreateForm();
    expect(capacityField()).toHaveAttribute("placeholder", "10000");
  });

  it("has no placeholder before the panel has reported a default", () => {
    render(<NodesAdmin />);
    openCreateForm();
    expect(capacityField()).not.toHaveAttribute("placeholder");
  });

  it("has no placeholder from an older panel, whose capacity is per config and not a node default", () => {
    state.load = loadOf({ nodes: undefined, capacity: 1000 });
    render(<NodesAdmin />);
    openCreateForm();
    expect(capacityField()).not.toHaveAttribute("placeholder");
  });

  it("creates a node on the panel default without any capacity key", () => {
    render(<NodesAdmin />);
    openCreateForm();
    click(submitButton());
    expect(createMutate.mock.calls[0][0]).not.toHaveProperty("capacity");
  });

  it("sends a typed capacity as a number under `capacity`", () => {
    render(<NodesAdmin />);
    openCreateForm();
    openAdvanced();
    enter(capacityField(), "15000");
    click(submitButton());
    expect(createMutate.mock.calls[0][0]).toMatchObject({ name: "fresh", capacity: 15000 });
    expect(typeof createMutate.mock.calls[0][0].capacity).toBe("number");
  });

  it("reads a Persian keyboard's digits and ignores surrounding spaces", () => {
    render(<NodesAdmin />);
    openCreateForm();
    openAdvanced();
    enter(capacityField(), " ۱۵۰۰۰ ");
    click(submitButton());
    expect(createMutate.mock.calls[0][0]).toMatchObject({ capacity: 15000 });
  });

  it.each([["1"], ["10000000"]])("accepts %s, the edge of the allowed range", (value) => {
    render(<NodesAdmin />);
    openCreateForm();
    openAdvanced();
    enter(capacityField(), value);
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(submitButton()).toBeEnabled();
    click(submitButton());
    expect(createMutate.mock.calls[0][0]).toMatchObject({ capacity: Number(value) });
  });

  it.each([
    ["0", "0 is not a valid capacity (use 1-10,000,000)."],
    ["10000001", "10000001 is not a valid capacity (use 1-10,000,000)."],
    ["-3", "-3 is not a valid capacity (use 1-10,000,000)."],
    ["1.5", "\"1.5\" is not a whole number."],
    ["abc", "\"abc\" is not a whole number."],
    ["15,000", "\"15,000\" is not a whole number."],
  ])("blocks the save of %s and says why, next to the field", (value, message) => {
    render(<NodesAdmin />);
    openCreateForm();
    openAdvanced();
    enter(capacityField(), value);
    expect(plain(screen.getByRole("alert"))).toBe(message);
    expect(capacityField()).toHaveAttribute("aria-invalid", "true");
    expect(submitButton()).toBeDisabled();
    expect(screen.queryByText(capacityHint)).not.toBeInTheDocument();
    expect(createMutate).not.toHaveBeenCalled();

    enter(capacityField(), "");
    expect(screen.queryByRole("alert")).not.toBeInTheDocument();
    expect(submitButton()).toBeEnabled();
  });

  it("opens itself, seeded from the node, when the node has a capacity of its own", () => {
    state.nodes = [node({ capacity: 15000 })];
    render(<NodesAdmin />);
    click(screen.getByRole("button", { name: "Edit" }));
    expect(screen.getByText("Advanced").closest("details")).toHaveAttribute("open");
    expect(capacityField()).toHaveValue("15000");
  });

  it("stays folded for a node on the panel default", () => {
    state.nodes = [node({ capacity: null })];
    render(<NodesAdmin />);
    click(screen.getByRole("button", { name: "Edit" }));
    expect(screen.getByText("Advanced").closest("details")).not.toHaveAttribute("open");
    openAdvanced();
    expect(capacityField()).toHaveValue("");
  });

  it("keeps the node's capacity on a save that does not touch it", () => {
    state.nodes = [node({ capacity: 15000 })];
    render(<NodesAdmin />);
    click(screen.getByRole("button", { name: "Edit" }));
    click(screen.getByRole("button", { name: "Update Node" }));
    expect(updateMutate.mock.calls[0][0].body).toMatchObject({ capacity: 15000 });
  });

  it("changes it, and clears it back to the default with an explicit null", () => {
    state.nodes = [node({ capacity: 15000 })];
    render(<NodesAdmin />);
    click(screen.getByRole("button", { name: "Edit" }));

    enter(capacityField(), "20000");
    click(screen.getByRole("button", { name: "Update Node" }));
    expect(updateMutate.mock.calls[0][0]).toMatchObject({ id: 1, body: { capacity: 20000 } });

    enter(capacityField(), "");
    click(screen.getByRole("button", { name: "Update Node" }));
    expect(updateMutate.mock.calls[1][0].body).toHaveProperty("capacity", null);
  });

  it("always says which it means on an update: a node on the default sends capacity null", () => {
    state.nodes = [node()];
    render(<NodesAdmin />);
    click(screen.getByRole("button", { name: "Edit" }));
    click(screen.getByRole("button", { name: "Update Node" }));
    expect(updateMutate.mock.calls[0][0].body).toHaveProperty("capacity", null);
  });

  it("shows the panel's rejection under the field it is about, and opens the section for it", () => {
    createMutate.mockImplementation((_body, opts) =>
      opts.onError({ response: { _data: { detail: "capacity: must be between 1 and 10000000" } } })
    );
    render(<NodesAdmin />);
    openCreateForm();
    click(submitButton());

    const alert = screen.getByRole("alert");
    expect(alert).toHaveTextContent("capacity: must be between 1 and 10000000");
    expect(alert).toHaveAttribute("dir", "ltr");
    // Once, under the field - not repeated at the bottom.
    expect(screen.getAllByText(/must be between 1 and 10000000/)).toHaveLength(1);
    expect(screen.getByText("Advanced").closest("details")).toHaveAttribute("open");
  });
});

describe("capacity in the node's row", () => {
  const rowText = () => plain(screen.getByText("Capacity:") as HTMLElement);

  it("shows the node's own capacity", () => {
    state.nodes = [node({ capacity: 15000 })];
    render(<NodesAdmin />);
    expect(rowText()).toBe("Capacity: 15000");
  });

  it("says 'Default' for a node without one", () => {
    state.nodes = [node({ capacity: null })];
    render(<NodesAdmin />);
    expect(rowText()).toBe("Capacity: Default");
  });

  it("adds the panel default's number once the panel has reported it", () => {
    state.nodes = [node({ capacity: null })];
    state.load = loadOf({ capacity: 10000 });
    render(<NodesAdmin />);
    expect(rowText()).toBe("Capacity: Default (10000)");
  });

  it("does not pass an older panel's per-config capacity off as the node default", () => {
    state.nodes = [node()];
    state.load = loadOf({ nodes: undefined, capacity: 1000 });
    render(<NodesAdmin />);
    expect(rowText()).toBe("Capacity: Default");
  });

  it("does not touch the capacity when a node is only enabled or disabled from its row", () => {
    state.nodes = [node({ capacity: 15000 })];
    render(<NodesAdmin />);
    click(screen.getByRole("button", { name: "Disable" }));
    expect(updateMutate.mock.calls[0][0].body).not.toHaveProperty("capacity");
  });
});

describe("node load chip", () => {
  const chip = () => document.querySelector("[data-level]") as HTMLElement | null;

  it("shows the node's percent and level word, with the numbers in the tooltip", () => {
    state.nodes = [node()];
    state.load = loadOf();
    render(<NodesAdmin />);
    const el = chip()!;
    expect(el).not.toBeNull();
    expect(el.textContent).toContain("26%");
    expect(el.textContent).toContain("Free");
    expect(el).toHaveAttribute("title", "3960 client connections of 15000 (26%)");
  });

  it("is on the node it belongs to, matched by id, and only there", () => {
    state.nodes = [node({ id: 1, name: "edge-1" }), node({ id: 2, name: "edge-2" }), node({ id: 3, name: "edge-3" })];
    state.load = loadOf({
      nodes: [nodeLoad({ id: 1, percent: 26 }), nodeLoad({ id: 3, name: "edge-3", percent: 74, conns: 11100 })],
    });
    render(<NodesAdmin />);
    expect(document.querySelectorAll("[data-level]")).toHaveLength(2);
    const cardOf = (name: string) => screen.getByText(name).closest("div.p-4") as HTMLElement;
    expect(cardOf("edge-1").querySelector("[data-level]")).toHaveTextContent("26%");
    expect(cardOf("edge-2").querySelector("[data-level]")).toBeNull();
    expect(cardOf("edge-3").querySelector("[data-level]")).toHaveTextContent("74%");
  });

  it.each([
    [15, "free", "Free", "text-emerald-400"],
    [55, "normal", "Normal", "text-yellow-400"],
    [75, "busy", "Busy", "text-orange-400"],
    [95, "full", "Full", "text-red-400"],
  ] as const)("colours %i%% as %s, the same way a host pill does", (percent, level, word, cls) => {
    state.nodes = [node()];
    state.load = loadOf({ nodes: [nodeLoad({ percent })] });
    render(<NodesAdmin />);
    const el = chip()!;
    expect(el.dataset.level).toBe(level);
    expect(el.className).toContain(cls);
    expect(el).toHaveTextContent(word);
  });

  it("switches level exactly at 40, 70 and 90", () => {
    state.nodes = [node()];
    const levelAt = (percent: number) => {
      state.load = loadOf({ nodes: [nodeLoad({ percent })] });
      const { unmount } = render(<NodesAdmin />);
      const level = chip()!.dataset.level;
      unmount();
      return level;
    };
    expect([39, 40, 69, 70, 89, 90].map(levelAt)).toEqual(["free", "normal", "normal", "busy", "busy", "full"]);
  });

  it("marks a default capacity in the tooltip, and only then", () => {
    state.nodes = [node()];
    state.load = loadOf({ nodes: [nodeLoad({ capacity: 10000, capacity_source: "default", conns: 2600 })] });
    const { unmount } = render(<NodesAdmin />);
    expect(chip()).toHaveAttribute("title", "2600 client connections of 10000 (26%) · default capacity");
    unmount();

    state.load = loadOf({ nodes: [nodeLoad({ capacity_source: "node" })] });
    render(<NodesAdmin />);
    expect(chip()!.getAttribute("title")).not.toContain("default");
  });

  it("uses the singular for one connection", () => {
    state.nodes = [node()];
    state.load = loadOf({ nodes: [nodeLoad({ conns: 1, percent: 0 })] });
    render(<NodesAdmin />);
    expect(chip()).toHaveAttribute("title", "1 client connection of 15000 (0%)");
  });

  it("never draws past 100%, whatever the panel sends", () => {
    state.nodes = [node()];
    state.load = loadOf({ nodes: [nodeLoad({ percent: 180, conns: 27000 })] });
    render(<NodesAdmin />);
    expect(chip()).toHaveTextContent("100%");
    expect((chip()!.querySelector("span[aria-hidden]") as HTMLElement).style.width).toBe("100%");
    expect(chip()!.dataset.level).toBe("full");
  });

  it("is hidden for a node whose percent is not a number", () => {
    state.nodes = [node()];
    state.load = loadOf({ nodes: [nodeLoad({ percent: NaN })] });
    render(<NodesAdmin />);
    expect(chip()).toBeNull();
  });

  it("is hidden when the load request fails, even if an earlier answer is still cached", () => {
    state.nodes = [node()];
    state.load = loadOf();
    state.loadFailed = true;
    render(<NodesAdmin />);
    expect(chip()).toBeNull();
    // The list itself is untouched, and no default is claimed from stale data.
    expect(screen.getByText("edge-1")).toBeInTheDocument();
    expect(screen.queryByText("Could not load the nodes.")).not.toBeInTheDocument();
    expect(plain(screen.getByText("Capacity:") as HTMLElement)).toBe("Capacity: Default");
  });

  it("is absent until the first answer, and from a panel that does not report nodes", () => {
    state.nodes = [node()];
    const { unmount } = render(<NodesAdmin />);
    expect(chip()).toBeNull();
    unmount();

    state.load = loadOf({ nodes: undefined });
    render(<NodesAdmin />);
    expect(chip()).toBeNull();
  });
});
