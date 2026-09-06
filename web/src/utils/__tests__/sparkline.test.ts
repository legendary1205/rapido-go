import { describe, expect, it } from "vitest";
import { buildSparklinePath } from "../sparkline";

describe("buildSparklinePath", () => {
  it("returns null for an empty series", () => {
    expect(buildSparklinePath([])).toBeNull();
  });

  it("returns null for a single point - there is no line to draw", () => {
    expect(buildSparklinePath([5])).toBeNull();
  });

  it("starts with an absolute moveto and continues with linetos", () => {
    const d = buildSparklinePath([1, 2, 3]);
    expect(d).not.toBeNull();
    expect(d!.startsWith("M")).toBe(true);
    expect(d!.split(" ").filter((seg) => seg.startsWith("L"))).toHaveLength(2);
  });

  it("does not divide by zero for an all-zero series", () => {
    expect(buildSparklinePath([0, 0, 0])).toBe("M0.00,30.00 L50.00,30.00 L100.00,30.00");
  });

  it("normalizes against the series' own max, not a fixed scale", () => {
    // max=10: first point (0) sits at the baseline y=30, the last (10) rises
    // the full 28 units to y=2.
    expect(buildSparklinePath([0, 10])).toBe("M0.00,30.00 L100.00,2.00");
  });

  it("spaces points evenly across the 0-100 x range", () => {
    const d = buildSparklinePath([1, 1, 1, 1, 1]);
    const xs = d!.split(" ").map((seg) => Number(seg.slice(1).split(",")[0]));
    expect(xs).toEqual([0, 25, 50, 75, 100]);
  });
});
