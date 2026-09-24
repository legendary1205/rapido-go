import { describe, expect, it } from "vitest";
import {
  formatInboundPorts,
  parseInboundPortInput,
  portsByTag,
  summarizePorts,
  validateInboundPortField,
} from "../inboundPorts";

describe("parseInboundPortInput", () => {
  it("treats empty and whitespace-only input as no restriction, not an error", () => {
    expect(parseInboundPortInput("")).toEqual({ ok: true, ports: [] });
    expect(parseInboundPortInput("  \n ")).toEqual({ ok: true, ports: [] });
  });

  it("accepts commas, spaces, semicolons and mixtures of them, keeping typed order", () => {
    expect(parseInboundPortInput("20002, 20000 20001;20003,,  20004")).toEqual({
      ok: true,
      ports: [20002, 20000, 20001, 20003, 20004],
    });
  });

  it("accepts the boundary ports 1 and 65535", () => {
    expect(parseInboundPortInput("1 65535")).toEqual({ ok: true, ports: [1, 65535] });
  });

  it("rejects 0, values above 65535, and absurdly long numbers as out of range", () => {
    expect(parseInboundPortInput("0")).toEqual({ ok: false, error: { kind: "outOfRange", value: "0" } });
    expect(parseInboundPortInput("20000 65536")).toEqual({
      ok: false,
      error: { kind: "outOfRange", value: "65536" },
    });
    expect(parseInboundPortInput("9".repeat(400))).toMatchObject({ ok: false, error: { kind: "outOfRange" } });
  });

  it("rejects anything that is not a whole number and names the token", () => {
    expect(parseInboundPortInput("20000, abc")).toEqual({ ok: false, error: { kind: "notInteger", value: "abc" } });
    expect(parseInboundPortInput("80.5")).toEqual({ ok: false, error: { kind: "notInteger", value: "80.5" } });
    expect(parseInboundPortInput("-5")).toEqual({ ok: false, error: { kind: "notInteger", value: "-5" } });
    expect(parseInboundPortInput("20000-20005")).toEqual({
      ok: false,
      error: { kind: "notInteger", value: "20000-20005" },
    });
  });

  it("rejects duplicates, including ones that differ only by leading zeros", () => {
    expect(parseInboundPortInput("20000, 20001, 20000")).toEqual({
      ok: false,
      error: { kind: "duplicate", value: "20000" },
    });
    expect(parseInboundPortInput("443 0443")).toEqual({ ok: false, error: { kind: "duplicate", value: "0443" } });
  });

  it("reads Persian and Arabic-Indic digits and the Arabic comma", () => {
    expect(parseInboundPortInput("۲۰۰۰۰، ٢٠٠٠١")).toEqual({ ok: true, ports: [20000, 20001] });
  });
});

describe("formatInboundPorts", () => {
  it("joins with a comma and space, and is empty for nothing", () => {
    expect(formatInboundPorts([20000, 20001])).toBe("20000, 20001");
    expect(formatInboundPorts([])).toBe("");
    expect(formatInboundPorts(undefined)).toBe("");
    expect(formatInboundPorts(null)).toBe("");
  });

  it("round-trips through parseInboundPortInput", () => {
    const ports = [20005, 20001, 443];
    expect(parseInboundPortInput(formatInboundPorts(ports))).toEqual({ ok: true, ports });
  });
});

describe("summarizePorts", () => {
  const range = (from: number, n: number) => Array.from({ length: n }, (_, i) => from + i);

  it("shows a short list in full, untruncated", () => {
    expect(summarizePorts([20000, 20001])).toEqual({ text: "20000, 20001", total: 2, truncated: false });
    expect(summarizePorts(range(20000, 5))).toMatchObject({ truncated: false, total: 5 });
  });

  it("cuts a long list to maxShown with an ellipsis and keeps the total", () => {
    expect(summarizePorts(range(20000, 15), 3)).toEqual({
      text: "20000, 20001, 20002, …",
      total: 15,
      truncated: true,
    });
  });

  it("handles a single port, an empty list and null", () => {
    expect(summarizePorts([443])).toEqual({ text: "443", total: 1, truncated: false });
    expect(summarizePorts([])).toEqual({ text: "", total: 0, truncated: false });
    expect(summarizePorts(null)).toEqual({ text: "", total: 0, truncated: false });
  });
});

describe("portsByTag", () => {
  it("uses ports when present, including for several protocols", () => {
    expect(
      portsByTag({
        vless: [{ tag: "main", port: 20000, ports: [20000, 20001, 20002] }],
        trojan: [{ tag: "t", port: 443, ports: [443] }],
      })
    ).toEqual({ main: [20000, 20001, 20002], t: [443] });
  });

  it("falls back to the single port for a backend without ports, and to none when there is no host", () => {
    expect(portsByTag({ vless: [{ tag: "old", port: 8443 }, { tag: "nohost", port: 0 }] })).toEqual({
      old: [8443],
      nohost: [],
    });
  });

  it("ignores bare tag strings and tolerates null/empty input", () => {
    expect(portsByTag({ vless: ["just-a-tag"] })).toEqual({});
    expect(portsByTag(null)).toEqual({});
    expect(portsByTag({})).toEqual({});
  });
});

describe("validateInboundPortField", () => {
  const inbound = ["main"];

  it("accepts an absent, null or empty inbound_port even without an inbound", () => {
    expect(validateInboundPortField(undefined, undefined)).toBeNull();
    expect(validateInboundPortField(null, undefined)).toBeNull();
    expect(validateInboundPortField([], undefined)).toBeNull();
  });

  it("accepts valid ports on a rule that has an inbound", () => {
    expect(validateInboundPortField([20000, 20001], inbound)).toBeNull();
  });

  it("requires a non-empty inbound", () => {
    expect(validateInboundPortField([20000], undefined)).toEqual({ kind: "needsInbound" });
    expect(validateInboundPortField([20000], [])).toEqual({ kind: "needsInbound" });
  });

  it("rejects a value that is not a list", () => {
    expect(validateInboundPortField("20000", inbound)).toEqual({ kind: "notList" });
    expect(validateInboundPortField(20000, inbound)).toEqual({ kind: "notList" });
  });

  it("rejects non-integers, including numeric strings, and names the value", () => {
    expect(validateInboundPortField(["20000"], inbound)).toEqual({ kind: "notInteger", value: "20000" });
    expect(validateInboundPortField([1.5], inbound)).toEqual({ kind: "notInteger", value: "1.5" });
    expect(validateInboundPortField([null], inbound)).toEqual({ kind: "notInteger", value: "null" });
  });

  it("rejects out-of-range ports and duplicates", () => {
    expect(validateInboundPortField([0], inbound)).toEqual({ kind: "outOfRange", value: "0" });
    expect(validateInboundPortField([70000], inbound)).toEqual({ kind: "outOfRange", value: "70000" });
    expect(validateInboundPortField([20000, 20000], inbound)).toEqual({ kind: "duplicate", value: "20000" });
  });
});
