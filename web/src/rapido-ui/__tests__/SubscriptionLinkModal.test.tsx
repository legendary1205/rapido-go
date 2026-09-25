import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import i18n from "locales/i18n";
import { afterEach, beforeAll, describe, expect, it, vi } from "vitest";
import { User } from "types/User";

// The real QR code is an SVG of modules; a stand-in that exposes its value is
// what lets a test see which address the code currently encodes.
vi.mock("qrcode.react", () => ({
  QRCodeSVG: ({ value }: { value: string }) => <div data-testid="qr" data-value={value} />,
}));

import { UserActionModals } from "../UserActionModals";
import { useUsersUiStore } from "../usersUiStore";

beforeAll(async () => {
  await i18n.changeLanguage("en");
});

afterEach(() => {
  act(() => useUsersUiStore.getState().setSubscriptionLinkUser(null));
});

const userWith = (subscription_url: string, subscription_urls?: string[]) =>
  ({ username: "u1", subscription_url, subscription_urls } as unknown as User);

const open = (user: User) => {
  act(() => useUsersUiStore.getState().setSubscriptionLinkUser(user));
  render(<UserActionModals />);
};

const NEW = "https://sub.new.example/sub/TOKEN";
const OLD = "https://sub.old.example/sub/TOKEN";

describe("subscription link modal", () => {
  it("lists both addresses with the new one first, and the QR starts on it", () => {
    open(userWith(OLD, [NEW, OLD]));

    const inputs = screen.getAllByRole("textbox") as HTMLInputElement[];
    expect(inputs.map((i) => i.value)).toEqual([NEW, OLD]);
    expect(screen.getByText("sub.new.example")).toBeInTheDocument();
    expect(screen.getByText("sub.old.example")).toBeInTheDocument();
    expect(screen.getAllByRole("button", { name: /copy/i })).toHaveLength(2);
    expect(screen.getByTestId("qr")).toHaveAttribute("data-value", NEW);
  });

  it("moves the QR to whichever address is clicked", async () => {
    open(userWith(OLD, [NEW, OLD]));
    const [, oldInput] = screen.getAllByRole("textbox");

    await userEvent.click(oldInput);

    expect(screen.getByTestId("qr")).toHaveAttribute("data-value", OLD);
  });

  it("shows a single address exactly as before when the backend sends no list", () => {
    open(userWith(OLD));

    const inputs = screen.getAllByRole("textbox") as HTMLInputElement[];
    expect(inputs.map((i) => i.value)).toEqual([OLD]);
    expect(screen.queryByText("sub.old.example")).not.toBeInTheDocument();
    expect(screen.getByTestId("qr")).toHaveAttribute("data-value", OLD);
  });
});
