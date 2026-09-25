import { describe, expect, it } from "vitest";
import { buildNodeInstallCommand } from "../nodeInstall";

describe("buildNodeInstallCommand", () => {
  it("is exactly the documented one-liner, built from the panel's origin and the setup blob", () => {
    expect(buildNodeInstallCommand("https://panel.example.com", "QUJDRA==")).toBe(
      "curl -fsSL https://panel.example.com/install/node.sh | sudo bash -s -- 'QUJDRA=='"
    );
  });

  it("keeps a port in the origin and never doubles a trailing slash", () => {
    expect(buildNodeInstallCommand("http://203.0.113.9:8000", "x")).toBe(
      "curl -fsSL http://203.0.113.9:8000/install/node.sh | sudo bash -s -- 'x'"
    );
    expect(buildNodeInstallCommand("https://panel.example.com/", "x")).toBe(
      "curl -fsSL https://panel.example.com/install/node.sh | sudo bash -s -- 'x'"
    );
  });

  it("escapes a single quote so the blob can never end the quoting early", () => {
    expect(buildNodeInstallCommand("https://p.test", "a'b")).toBe(
      "curl -fsSL https://p.test/install/node.sh | sudo bash -s -- 'a'\\''b'"
    );
  });
});
