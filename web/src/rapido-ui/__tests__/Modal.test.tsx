import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { Modal } from "../Modal";

describe("Modal", () => {
  it("renders a title via CardTitle and its children", () => {
    render(
      <Modal onClose={() => {}} title="Delete user">
        <p>Are you sure?</p>
      </Modal>
    );
    expect(screen.getByText("Delete user")).toBeInTheDocument();
    expect(screen.getByText("Are you sure?")).toBeInTheDocument();
  });

  it("renders no title element at all when title is omitted, for a fully custom header", () => {
    const { container } = render(
      <Modal onClose={() => {}}>
        <div data-testid="custom-header">Custom</div>
      </Modal>
    );
    expect(screen.getByTestId("custom-header")).toBeInTheDocument();
    // CardTitle always renders a div with this exact class combo - absent
    // here since no `title` prop was given.
    expect(container.querySelector(".text-base.font-semibold")).toBeNull();
  });

  it("closes when the overlay itself is clicked (outside the card)", () => {
    const onClose = vi.fn();
    render(
      <Modal onClose={onClose} title="t">
        <p>content</p>
      </Modal>
    );
    // The overlay is content's great-grandparent: content -> Card -> overlay.
    const overlay = screen.getByText("content").closest("div.fixed") as HTMLElement;
    fireEvent.mouseDown(overlay);
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it("does not close when a click starts inside the card", () => {
    const onClose = vi.fn();
    render(
      <Modal onClose={onClose} title="t">
        <p>content</p>
      </Modal>
    );
    fireEvent.mouseDown(screen.getByText("content"));
    expect(onClose).not.toHaveBeenCalled();
  });

  it("applies a caller className to size the card", () => {
    render(
      <Modal onClose={() => {}} className="max-w-2xl">
        <p>content</p>
      </Modal>
    );
    const card = screen.getByText("content").closest(".max-h-\\[90vh\\]");
    expect(card).toHaveClass("max-w-2xl");
  });
});
