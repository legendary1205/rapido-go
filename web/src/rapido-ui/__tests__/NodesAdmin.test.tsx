import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import i18n from "locales/i18n";
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { Node, NodeCreateResult } from "types/Node";

const state: { nodes: Node[] } = { nodes: [] };
const createMutate = vi.fn();
const updateMutate = vi.fn();

vi.mock("hooks/useNodesQuery", () => ({
  useNodesQuery: () => ({ data: state.nodes, isLoading: false, isError: false }),
  useNodesUsageQuery: () => ({ data: { usages: [] }, isLoading: false, isError: false }),
  useCreateNodeMutation: () => ({ mutate: createMutate, isPending: false }),
  useUpdateNodeMutation: () => ({ mutate: updateMutate, isPending: false }),
  useDeleteNodeMutation: () => ({ mutate: vi.fn(), isPending: false }),
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
