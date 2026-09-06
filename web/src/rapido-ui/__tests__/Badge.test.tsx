import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Badge, PulseDot } from "../Badge";

describe("Badge", () => {
  it("renders its children", () => {
    render(<Badge tone="green">Online</Badge>);
    expect(screen.getByText("Online")).toBeInTheDocument();
  });

  it("carries the brand tone class for the renamed accent tone", () => {
    render(<Badge tone="brand">Sudo</Badge>);
    expect(screen.getByText("Sudo")).toHaveClass("text-rapido-accent");
  });

  it("falls back to gray when no tone is given", () => {
    render(<Badge>Unknown</Badge>);
    expect(screen.getByText("Unknown")).toHaveClass("text-rapido-muted");
  });
});

describe("PulseDot", () => {
  it("renders only the solid core when not live", () => {
    const { container } = render(<PulseDot tone="red" live={false} />);
    // One span for the wrapper, one for the solid dot - no ring span.
    expect(container.querySelectorAll("span")).toHaveLength(2);
    expect(container.querySelector(".animate-ping")).toBeNull();
  });

  it("adds the expanding ring while live", () => {
    const { container } = render(<PulseDot tone="green" live />);
    expect(container.querySelector(".animate-ping")).not.toBeNull();
  });
});
