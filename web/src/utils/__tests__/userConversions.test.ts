import { describe, expect, it } from "vitest";
import {
  DAY_SECONDS,
  GIB,
  dataLimitToGb,
  daysToExpire,
  expireToDays,
  gbToDataLimit,
} from "../userConversions";

describe("gbToDataLimit", () => {
  it("treats 0, null, and undefined as unlimited (null), not zero bytes", () => {
    expect(gbToDataLimit(0)).toBeNull();
    expect(gbToDataLimit(null)).toBeNull();
    expect(gbToDataLimit(undefined)).toBeNull();
  });

  it("converts a whole GB exactly", () => {
    expect(gbToDataLimit(1)).toBe(GIB);
    expect(gbToDataLimit(2)).toBe(2 * GIB);
  });

  it("rounds a fractional GB to the nearest byte", () => {
    // 0.1 * GIB = 107374182.4 -> rounds to 107374182, not truncates.
    expect(gbToDataLimit(0.1)).toBe(Math.round(0.1 * GIB));
    expect(gbToDataLimit(0.1)).toBe(107374182);
  });

  it("rounds .5-byte boundaries up, matching Math.round", () => {
    // Pick a GB value whose byte count lands exactly on a .5 boundary.
    const gb = 0.5 / GIB; // gb * GIB === 0.5
    expect(gbToDataLimit(gb)).toBe(1);
  });
});

describe("dataLimitToGb", () => {
  it("treats 0 and null/undefined bytes as null, not 0 GB", () => {
    expect(dataLimitToGb(0)).toBeNull();
    expect(dataLimitToGb(null)).toBeNull();
    expect(dataLimitToGb(undefined)).toBeNull();
  });

  it("converts whole and fractional byte counts back to GB", () => {
    expect(dataLimitToGb(GIB)).toBe(1);
    expect(dataLimitToGb(GIB * 2.5)).toBe(2.5);
  });

  it("round-trips with gbToDataLimit for a whole GB", () => {
    expect(dataLimitToGb(gbToDataLimit(5)!)).toBe(5);
  });
});

describe("daysToExpire", () => {
  const now = Date.UTC(2026, 0, 1, 0, 0, 0); // fixed clock, deterministic

  it("treats 0, null, and undefined days as never-expiring (null)", () => {
    expect(daysToExpire(0, now)).toBeNull();
    expect(daysToExpire(null, now)).toBeNull();
    expect(daysToExpire(undefined, now)).toBeNull();
  });

  it("adds exactly N whole days of seconds to the current Unix time", () => {
    const expected = Math.floor(now / 1000) + 30 * DAY_SECONDS;
    expect(daysToExpire(30, now)).toBe(expected);
  });

  it("floors a sub-second `now` before adding, per the exact old formula", () => {
    const fractionalNow = now + 500; // .5 seconds into the millisecond clock
    const expected = Math.floor(fractionalNow / 1000) + 1 * DAY_SECONDS;
    expect(daysToExpire(1, fractionalNow)).toBe(expected);
  });
});

describe("expireToDays", () => {
  const now = Date.UTC(2026, 0, 1, 0, 0, 0);

  it("treats a null/undefined/0 expire as null (never), not 0 days", () => {
    expect(expireToDays(null, now)).toBeNull();
    expect(expireToDays(undefined, now)).toBeNull();
    expect(expireToDays(0, now)).toBeNull();
  });

  it("round-trips with daysToExpire for a positive day count", () => {
    const expire = daysToExpire(45, now);
    expect(expireToDays(expire, now)).toBe(45);
  });

  it("rounds to the nearest day rather than flooring/truncating", () => {
    const nowSeconds = Math.floor(now / 1000);
    // 10.6 days out - rounds to 11, not 10.
    const expire = nowSeconds + Math.round(10.6 * DAY_SECONDS);
    expect(expireToDays(expire, now)).toBe(11);
  });

  it("can come back negative for an already-expired timestamp - not clamped", () => {
    const nowSeconds = Math.floor(now / 1000);
    const pastExpire = nowSeconds - 3 * DAY_SECONDS;
    expect(expireToDays(pastExpire, now)).toBe(-3);
  });
});
