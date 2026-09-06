import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Card, CardSubtitle, CardTitle } from "../Card";

describe("Card", () => {
  it("renders on the surface token with the control-room radius", () => {
    render(<Card data-testid="card">content</Card>);
    const el = screen.getByTestId("card");
    expect(el).toHaveClass("bg-rapido-surface");
    expect(el).toHaveClass("rounded-xl2");
  });

  it("merges a caller className without dropping the base styling", () => {
    render(
      <Card data-testid="card" className="border-emerald-500/30">
        content
      </Card>
    );
    const el = screen.getByTestId("card");
    expect(el).toHaveClass("bg-rapido-surface");
    expect(el).toHaveClass("border-emerald-500/30");
  });
});

describe("CardTitle / CardSubtitle", () => {
  it("render their text on the text and muted tokens", () => {
    render(
      <>
        <CardTitle>Fleet status</CardTitle>
        <CardSubtitle>Live per-node health</CardSubtitle>
      </>
    );
    expect(screen.getByText("Fleet status")).toHaveClass("text-rapido-text");
    expect(screen.getByText("Live per-node health")).toHaveClass("text-rapido-muted");
  });
});
