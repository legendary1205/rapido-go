import { describe, expect, it } from "vitest";
import { Host, HostsMap } from "types/Host";
import {
  addHostToTag,
  emptyHost,
  flattenSortedHosts,
  patchHostAt,
  removeHostAt,
  swapHostPriority,
} from "../hostsReducers";

const host = (remark: string, priority = 0): Host => ({ ...emptyHost(priority), remark });

const sample: HostsMap = {
  "vless-tls": [host("A"), host("B")],
  "vmess-ws": [host("C")],
};

describe("patchHostAt", () => {
  it("edits only the targeted host, leaving its own tag's other hosts untouched", () => {
    const next = patchHostAt(sample, "vless-tls", 0, { remark: "A-edited" });
    expect(next["vless-tls"][0].remark).toBe("A-edited");
    expect(next["vless-tls"][1]).toEqual(sample["vless-tls"][1]);
  });

  it("never touches a sibling tag's array at all - same reference, not just equal content", () => {
    const next = patchHostAt(sample, "vless-tls", 0, { remark: "A-edited" });
    // The untouched tag's array must be the exact same reference: proof
    // nothing iterated into it, not just that its contents happen to match.
    expect(next["vmess-ws"]).toBe(sample["vmess-ws"]);
  });

  it("does not mutate the input map (returns a new object)", () => {
    const before = JSON.stringify(sample);
    patchHostAt(sample, "vless-tls", 0, { remark: "should-not-leak" });
    expect(JSON.stringify(sample)).toBe(before);
  });
});

describe("removeHostAt", () => {
  it("removes only the targeted host from its own tag", () => {
    const next = removeHostAt(sample, "vless-tls", 0);
    expect(next["vless-tls"]).toEqual([sample["vless-tls"][1]]);
  });

  it("never touches a sibling tag's array", () => {
    const next = removeHostAt(sample, "vless-tls", 0);
    expect(next["vmess-ws"]).toBe(sample["vmess-ws"]);
  });
});

describe("addHostToTag", () => {
  it("appends only to the targeted tag", () => {
    const added = host("D");
    const next = addHostToTag(sample, "vmess-ws", added);
    expect(next["vmess-ws"]).toEqual([sample["vmess-ws"][0], added]);
  });

  it("never touches a sibling tag's array", () => {
    const next = addHostToTag(sample, "vmess-ws", host("D"));
    expect(next["vless-tls"]).toBe(sample["vless-tls"]);
  });

  it("defaults security to inbound_default, and leaves alpn/fingerprint blank for the backend's own none default", () => {
    const next = addHostToTag(sample, "vmess-ws");
    const added = next["vmess-ws"][next["vmess-ws"].length - 1];
    expect(added.security).toBe("inbound_default");
    // Matches internal/httpapi/hosts.go's handlePutHosts: an empty
    // alpn/fingerprint on the wire is filled in with "none" server-side, so
    // the blank-host template intentionally leaves them blank rather than
    // duplicating that default client-side.
    expect(added.alpn).toBe("");
    expect(added.fingerprint).toBe("");
  });
});

describe("flattenSortedHosts", () => {
  it("sorts every host across every tag by priority, interleaving tags", () => {
    // Deliberately interleaved priorities across two tags - this is the
    // whole point of the feature: a customer-facing order that mixes
    // configs from different inbound tags/nodes, not grouped by tag.
    const mixed: HostsMap = {
      "node-1": [host("N1-first", 0), host("N1-third", 2)],
      "node-2": [host("N2-second", 1)],
    };
    const flat = flattenSortedHosts(mixed);
    expect(flat.map((f) => f.host.remark)).toEqual(["N1-first", "N2-second", "N1-third"]);
    // Each entry still knows its own tag and its index within that tag's
    // array, since that's what swapHostPriority/patchHostAt address by.
    expect(flat[1].tag).toBe("node-2");
    expect(flat[1].index).toBe(0);
  });

  it("breaks a priority tie by id, for a stable order instead of jittering", () => {
    const tied: HostsMap = {
      a: [{ ...host("X", 5), id: 20 }],
      b: [{ ...host("Y", 5), id: 10 }],
    };
    const flat = flattenSortedHosts(tied);
    expect(flat.map((f) => f.host.remark)).toEqual(["Y", "X"]);
  });
});

describe("swapHostPriority", () => {
  it("swaps priority values across two different tags", () => {
    const mixed: HostsMap = {
      "node-1": [host("N1", 0)],
      "node-2": [host("N2", 1)],
    };
    const next = swapHostPriority(mixed, { tag: "node-1", index: 0 }, { tag: "node-2", index: 0 });
    expect(next["node-1"][0].priority).toBe(1);
    expect(next["node-2"][0].priority).toBe(0);
    // The swap is visible in the flattened, sorted order too - N2 now
    // sorts before N1, proving this is what actually reorders display.
    expect(flattenSortedHosts(next).map((f) => f.host.remark)).toEqual(["N2", "N1"]);
  });

  it("leaves remark/address/every other field untouched - only priority moves", () => {
    const mixed: HostsMap = {
      a: [host("A", 0)],
      b: [host("B", 1)],
    };
    const next = swapHostPriority(mixed, { tag: "a", index: 0 }, { tag: "b", index: 0 });
    expect(next["a"][0].remark).toBe("A");
    expect(next["b"][0].remark).toBe("B");
  });

  it("does not mutate the input map", () => {
    const mixed: HostsMap = { a: [host("A", 0)], b: [host("B", 1)] };
    const before = JSON.stringify(mixed);
    swapHostPriority(mixed, { tag: "a", index: 0 }, { tag: "b", index: 0 });
    expect(JSON.stringify(mixed)).toBe(before);
  });
});

describe("the full-replace save invariant", () => {
  it("every tag present before an edit is still present in the result, so a save body never silently drops an inbound's hosts", () => {
    const edited = patchHostAt(sample, "vless-tls", 1, { address: "1.2.3.4" });
    expect(Object.keys(edited).sort()).toEqual(Object.keys(sample).sort());
    // Every tag's host count is preserved too - a save of `edited` sends
    // PUT /hosts the complete map, matching internal/httpapi/hosts.go's
    // handlePutHosts full-replace-per-tag semantics.
    for (const tag of Object.keys(sample)) {
      expect(edited[tag]).toHaveLength(sample[tag].length);
    }
  });
});
