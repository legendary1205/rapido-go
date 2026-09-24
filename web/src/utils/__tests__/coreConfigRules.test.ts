import { describe, expect, it } from "vitest";
import { Outbound, RoutingRule } from "types/CoreConfig";
import {
  isLeafOutboundType,
  otherMatcherSummary,
  outboundSupportsDirectFallback,
  setOutboundBindInterface,
  setOutboundDirectFallback,
  setRuleInboundPorts,
  setRuleInbounds,
  validateCoreConfigDoc,
} from "../coreConfigRules";

describe("isLeafOutboundType", () => {
  it("is false for selector, urltest and block, true for dialing types", () => {
    for (const type of ["selector", "urltest", "block"]) expect(isLeafOutboundType(type)).toBe(false);
    for (const type of ["direct", "socks", "vless", "shadowsocks", "hysteria2"]) {
      expect(isLeafOutboundType(type)).toBe(true);
    }
  });

  it("is false for a missing or non-string type", () => {
    expect(isLeafOutboundType(undefined)).toBe(false);
    expect(isLeafOutboundType("")).toBe(false);
    expect(isLeafOutboundType(3)).toBe(false);
  });
});

describe("outboundSupportsDirectFallback", () => {
  it("needs both a leaf type and a non-blank bind interface", () => {
    expect(outboundSupportsDirectFallback({ type: "direct", bind_interface: "wg0" })).toBe(true);
    expect(outboundSupportsDirectFallback({ type: "direct" })).toBe(false);
    expect(outboundSupportsDirectFallback({ type: "direct", bind_interface: "  " })).toBe(false);
    expect(outboundSupportsDirectFallback({ type: "selector", bind_interface: "wg0" })).toBe(false);
  });
});

describe("setOutboundBindInterface", () => {
  const wg: Outbound = { tag: "wg-de", type: "direct", bind_interface: "wg0", direct_fallback: true };

  it("clears direct_fallback together with the interface", () => {
    expect(setOutboundBindInterface(wg, "")).toEqual({ tag: "wg-de", type: "direct" });
    expect(setOutboundBindInterface(wg, "   ")).toEqual({ tag: "wg-de", type: "direct" });
  });

  it("keeps the flag when the interface just changes, and trims it", () => {
    expect(setOutboundBindInterface(wg, " wg1 ")).toEqual({
      tag: "wg-de",
      type: "direct",
      bind_interface: "wg1",
      direct_fallback: true,
    });
  });

  it("does not mutate its input", () => {
    setOutboundBindInterface(wg, "");
    expect(wg).toEqual({ tag: "wg-de", type: "direct", bind_interface: "wg0", direct_fallback: true });
  });
});

describe("setOutboundDirectFallback", () => {
  it("sets the flag on an eligible outbound and removes the key (never writes false) when off", () => {
    const o: Outbound = { tag: "wg", type: "direct", bind_interface: "wg0" };
    const on = setOutboundDirectFallback(o, true);
    expect(on.direct_fallback).toBe(true);
    const off = setOutboundDirectFallback(on, false);
    expect("direct_fallback" in off).toBe(false);
  });

  it("refuses to set the flag when the outbound is not eligible", () => {
    expect("direct_fallback" in setOutboundDirectFallback({ tag: "x", type: "direct" }, true)).toBe(false);
    expect(
      "direct_fallback" in setOutboundDirectFallback({ tag: "x", type: "selector", bind_interface: "wg0" }, true)
    ).toBe(false);
  });
});

describe("setRuleInbounds / setRuleInboundPorts", () => {
  const rule: RoutingRule = { inbound: ["main"], inbound_port: [20000], outbound_tag: "wg-de" };

  it("drops inbound_port when the last inbound is deselected", () => {
    expect(setRuleInbounds(rule, [])).toEqual({ outbound_tag: "wg-de" });
    expect(setRuleInbounds(rule, ["main", "alt"])).toEqual({
      inbound: ["main", "alt"],
      inbound_port: [20000],
      outbound_tag: "wg-de",
    });
  });

  it("omits inbound_port when the list is empty instead of sending []", () => {
    expect("inbound_port" in setRuleInboundPorts(rule, [])).toBe(false);
    expect(setRuleInboundPorts(rule, [20001, 20002]).inbound_port).toEqual([20001, 20002]);
  });

  it("never sets ports on a rule with no inbound", () => {
    expect("inbound_port" in setRuleInboundPorts({ outbound_tag: "x" }, [20000])).toBe(false);
  });
});

describe("otherMatcherSummary", () => {
  it("lists only the matchers a rule uses, ignoring inbound, inbound_port and outbound_tag", () => {
    expect(
      otherMatcherSummary({
        inbound: ["main"],
        inbound_port: [20000],
        domain_suffix: [".ir"],
        network: ["tcp", "udp"],
        outbound_tag: "wg",
      })
    ).toEqual(["domain_suffix: .ir", "network: tcp, udp"]);
  });

  it("truncates a long value list with a hidden count", () => {
    expect(otherMatcherSummary({ ip_cidr: ["a", "b", "c", "d", "e"], outbound_tag: "x" })).toEqual([
      "ip_cidr: a, b, c (+2)",
    ]);
  });

  it("includes ip_is_private, and is empty for a rule with no other matchers", () => {
    expect(otherMatcherSummary({ ip_is_private: true, outbound_tag: "x" })).toEqual(["ip_is_private"]);
    expect(otherMatcherSummary({ outbound_tag: "x" })).toEqual([]);
  });
});

describe("validateCoreConfigDoc", () => {
  it("finds nothing to say about a document without the new fields", () => {
    expect(
      validateCoreConfigDoc({
        outbounds: [{ tag: "a", type: "direct" }],
        routing_rules: [{ inbound: ["main"], outbound_tag: "a" }],
      })
    ).toEqual([]);
  });

  it("does not throw on non-objects or wrongly shaped sections", () => {
    expect(validateCoreConfigDoc(null)).toEqual([]);
    expect(validateCoreConfigDoc([])).toEqual([]);
    expect(validateCoreConfigDoc({ outbounds: "x", routing_rules: [1, null, "y"] })).toEqual([]);
  });

  it("reports an inbound_port problem with the rule position and its outbound", () => {
    const issues = validateCoreConfigDoc({
      routing_rules: [
        { inbound: ["main"], inbound_port: [20000], outbound_tag: "ok" },
        { inbound: ["main"], inbound_port: [20000, 20000], outbound_tag: "wg-de" },
        { inbound_port: [20001], outbound_tag: "wg-nl" },
      ],
    });
    expect(issues).toEqual([
      { scope: "rule", index: 1, ref: "wg-de", kind: "inboundPortDuplicate", value: "20000" },
      { scope: "rule", index: 2, ref: "wg-nl", kind: "inboundPortNeedsInbound", value: undefined },
    ]);
  });

  it("maps each port problem to its own kind", () => {
    const kind = (inbound_port: unknown) =>
      validateCoreConfigDoc({ routing_rules: [{ inbound: ["m"], inbound_port, outbound_tag: "x" }] })[0]?.kind;
    expect(kind("20000")).toBe("inboundPortNotList");
    expect(kind([1.5])).toBe("inboundPortNotInteger");
    expect(kind([0])).toBe("inboundPortOutOfRange");
    expect(kind([5, 5])).toBe("inboundPortDuplicate");
  });

  it("rejects direct_fallback without a bind interface", () => {
    expect(validateCoreConfigDoc({ outbounds: [{ tag: "a", type: "direct", direct_fallback: true }] })).toEqual([
      { scope: "outbound", index: 0, ref: "a", kind: "directFallbackNeedsInterface" },
    ]);
  });

  it("rejects direct_fallback on selector, urltest and block even with an interface", () => {
    for (const type of ["selector", "urltest", "block"]) {
      expect(
        validateCoreConfigDoc({ outbounds: [{ tag: "g", type, bind_interface: "wg0", direct_fallback: true }] })
      ).toEqual([{ scope: "outbound", index: 0, ref: "g", kind: "directFallbackWrongType", value: type }]);
    }
  });

  it("accepts direct_fallback on a leaf outbound with an interface, and ignores false/null", () => {
    expect(
      validateCoreConfigDoc({
        outbounds: [
          { tag: "a", type: "direct", bind_interface: "wg0", direct_fallback: true },
          { tag: "b", type: "selector", direct_fallback: false },
          { tag: "c", type: "block", direct_fallback: null },
        ],
      })
    ).toEqual([]);
  });

  it("rejects a non-boolean direct_fallback", () => {
    expect(
      validateCoreConfigDoc({ outbounds: [{ tag: "a", type: "direct", bind_interface: "wg0", direct_fallback: "yes" }] })
    ).toEqual([{ scope: "outbound", index: 0, ref: "a", kind: "directFallbackNotBoolean" }]);
  });
});
