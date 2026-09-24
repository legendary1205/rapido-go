import { FC, useState } from "react";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import i18n from "locales/i18n";
import { beforeAll, describe, expect, it } from "vitest";
import { XrayConfigStructure } from "../XrayConfigStructure";

beforeAll(async () => {
  await i18n.changeLanguage("en");
});

const Harness: FC<{ initial: object; inboundPorts?: Record<string, number[]> }> = ({ initial, inboundPorts }) => {
  const [text, setText] = useState(JSON.stringify(initial, null, 2));
  const [drafts, setDrafts] = useState<Record<number, string>>({});
  return (
    <>
      <XrayConfigStructure
        text={text}
        onEdit={setText}
        inboundPorts={inboundPorts}
        portDrafts={drafts}
        onPortDraft={(i, draft) =>
          setDrafts((prev) => {
            const { [i]: _dropped, ...rest } = prev;
            return draft === null ? rest : { ...rest, [i]: draft };
          })
        }
      />
      <pre data-testid="json">{text}</pre>
      <span data-testid="drafts">{JSON.stringify(drafts)}</span>
    </>
  );
};

// Interpolated values are wrapped in bidi isolates (see rapido-ui/bidi.ts).
const plain = (el: HTMLElement) => (el.textContent ?? "").replace(/[⁦⁩]/g, "");
const json = () => JSON.parse(screen.getByTestId("json").textContent ?? "");
const open = async () => userEvent.click(screen.getByRole("button", { name: /Outbounds and routing rules/ }));

const baseDoc = {
  outbounds: [
    { tag: "wg-de", type: "direct" },
    { tag: "grp", type: "selector", outbounds: ["wg-de"] },
  ],
  routing_rules: [
    { domain_suffix: [".ir"], outbound_tag: "direct" },
    { inbound: ["main"], inbound_port: [20000], outbound_tag: "wg-de" },
  ],
  inbounds: [
    { tag: "main", protocol: "vless" },
    { tag: "alt", protocol: "vless" },
  ],
};

describe("XrayConfigStructure", () => {
  it("is collapsed until opened, with the outbound and rule counts in its header", async () => {
    render(<Harness initial={baseDoc} />);
    expect(screen.getByText("Outbounds: 2 · Rules: 2")).toBeInTheDocument();
    expect(screen.queryByText("Routing rules")).not.toBeInTheDocument();
    await open();
    expect(screen.getByText("Routing rules")).toBeInTheDocument();
  });

  it("tells the admin to fix the JSON instead of crashing when it does not parse", async () => {
    render(<XrayConfigStructure text="{ nope" onEdit={() => {}} inboundPorts={undefined} portDrafts={{}} onPortDraft={() => {}} />);
    await open();
    expect(screen.getByText(/syntax error/)).toBeInTheDocument();
  });

  describe("inbound ports", () => {
    it("shows every listen port of an inbound, truncated with a count when there are many", async () => {
      const many = Array.from({ length: 15 }, (_, i) => 20000 + i);
      render(<Harness initial={baseDoc} inboundPorts={{ main: many, alt: [443, 8443] }} />);
      await open();
      expect(screen.getByText("20000, 20001, 20002, 20003, 20004, …")).toBeInTheDocument();
      expect(screen.getByText("(15 ports)")).toBeInTheDocument();
      expect(screen.getByText("443, 8443")).toBeInTheDocument();
    });
  });

  describe("routing rule ports", () => {
    it("summarises the rule's inbound and ports read-only", async () => {
      render(<Harness initial={baseDoc} />);
      await open();
      const rule2 = screen.getByText("Rule 2").parentElement as HTMLElement;
      expect(within(rule2).getByText("main")).toBeInTheDocument();
      expect(within(rule2).getByText("20000")).toBeInTheDocument();
    });

    it("offers the ports field only while at least one inbound is selected", async () => {
      render(<Harness initial={baseDoc} />);
      await open();
      const editButtons = screen.getAllByRole("button", { name: "Edit" });
      // outbound wg-de, rule 1, rule 2 (the selector outbound has nothing to edit)
      expect(editButtons).toHaveLength(3);
      await userEvent.click(editButtons[1]);
      expect(screen.queryByLabelText(/Local listen ports/)).not.toBeInTheDocument();

      await userEvent.click(screen.getByRole("checkbox", { name: "main" }));
      expect(json().routing_rules[0].inbound).toEqual(["main"]);
      expect(screen.getByLabelText(/Local listen ports/)).toBeInTheDocument();
    });

    it("writes typed ports into the JSON, and omits the key again when the field is emptied", async () => {
      render(<Harness initial={baseDoc} />);
      await open();
      await userEvent.click(screen.getAllByRole("button", { name: "Edit" })[2]);
      const field = screen.getByLabelText(/Local listen ports/);
      expect(field).toHaveValue("20000");

      await userEvent.clear(field);
      await userEvent.type(field, "20001, 20002 20003");
      expect(json().routing_rules[1].inbound_port).toEqual([20001, 20002, 20003]);

      await userEvent.clear(field);
      expect("inbound_port" in json().routing_rules[1]).toBe(false);
    });

    it("shows an inline error for a bad port and leaves the JSON untouched", async () => {
      render(<Harness initial={baseDoc} />);
      await open();
      await userEvent.click(screen.getAllByRole("button", { name: "Edit" })[2]);
      const field = screen.getByLabelText(/Local listen ports/);
      await userEvent.type(field, "1"); // 20000 -> 200001
      expect(plain(screen.getByRole("alert"))).toBe("200001 is not a valid port (use 1-65535).");
      expect(field).toHaveAttribute("aria-invalid", "true");
      expect(json().routing_rules[1].inbound_port).toEqual([20000]);
      // The bad draft is reported upward so the parent can hold Apply back.
      expect(JSON.parse(screen.getByTestId("drafts").textContent ?? "")).toEqual({ 1: "200001" });
    });

    it("rejects duplicates inline", async () => {
      render(<Harness initial={baseDoc} />);
      await open();
      await userEvent.click(screen.getAllByRole("button", { name: "Edit" })[2]);
      const field = screen.getByLabelText(/Local listen ports/);
      await userEvent.clear(field);
      await userEvent.type(field, "20001, 20001");
      expect(plain(screen.getByRole("alert"))).toBe("20001 is listed more than once.");
    });

    it("drops inbound_port together with the last inbound", async () => {
      render(<Harness initial={baseDoc} />);
      await open();
      await userEvent.click(screen.getAllByRole("button", { name: "Edit" })[2]);
      await userEvent.click(screen.getByRole("checkbox", { name: "main" }));
      const rule = json().routing_rules[1];
      expect("inbound" in rule).toBe(false);
      expect("inbound_port" in rule).toBe(false);
      expect(screen.queryByLabelText(/Local listen ports/)).not.toBeInTheDocument();
    });
  });

  describe("direct fallback", () => {
    const editWg = async () => {
      render(<Harness initial={baseDoc} />);
      await open();
      await userEvent.click(screen.getAllByRole("button", { name: "Edit" })[0]);
    };

    it("is not offered for an outbound without a bind interface", async () => {
      await editWg();
      expect(screen.queryByRole("checkbox", { name: /Fall back to a direct connection/ })).not.toBeInTheDocument();
      expect(screen.getByText(/Set a bind interface to allow a direct fallback/)).toBeInTheDocument();
    });

    it("appears once an interface is set, and ticking it writes direct_fallback: true", async () => {
      await editWg();
      await userEvent.type(screen.getByLabelText("Bind interface"), "wg0");
      expect(json().outbounds[0].bind_interface).toBe("wg0");
      const box = screen.getByRole("checkbox", { name: "Fall back to a direct connection when this interface is down" });
      await userEvent.click(box);
      expect(json().outbounds[0].direct_fallback).toBe(true);
      expect(screen.getAllByText("Direct fallback").length).toBeGreaterThan(0);
    });

    it("clears the flag when the interface is cleared, and never writes false", async () => {
      await editWg();
      await userEvent.type(screen.getByLabelText("Bind interface"), "wg0");
      await userEvent.click(screen.getByRole("checkbox", { name: /Fall back to a direct connection/ }));
      await userEvent.click(screen.getByRole("checkbox", { name: /Fall back to a direct connection/ }));
      expect("direct_fallback" in json().outbounds[0]).toBe(false);
      await userEvent.click(screen.getByRole("checkbox", { name: /Fall back to a direct connection/ }));
      expect(json().outbounds[0].direct_fallback).toBe(true);

      await userEvent.clear(screen.getByLabelText("Bind interface"));
      expect("bind_interface" in json().outbounds[0]).toBe(false);
      expect("direct_fallback" in json().outbounds[0]).toBe(false);
      expect(screen.queryByRole("checkbox", { name: /Fall back to a direct connection/ })).not.toBeInTheDocument();
    });

    it("has no editor at all for selector, urltest and block outbounds", async () => {
      render(
        <Harness
          initial={{
            outbounds: [
              { tag: "g", type: "selector", bind_interface: "wg0" },
              { tag: "u", type: "urltest" },
              { tag: "b", type: "block" },
            ],
          }}
        />
      );
      await open();
      expect(screen.queryByRole("button", { name: "Edit" })).not.toBeInTheDocument();
    });
  });
});
