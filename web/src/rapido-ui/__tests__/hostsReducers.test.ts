import { describe, expect, it } from "vitest";
import { Host, HostsMap } from "types/Host";
import { addHostToTag, emptyHost, patchHostAt, removeHostAt } from "../hostsReducers";

const host = (remark: string): Host => ({ ...emptyHost(), remark });

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
