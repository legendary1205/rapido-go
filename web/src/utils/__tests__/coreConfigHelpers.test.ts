import { describe, expect, it } from "vitest";
import {
  buildOutboundOptions,
  formatIntList,
  formatList,
  moveItem,
  parseCommaIntList,
  parseCommaList,
  summarizeRoutingRule,
  validateDnsServerDraft,
  validateOutboundDraft,
  validateRoutingRuleDraft,
} from "../coreConfigHelpers";
import { DNSServer, Outbound, RoutingRule } from "types/CoreConfig";

describe("parseCommaList", () => {
  it("splits, trims, and drops empty entries", () => {
    expect(parseCommaList(" example.com, ,sub.example.com ,")).toEqual([
      "example.com",
      "sub.example.com",
    ]);
  });

  it("returns an empty array for blank input", () => {
    expect(parseCommaList("   ")).toEqual([]);
  });
});

describe("parseCommaIntList", () => {
  it("parses comma-separated numbers, dropping non-numeric entries", () => {
    expect(parseCommaIntList("443, 8443,, abc, 80")).toEqual([443, 8443, 80]);
  });
});

describe("formatList / formatIntList", () => {
  it("joins with a comma+space, and handles null/undefined as empty", () => {
    expect(formatList(["a", "b"])).toBe("a, b");
    expect(formatList(null)).toBe("");
    expect(formatList(undefined)).toBe("");
    expect(formatIntList([443, 80])).toBe("443, 80");
    expect(formatIntList(undefined)).toBe("");
  });
});

describe("buildOutboundOptions", () => {
  const outbounds: Outbound[] = [
    { tag: "proxy-a", type: "socks" },
    { tag: "proxy-b", type: "selector", outbounds: ["proxy-a"] },
  ];

  it("always leads with the two implicit built-ins", () => {
    const options = buildOutboundOptions([]);
    expect(options).toEqual([
      { value: "direct", implicit: true },
      { value: "block", implicit: true },
    ]);
  });

  it("appends every custom outbound tag after the built-ins", () => {
    const options = buildOutboundOptions(outbounds);
    expect(options.map((o) => o.value)).toEqual(["direct", "block", "proxy-a", "proxy-b"]);
  });

  it("excludes the given tag - a selector/urltest cannot list itself as a member", () => {
    const options = buildOutboundOptions(outbounds, "proxy-b");
    expect(options.map((o) => o.value)).toEqual(["direct", "block", "proxy-a"]);
  });
});

describe("validateOutboundDraft", () => {
  it("requires a non-empty tag", () => {
    expect(validateOutboundDraft({ tag: "", type: "direct" })).toBe(
      "rapido.coreConfig.errorTagRequired"
    );
  });

  it("rejects the reserved tags direct/block", () => {
    expect(validateOutboundDraft({ tag: "direct", type: "socks" })).toBe(
      "rapido.coreConfig.errorTagReserved"
    );
    expect(validateOutboundDraft({ tag: "block", type: "socks" })).toBe(
      "rapido.coreConfig.errorTagReserved"
    );
  });

  it("requires server+server_port for socks/http", () => {
    expect(validateOutboundDraft({ tag: "proxy-a", type: "socks" })).toBe(
      "rapido.coreConfig.errorServerRequired"
    );
    expect(
      validateOutboundDraft({ tag: "proxy-a", type: "http", server: "1.2.3.4" })
    ).toBe("rapido.coreConfig.errorServerRequired");
    expect(
      validateOutboundDraft({
        tag: "proxy-a",
        type: "socks",
        server: "1.2.3.4",
        server_port: 1080,
      })
    ).toBeNull();
  });

  it("requires at least one member for selector/urltest", () => {
    expect(validateOutboundDraft({ tag: "grp", type: "selector", outbounds: [] })).toBe(
      "rapido.coreConfig.errorMemberRequired"
    );
    expect(
      validateOutboundDraft({ tag: "grp", type: "urltest", outbounds: ["direct"] })
    ).toBeNull();
  });

  it("has no extra requirements for direct/block", () => {
    expect(validateOutboundDraft({ tag: "my-direct", type: "direct" })).toBeNull();
  });
});

describe("validateRoutingRuleDraft", () => {
  it("requires an outbound_tag", () => {
    const rule: RoutingRule = { outbound_tag: "" };
    expect(validateRoutingRuleDraft(rule)).toBe("rapido.coreConfig.errorOutboundRequired");
    expect(validateRoutingRuleDraft({ ...rule, outbound_tag: "direct" })).toBeNull();
  });
});

describe("validateDnsServerDraft", () => {
  it("requires a tag", () => {
    const srv: DNSServer = { tag: "", type: "local" };
    expect(validateDnsServerDraft(srv)).toBe("rapido.coreConfig.errorTagRequired");
  });

  it("requires an address for every type except local", () => {
    expect(validateDnsServerDraft({ tag: "dns1", type: "udp" })).toBe(
      "rapido.coreConfig.errorAddressRequired"
    );
    expect(
      validateDnsServerDraft({ tag: "dns1", type: "udp", address: "1.1.1.1" })
    ).toBeNull();
    expect(validateDnsServerDraft({ tag: "dns1", type: "local" })).toBeNull();
  });
});

describe("summarizeRoutingRule", () => {
  it("reports 'match: any' for a rule with no match criteria", () => {
    expect(summarizeRoutingRule({ outbound_tag: "direct" })).toBe("match: any");
  });

  it("joins every populated field, in field order", () => {
    const summary = summarizeRoutingRule({
      domain_suffix: ["example.com"],
      ip_is_private: true,
      port: [443, 8443],
      network: ["tcp"],
      outbound_tag: "proxy-a",
    });
    expect(summary).toBe(
      "domain_suffix: example.com · ip_is_private · port: 443, 8443 · network: tcp"
    );
  });

  it("omits empty-array fields", () => {
    const summary = summarizeRoutingRule({
      domain: [],
      domain_suffix: ["a.com"],
      outbound_tag: "direct",
    });
    expect(summary).toBe("domain_suffix: a.com");
  });
});

describe("moveItem", () => {
  it("swaps with the previous item when moving up", () => {
    expect(moveItem(["a", "b", "c"], 1, -1)).toEqual(["b", "a", "c"]);
  });

  it("swaps with the next item when moving down", () => {
    expect(moveItem(["a", "b", "c"], 1, 1)).toEqual(["a", "c", "b"]);
  });

  it("is a no-op at the top boundary", () => {
    const arr = ["a", "b", "c"];
    expect(moveItem(arr, 0, -1)).toBe(arr);
  });

  it("is a no-op at the bottom boundary", () => {
    const arr = ["a", "b", "c"];
    expect(moveItem(arr, 2, 1)).toBe(arr);
  });

  it("does not mutate the original array", () => {
    const arr = ["a", "b", "c"];
    moveItem(arr, 0, 1);
    expect(arr).toEqual(["a", "b", "c"]);
  });
});
