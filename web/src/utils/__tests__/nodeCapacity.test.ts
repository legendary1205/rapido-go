import { describe, expect, it } from "vitest";
import { MAX_NODE_CAPACITY, MIN_NODE_CAPACITY, formatCapacityInput, parseCapacityInput } from "../nodeCapacity";

describe("parseCapacityInput", () => {
  it("reads empty and whitespace-only input as the panel default (null), not an error", () => {
    expect(parseCapacityInput("")).toEqual({ ok: true, capacity: null });
    expect(parseCapacityInput("   ")).toEqual({ ok: true, capacity: null });
    expect(parseCapacityInput("\n")).toEqual({ ok: true, capacity: null });
  });

  it("accepts whole numbers across the whole range 1..10,000,000", () => {
    expect(MIN_NODE_CAPACITY).toBe(1);
    expect(MAX_NODE_CAPACITY).toBe(10_000_000);
    expect(parseCapacityInput("1")).toEqual({ ok: true, capacity: 1 });
    expect(parseCapacityInput("15000")).toEqual({ ok: true, capacity: 15000 });
    expect(parseCapacityInput("10000000")).toEqual({ ok: true, capacity: 10_000_000 });
    expect(parseCapacityInput("  4300  ")).toEqual({ ok: true, capacity: 4300 });
    expect(parseCapacityInput("007")).toEqual({ ok: true, capacity: 7 });
  });

  it("reads Persian and Arabic-Indic digits as the digits they are", () => {
    expect(parseCapacityInput("۱۵۰۰۰")).toEqual({ ok: true, capacity: 15000 });
    expect(parseCapacityInput("١٥٠٠٠")).toEqual({ ok: true, capacity: 15000 });
    expect(parseCapacityInput("۱2٣")).toEqual({ ok: true, capacity: 123 });
  });

  it("rejects what is outside the range, and says which value", () => {
    expect(parseCapacityInput("0")).toEqual({ ok: false, error: { kind: "outOfRange", value: "0" } });
    expect(parseCapacityInput("10000001")).toEqual({ ok: false, error: { kind: "outOfRange", value: "10000001" } });
    expect(parseCapacityInput("-5")).toEqual({ ok: false, error: { kind: "outOfRange", value: "-5" } });
    expect(parseCapacityInput("99999999999999999999")).toMatchObject({ ok: false, error: { kind: "outOfRange" } });
  });

  it("rejects what is not a whole number instead of guessing", () => {
    for (const bad of ["abc", "1.5", "1e3", "15,000", "15 000", "+5", "0x10", "12k", "--3"]) {
      expect(parseCapacityInput(bad), bad).toEqual({ ok: false, error: { kind: "notInteger", value: bad } });
    }
  });

  it("reports the value as typed, without the surrounding spaces", () => {
    expect(parseCapacityInput("  abc ")).toEqual({ ok: false, error: { kind: "notInteger", value: "abc" } });
  });
});

describe("formatCapacityInput", () => {
  it("is the inverse of the parse, with null and undefined as an empty field", () => {
    expect(formatCapacityInput(15000)).toBe("15000");
    expect(formatCapacityInput(null)).toBe("");
    expect(formatCapacityInput(undefined)).toBe("");
    expect(parseCapacityInput(formatCapacityInput(15000))).toEqual({ ok: true, capacity: 15000 });
    expect(parseCapacityInput(formatCapacityInput(null))).toEqual({ ok: true, capacity: null });
  });
});
