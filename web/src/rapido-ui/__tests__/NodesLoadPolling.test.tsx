import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { act, render, screen } from "@testing-library/react";
import i18n from "locales/i18n";
import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { HostsLoad, NodeLoadEntry } from "types/HostLoad";
import { Node } from "types/Node";

// The real useHostsLoadQuery against a faked GET /hosts/load: what is under
// test is that the Nodes page keeps its chips fresh, and hides them when the
// request fails - not the wiring of the mocked hooks the other tests use.
const fetchMock = vi.hoisted(() => vi.fn());
const state = vi.hoisted(() => ({ nodes: [] as unknown[] }));

vi.mock("service/http", () => ({ fetch: fetchMock, fetcher: fetchMock }));
vi.mock("hooks/useNodesQuery", () => ({
  useNodesQuery: () => ({ data: state.nodes, isLoading: false, isError: false }),
  useNodesUsageQuery: () => ({ data: { usages: [] }, isLoading: false, isError: false }),
  useCreateNodeMutation: () => ({ mutate: vi.fn(), isPending: false }),
  useUpdateNodeMutation: () => ({ mutate: vi.fn(), isPending: false }),
  useDeleteNodeMutation: () => ({ mutate: vi.fn(), isPending: false }),
}));
vi.mock("hooks/useInboundsQuery", () => ({ useInboundsQuery: () => ({ data: {} }) }));

import { NodesAdmin } from "../NodesAdmin";

beforeAll(async () => {
  await i18n.changeLanguage("en");
});

const node = (extra: Partial<Node> = {}): Node => ({
  id: 4,
  name: "nod2",
  address: "10.0.0.4",
  port: 62050,
  api_port: 62051,
  status: "connected",
  usage_coefficient: 1,
  ...extra,
});

const entry = (o: Partial<NodeLoadEntry> = {}): NodeLoadEntry => ({
  id: 4,
  name: "nod2",
  conns: 3960,
  capacity: 15000,
  capacity_source: "node",
  percent: 26,
  ...o,
});

const answerWith = (nodes: NodeLoadEntry[]): HostsLoad => ({
  capacity: 10000,
  indicator: true,
  sort_by_load: false,
  updated_at: "2026-09-25T04:00:00Z",
  hosts: [],
  nodes,
});

let current: HostsLoad;
let failing: boolean;

const loadCalls = () => fetchMock.mock.calls.filter((c) => c[0] === "/hosts/load").length;

beforeEach(() => {
  vi.useFakeTimers();
  state.nodes = [node()];
  current = answerWith([entry()]);
  failing = false;
  fetchMock.mockReset();
  fetchMock.mockImplementation((url: string) => {
    if (url === "/hosts/load") {
      return failing ? Promise.reject(new Error("HTTP 500")) : Promise.resolve(structuredClone(current));
    }
    return Promise.reject(new Error(`unexpected request ${url}`));
  });
});

afterEach(() => {
  vi.useRealTimers();
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  delete (document as any).visibilityState;
});

const tick = (ms: number) =>
  act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });

const renderNodes = async () => {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = render(
    <QueryClientProvider client={queryClient}>
      <NodesAdmin />
    </QueryClientProvider>
  );
  await tick(0);
  return view;
};

const chip = () => document.querySelector("[data-level]") as HTMLElement | null;

describe("the Nodes page's load chips", () => {
  it("appear from the first answer", async () => {
    await renderNodes();
    expect(loadCalls()).toBe(1);
    expect(chip()).toHaveTextContent("26%");
    expect(chip()).toHaveAttribute("title", "3960 client connections of 15000 (26%)");
  });

  it("refresh every 5 s, and not before", async () => {
    await renderNodes();
    current = answerWith([entry({ conns: 6000, percent: 40 })]);

    await tick(4900);
    expect(loadCalls()).toBe(1);
    expect(chip()).toHaveTextContent("26%");

    await tick(200);
    expect(loadCalls()).toBe(2);
    expect(chip()).toHaveTextContent("40%");
    expect(chip()!.dataset.level).toBe("normal");

    current = answerWith([entry({ conns: 14000, percent: 93 })]);
    await tick(5000);
    expect(loadCalls()).toBe(3);
    expect(chip()).toHaveTextContent("93%");
    expect(chip()!.dataset.level).toBe("full");
  });

  it("do not poll while the tab is hidden", async () => {
    await renderNodes();
    expect(loadCalls()).toBe(1);

    Object.defineProperty(document, "visibilityState", { configurable: true, get: () => "hidden" });
    await tick(30_000);
    expect(loadCalls()).toBe(1);
  });

  it("are hidden when the request fails, without touching the list, and come back with the next good answer", async () => {
    await renderNodes();
    expect(chip()).not.toBeNull();

    failing = true;
    await tick(5050); // the poll at 5 s, then a moment for the page to redraw
    expect(loadCalls()).toBe(2);
    expect(chip()).toBeNull();
    expect(screen.getByText("nod2")).toBeInTheDocument();
    expect(screen.queryByText("Could not load the nodes.")).not.toBeInTheDocument();

    // A failed poll is not retried in a burst: the next attempt is the next tick.
    await tick(4800);
    expect(loadCalls()).toBe(2);

    failing = false;
    await tick(200);
    expect(loadCalls()).toBe(3);
    expect(chip()).toHaveTextContent("26%");
  });

  it("stay hidden when the very first request fails, and the page still renders", async () => {
    failing = true;
    await renderNodes();
    expect(loadCalls()).toBe(1);
    expect(chip()).toBeNull();
    expect(screen.getByText("nod2")).toBeInTheDocument();
    expect(screen.getByText("Capacity:")).toHaveTextContent("Capacity: Default");
  });

  it("drop the chip of a node that stops reporting, and keep the other nodes' chips", async () => {
    state.nodes = [node({ id: 4, name: "nod2" }), node({ id: 5, name: "nod5" })];
    current = answerWith([entry({ id: 4 }), entry({ id: 5, name: "nod5", percent: 8, conns: 1200 })]);
    await renderNodes();
    expect(document.querySelectorAll("[data-level]")).toHaveLength(2);

    current = answerWith([entry({ id: 5, name: "nod5", percent: 9, conns: 1350 })]);
    await tick(5050);
    expect(document.querySelectorAll("[data-level]")).toHaveLength(1);
    expect(chip()).toHaveTextContent("9%");
  });
});
