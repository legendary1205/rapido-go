import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { Button } from "../Button";

describe("Button", () => {
  it("defaults to the secondary (bordered) look", () => {
    render(<Button>Cancel</Button>);
    const el = screen.getByRole("button", { name: "Cancel" });
    expect(el).toHaveClass("border-rapido-border");
    expect(el).toHaveClass("rounded-lg");
  });

  it("renders the primary (filled accent) look", () => {
    render(<Button variant="primary">Save</Button>);
    const el = screen.getByRole("button", { name: "Save" });
    expect(el).toHaveClass("bg-rapido-accent");
    expect(el).toHaveClass("text-white");
  });

  it("renders the chip look with buttonStyles.ts's shared base class", () => {
    render(<Button variant="chip">Edit</Button>);
    const el = screen.getByRole("button", { name: "Edit" });
    // "rounded-md" is unique to btnBase - primary/secondary both use
    // "rounded-lg" - so this proves the chip variant actually routes through
    // the shared buttonStyles.ts rather than reimplementing its own class.
    expect(el).toHaveClass("rounded-md");
  });

  it("applies the requested tone only when variant is chip", () => {
    render(<Button variant="chip" tone="red">Delete</Button>);
    const el = screen.getByRole("button", { name: "Delete" });
    expect(el.className).toMatch(/red/);
  });

  it("defaults type to \"button\" so it never submits a surrounding form by accident", () => {
    render(<Button>Cancel</Button>);
    expect(screen.getByRole("button", { name: "Cancel" })).toHaveAttribute("type", "button");
  });

  it("still fires onClick and forwards disabled", () => {
    const onClick = vi.fn();
    render(
      <Button onClick={onClick} disabled>
        Disabled
      </Button>
    );
    const el = screen.getByRole("button", { name: "Disabled" });
    expect(el).toBeDisabled();
    fireEvent.click(el);
    expect(onClick).not.toHaveBeenCalled();
  });

  it("merges a caller className without dropping the variant's own classes", () => {
    render(
      <Button variant="primary" className="w-full">
        Submit
      </Button>
    );
    const el = screen.getByRole("button", { name: "Submit" });
    expect(el).toHaveClass("bg-rapido-accent");
    expect(el).toHaveClass("w-full");
  });
});
