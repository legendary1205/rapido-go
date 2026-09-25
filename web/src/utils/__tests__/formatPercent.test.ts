import { describe, expect, it } from "vitest";
import { formatPercent } from "utils/formatPercent";

describe("formatPercent", () => {
  it("rounds to whole numbers from 10% up and keeps one decimal below", () => {
    expect(formatPercent(9000, 9400)).toBe("96%");
    expect(formatPercent(1, 2)).toBe("50%");
    expect(formatPercent(200, 9400)).toBe("2.1%");
    expect(formatPercent(80, 9400)).toBe("0.9%");
  });

  it("does not round a tiny slice down to zero, and prints a real zero as 0%", () => {
    expect(formatPercent(1, 10000)).toBe("<0.1%");
    expect(formatPercent(0, 9400)).toBe("0%");
    expect(formatPercent(5, 0)).toBe("0%");
  });
});
