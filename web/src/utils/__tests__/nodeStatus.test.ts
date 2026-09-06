import { describe, expect, it } from "vitest";
import { toneForNodeStatus } from "../nodeStatus";

describe("toneForNodeStatus", () => {
  it("maps connected to green", () => {
    expect(toneForNodeStatus("connected")).toBe("green");
  });

  it("maps connecting to yellow", () => {
    expect(toneForNodeStatus("connecting")).toBe("yellow");
  });

  it("maps error to red", () => {
    expect(toneForNodeStatus("error")).toBe("red");
  });

  it("maps disabled to gray", () => {
    expect(toneForNodeStatus("disabled")).toBe("gray");
  });

  it("falls back to gray for an unrecognized status", () => {
    expect(toneForNodeStatus("something-new")).toBe("gray");
  });
});
