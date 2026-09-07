import { describe, expect, it } from "vitest";
import {
  buildOutboundOptions,
  formatIntList,
  formatList,
  moveItem,
  outboundIsProxyType,
  outboundTLSIsMandatory,
  parseCommaIntList,
  parseCommaList,
  parseFullConfigJSON,
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

  it("requires server+port and a password for shadowsocks/trojan/hysteria2", () => {
    for (const type of ["shadowsocks", "trojan", "hysteria2"] as const) {
      expect(validateOutboundDraft({ tag: "ob", type, server: "1.2.3.4", server_port: 443 })).toBe(
        "rapido.coreConfig.errorPasswordRequired"
      );
      expect(
        validateOutboundDraft({ tag: "ob", type, server: "1.2.3.4", server_port: 443, password: "pw" })
      ).toBeNull();
    }
  });

  it("requires a uuid for vmess/vless", () => {
    for (const type of ["vmess", "vless"] as const) {
      expect(validateOutboundDraft({ tag: "ob", type, server: "1.2.3.4", server_port: 443 })).toBe(
        "rapido.coreConfig.errorUuidRequired"
      );
      expect(
        validateOutboundDraft({ tag: "ob", type, server: "1.2.3.4", server_port: 443, uuid: "u" })
      ).toBeNull();
    }
  });

  it("requires both uuid and password for tuic", () => {
    expect(validateOutboundDraft({ tag: "ob", type: "tuic", server: "1.2.3.4", server_port: 443 })).toBe(
      "rapido.coreConfig.errorUuidAndPasswordRequired"
    );
    expect(
      validateOutboundDraft({
        tag: "ob",
        type: "tuic",
        server: "1.2.3.4",
        server_port: 443,
        uuid: "u",
        password: "pw",
      })
    ).toBeNull();
  });
});

describe("outboundIsProxyType / outboundTLSIsMandatory", () => {
  it("flags every server-dialing type as a proxy type, and only hysteria2/tuic as TLS-mandatory", () => {
    const proxyTypes: Outbound["type"][] = [
      "socks",
      "http",
      "shadowsocks",
      "vmess",
      "trojan",
      "vless",
      "hysteria2",
      "tuic",
    ];
    for (const type of proxyTypes) expect(outboundIsProxyType(type)).toBe(true);
    expect(outboundIsProxyType("direct")).toBe(false);
    expect(outboundIsProxyType("selector")).toBe(false);

    expect(outboundTLSIsMandatory("hysteria2")).toBe(true);
    expect(outboundTLSIsMandatory("tuic")).toBe(true);
    expect(outboundTLSIsMandatory("vmess")).toBe(false);
    expect(outboundTLSIsMandatory("trojan")).toBe(false);
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

  it("puts inbound first, ahead of domain/ip/port criteria", () => {
    const summary = summarizeRoutingRule({
      inbound: ["node1", "node2"],
      domain_suffix: ["example.com"],
      outbound_tag: "germany",
    });
    expect(summary).toBe("inbound: node1, node2 · domain_suffix: example.com");
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

describe("parseFullConfigJSON", () => {
  it("round-trips a full config unchanged", () => {
    const config = {
      log_level: "debug",
      sniff_enabled: false,
      outbounds: [{ tag: "up", type: "socks", server: "1.2.3.4", server_port: 1080 }],
      routing_rules: [{ ip_is_private: true, outbound_tag: "block" }],
      dns_servers: [{ tag: "d", type: "udp", address: "1.1.1.1" }],
      updated_at: "2026-01-01T00:00:00Z",
    };
    expect(parseFullConfigJSON(JSON.stringify(config))).toEqual(config);
  });

  it("defaults missing list fields to empty arrays instead of throwing", () => {
    const parsed = parseFullConfigJSON(JSON.stringify({ log_level: "warn", sniff_enabled: true }));
    expect(parsed.outbounds).toEqual([]);
    expect(parsed.routing_rules).toEqual([]);
    expect(parsed.dns_servers).toEqual([]);
  });

  it("defaults a missing log_level to warn and sniff_enabled to false", () => {
    const parsed = parseFullConfigJSON("{}");
    expect(parsed.log_level).toBe("warn");
    expect(parsed.sniff_enabled).toBe(false);
  });

  it("throws on invalid JSON syntax", () => {
    expect(() => parseFullConfigJSON("{not valid json")).toThrow();
  });

  it("throws on a valid-JSON non-object (e.g. an array or a string)", () => {
    expect(() => parseFullConfigJSON("[1,2,3]")).toThrow();
    expect(() => parseFullConfigJSON('"just a string"')).toThrow();
    expect(() => parseFullConfigJSON("null")).toThrow();
  });
});
