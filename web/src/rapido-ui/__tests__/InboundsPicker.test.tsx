import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import i18n from "locales/i18n";
import { beforeAll, describe, expect, it, vi } from "vitest";
import { InboundsPicker } from "../InboundsPicker";

// Deterministic regardless of jsdom's navigator.language / detector caches -
// the assertions below check specific English copy.
beforeAll(async () => {
  await i18n.changeLanguage("en");
});

describe("InboundsPicker", () => {
  it("shows the no-protocols message when nothing is available", () => {
    render(
      <InboundsPicker
        protocols={[]}
        inboundsByProtocol={{}}
        selectedProtocols={new Set()}
        onToggleProtocol={() => {}}
        selectedInbounds={{}}
        onToggleTag={() => {}}
      />
    );
    expect(
      screen.getByText("No protocols are enabled on this server.")
    ).toBeInTheDocument();
  });

  it("renders one checkbox per protocol, reflecting the selected set", () => {
    render(
      <InboundsPicker
        protocols={["vless", "vmess"]}
        inboundsByProtocol={{ vless: ["tag-a"], vmess: ["tag-b"] }}
        selectedProtocols={new Set(["vless"])}
        onToggleProtocol={() => {}}
        selectedInbounds={{}}
        onToggleTag={() => {}}
      />
    );
    expect(screen.getByRole("checkbox", { name: /vless/ })).toBeChecked();
    expect(screen.getByRole("checkbox", { name: /vmess/ })).not.toBeChecked();
  });

  it("flags a protocol as unavailable on this server via isProtocolUnavailable", () => {
    render(
      <InboundsPicker
        protocols={["vless"]}
        isProtocolUnavailable={() => true}
        inboundsByProtocol={{}}
        selectedProtocols={new Set(["vless"])}
        onToggleProtocol={() => {}}
        selectedInbounds={{}}
        onToggleTag={() => {}}
      />
    );
    expect(screen.getByText("(unavailable on this server)")).toBeInTheDocument();
  });

  it("calls onToggleProtocol with the clicked protocol", async () => {
    const user = userEvent.setup();
    const onToggleProtocol = vi.fn();
    render(
      <InboundsPicker
        protocols={["vless"]}
        inboundsByProtocol={{ vless: ["tag-a"] }}
        selectedProtocols={new Set()}
        onToggleProtocol={onToggleProtocol}
        selectedInbounds={{}}
        onToggleTag={() => {}}
      />
    );
    await user.click(screen.getByRole("checkbox", { name: /vless/ }));
    expect(onToggleProtocol).toHaveBeenCalledWith("vless");
  });

  it("only shows a protocol's inbound tags once that protocol is selected", () => {
    const { rerender } = render(
      <InboundsPicker
        protocols={["vless"]}
        inboundsByProtocol={{ vless: ["tag-a", "tag-b"] }}
        selectedProtocols={new Set()}
        onToggleProtocol={() => {}}
        selectedInbounds={{}}
        onToggleTag={() => {}}
      />
    );
    expect(screen.queryByText("tag-a")).not.toBeInTheDocument();

    rerender(
      <InboundsPicker
        protocols={["vless"]}
        inboundsByProtocol={{ vless: ["tag-a", "tag-b"] }}
        selectedProtocols={new Set(["vless"])}
        onToggleProtocol={() => {}}
        selectedInbounds={{}}
        onToggleTag={() => {}}
      />
    );
    expect(screen.getByText("tag-a")).toBeInTheDocument();
    expect(screen.getByText("tag-b")).toBeInTheDocument();
  });

  it("calls onToggleTag with (protocol, tag) when a tag checkbox is clicked", async () => {
    const user = userEvent.setup();
    const onToggleTag = vi.fn();
    render(
      <InboundsPicker
        protocols={["vless"]}
        inboundsByProtocol={{ vless: ["tag-a"] }}
        selectedProtocols={new Set(["vless"])}
        onToggleProtocol={() => {}}
        selectedInbounds={{ vless: new Set() }}
        onToggleTag={onToggleTag}
      />
    );
    await user.click(screen.getByRole("checkbox", { name: "tag-a" }));
    expect(onToggleTag).toHaveBeenCalledWith("vless", "tag-a");
  });

  it("renders no tag section for a selected protocol with zero known tags", () => {
    render(
      <InboundsPicker
        protocols={["vless"]}
        inboundsByProtocol={{ vless: [] }}
        selectedProtocols={new Set(["vless"])}
        onToggleProtocol={() => {}}
        selectedInbounds={{}}
        onToggleTag={() => {}}
      />
    );
    // Only the protocol checkbox itself - no "vless inbounds" tag section.
    expect(screen.getAllByRole("checkbox")).toHaveLength(1);
  });
});
