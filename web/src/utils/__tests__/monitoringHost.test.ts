import { describe, expect, it } from "vitest";
import { formatRate, hostDisplayState, hostTone, meterTone } from "../monitoringHost";

const host = (overrides: Partial<{ reachable: boolean; stale: boolean; healthy: boolean }> = {}) => ({
  reachable: true,
  stale: false,
  healthy: true,
  ...overrides,
});

describe("hostDisplayState", () => {
  it("is no-data when the host has never reported, even if stale/healthy say otherwise", () => {
    expect(hostDisplayState(host({ reachable: false, stale: false, healthy: true }))).toBe("no-data");
  });

  it("is stale when the last report is too old, even if it was healthy", () => {
    expect(hostDisplayState(host({ stale: true, healthy: true }))).toBe("stale");
  });

  it("is unhealthy when reachable, fresh, but reporting unhealthy", () => {
    expect(hostDisplayState(host({ stale: false, healthy: false }))).toBe("unhealthy");
  });

  it("is healthy when reachable, fresh, and healthy", () => {
    expect(hostDisplayState(host())).toBe("healthy");
  });

  it("prioritizes no-data over stale", () => {
    expect(hostDisplayState(host({ reachable: false, stale: true }))).toBe("no-data");
  });
});

describe("hostTone", () => {
  it("maps each display state to its badge tone", () => {
    expect(hostTone(host())).toBe("green");
    expect(hostTone(host({ stale: true }))).toBe("yellow");
    expect(hostTone(host({ healthy: false }))).toBe("red");
    expect(hostTone(host({ reachable: false }))).toBe("gray");
  });
});

describe("formatRate", () => {
  it("renders an em dash for null (no data), not 0 B/s", () => {
    expect(formatRate(null)).toBe("—");
  });

  it("renders an em dash for undefined", () => {
    expect(formatRate(undefined)).toBe("—");
  });

  it("formats a real rate with a per-second suffix", () => {
    expect(formatRate(1024)).toBe("1 KB/s");
  });

  it("formats zero as 0 B/s, not an em dash", () => {
    expect(formatRate(0)).toBe("0 B/s");
  });
});

describe("meterTone", () => {
  it("is empty for null/undefined, not a false-healthy green", () => {
    expect(meterTone(null)).toBe("empty");
    expect(meterTone(undefined)).toBe("empty");
  });

  it("is green below 70", () => {
    expect(meterTone(0)).toBe("green");
    expect(meterTone(69)).toBe("green");
  });

  it("is yellow from 70 up to (not including) 90", () => {
    expect(meterTone(70)).toBe("yellow");
    expect(meterTone(89)).toBe("yellow");
  });

  it("is red at 90 and above", () => {
    expect(meterTone(90)).toBe("red");
    expect(meterTone(100)).toBe("red");
  });
});
