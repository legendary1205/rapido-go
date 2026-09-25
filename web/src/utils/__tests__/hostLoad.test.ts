import { describe, expect, it } from "vitest";
import { HostsLoad, NodeLoadEntry } from "types/HostLoad";
import { clampPercent, hostLimit, levelForPercent, nodeLoadById } from "../hostLoad";

describe("levelForPercent", () => {
  it("uses the panel's thresholds: free < 40, normal < 70, busy < 90, full from 90", () => {
    expect([0, 39, 39.9].map(levelForPercent)).toEqual(["free", "free", "free"]);
    expect([40, 69, 69.9].map(levelForPercent)).toEqual(["normal", "normal", "normal"]);
    expect([70, 89, 89.9].map(levelForPercent)).toEqual(["busy", "busy", "busy"]);
    expect([90, 100, 250].map(levelForPercent)).toEqual(["full", "full", "full"]);
  });

  it("is unknown for anything that is not a number", () => {
    expect(levelForPercent(NaN)).toBe("unknown");
    expect(levelForPercent(Infinity)).toBe("unknown");
    expect(levelForPercent(undefined as unknown as number)).toBe("unknown");
    expect(levelForPercent("12" as unknown as number)).toBe("unknown");
  });
});

describe("clampPercent", () => {
  it("rounds into 0..100", () => {
    expect(clampPercent(26.4)).toBe(26);
    expect(clampPercent(26.5)).toBe(27);
    expect(clampPercent(-3)).toBe(0);
    expect(clampPercent(180)).toBe(100);
  });
});

describe("hostLimit", () => {
  it("is the node when the node's whole load is the larger of the two", () => {
    expect(hostLimit({ node_percent: 45, port_percent: 3 })).toEqual({ by: "node", node: 45, port: 3 });
  });

  it("is the config when its own connections are the larger", () => {
    expect(hostLimit({ node_percent: 26, port_percent: 72 })).toEqual({ by: "config", node: 26, port: 72 });
  });

  it("gives a tie to the config", () => {
    expect(hostLimit({ node_percent: 30, port_percent: 30 })).toEqual({ by: "config", node: 30, port: 30 });
    expect(hostLimit({ node_percent: 0, port_percent: 0 })).toEqual({ by: "config", node: 0, port: 0 });
  });

  it("compares before rounding, and clamps what it reports", () => {
    expect(hostLimit({ node_percent: 30.4, port_percent: 30.2 })).toEqual({ by: "node", node: 30, port: 30 });
    expect(hostLimit({ node_percent: 140, port_percent: 90 })).toEqual({ by: "node", node: 100, port: 90 });
  });

  it("is null unless the panel sent both numbers", () => {
    expect(hostLimit({})).toBeNull();
    expect(hostLimit({ node_percent: 40 })).toBeNull();
    expect(hostLimit({ port_percent: 40 })).toBeNull();
    expect(hostLimit({ node_percent: NaN, port_percent: 40 })).toBeNull();
    expect(hostLimit({ node_percent: null as unknown as number, port_percent: 40 })).toBeNull();
  });
});

describe("nodeLoadById", () => {
  const entry = (o: Partial<NodeLoadEntry>): NodeLoadEntry => ({
    id: 1,
    name: "n",
    conns: 0,
    capacity: 10000,
    capacity_source: "default",
    percent: 0,
    ...o,
  });

  it("indexes the reporting nodes by id", () => {
    const load = { nodes: [entry({ id: 4, name: "nod2" }), entry({ id: 9, name: "nod5" })] } as HostsLoad;
    const byId = nodeLoadById(load);
    expect(byId.size).toBe(2);
    expect(byId.get(4)?.name).toBe("nod2");
    expect(byId.get(9)?.name).toBe("nod5");
    expect(byId.get(1)).toBeUndefined();
  });

  it("is empty for no answer, a failed one, or a panel that does not report nodes", () => {
    expect(nodeLoadById(undefined).size).toBe(0);
    expect(nodeLoadById(null).size).toBe(0);
    expect(nodeLoadById({} as HostsLoad).size).toBe(0);
    expect(nodeLoadById({ nodes: [] } as unknown as HostsLoad).size).toBe(0);
  });
});
