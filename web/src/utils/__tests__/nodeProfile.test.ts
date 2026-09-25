import { describe, expect, it } from "vitest";
import {
  buildNodeProfile,
  draftFromNode,
  emptyNodeProfileDraft,
  formatCoreOverrides,
  hasCustomProfile,
  parseCoreOverridesInput,
  profileFieldOfServerError,
  profileForCreate,
  summarizeNodeProfile,
  tagChoices,
  toggleTag,
} from "../nodeProfile";
import { Node } from "types/Node";

const node = (extra: Partial<Node> = {}): Node => ({
  id: 1,
  name: "n1",
  address: "10.0.0.1",
  port: 62050,
  api_port: 62051,
  status: "connected",
  usage_coefficient: 1,
  ...extra,
});

describe("parseCoreOverridesInput", () => {
  it("treats empty and whitespace-only input as no overrides, not an error", () => {
    expect(parseCoreOverridesInput("")).toEqual({ ok: true, overrides: {} });
    expect(parseCoreOverridesInput("  \n ")).toEqual({ ok: true, overrides: {} });
    expect(parseCoreOverridesInput("{}")).toEqual({ ok: true, overrides: {} });
  });

  it("accepts every supported key with the right shape", () => {
    const text = JSON.stringify({
      log_level: "debug",
      sniff_enabled: false,
      dns_servers: [{ tag: "d", type: "udp", address: "8.8.8.8" }],
      outbounds: [{ tag: "x", type: "direct" }],
      routing_rules_first: [{ inbound: ["a"], inbound_port: [1], outbound_tag: "x" }],
    });
    const result = parseCoreOverridesInput(text);
    expect(result.ok).toBe(true);
    expect(result.ok && Object.keys(result.overrides)).toEqual([
      "log_level",
      "sniff_enabled",
      "dns_servers",
      "outbounds",
      "routing_rules_first",
    ]);
  });

  it("keeps an explicit empty dns list, which means no DNS servers on this node", () => {
    expect(parseCoreOverridesInput('{"dns_servers": []}')).toEqual({ ok: true, overrides: { dns_servers: [] } });
  });

  it("drops keys that are null", () => {
    expect(parseCoreOverridesInput('{"log_level": null, "outbounds": null}')).toEqual({ ok: true, overrides: {} });
  });

  it("reports invalid JSON with the parser's own message", () => {
    const result = parseCoreOverridesInput('{"log_level": ');
    expect(result.ok).toBe(false);
    expect(!result.ok && result.error.kind).toBe("invalidJson");
    expect(!result.ok && result.error.kind === "invalidJson" && result.error.message).toBeTruthy();
  });

  it("rejects anything that is not a JSON object", () => {
    for (const text of ["[]", '"x"', "5", "true", "null"]) {
      expect(parseCoreOverridesInput(text), text).toEqual({ ok: false, error: { kind: "notObject" } });
    }
  });

  it("rejects an unknown key and names it", () => {
    expect(parseCoreOverridesInput('{"outbounds_typo": []}')).toEqual({
      ok: false,
      error: { kind: "unknownKey", key: "outbounds_typo" },
    });
    // The fleet-only keys are not overridable per node.
    expect(parseCoreOverridesInput('{"routing_rules": []}')).toEqual({
      ok: false,
      error: { kind: "unknownKey", key: "routing_rules" },
    });
  });

  it("rejects values of the wrong type for a known key", () => {
    expect(parseCoreOverridesInput('{"log_level": 5}')).toEqual({ ok: false, error: { kind: "notString", key: "log_level" } });
    expect(parseCoreOverridesInput('{"sniff_enabled": "yes"}')).toEqual({
      ok: false,
      error: { kind: "notBoolean", key: "sniff_enabled" },
    });
    expect(parseCoreOverridesInput('{"outbounds": {"tag": "x"}}')).toEqual({
      ok: false,
      error: { kind: "notList", key: "outbounds" },
    });
    expect(parseCoreOverridesInput('{"routing_rules_first": ["nope"]}')).toEqual({
      ok: false,
      error: { kind: "notList", key: "routing_rules_first" },
    });
  });

  it("rejects a log level the server would", () => {
    expect(parseCoreOverridesInput('{"log_level": "loud"}')).toEqual({
      ok: false,
      error: { kind: "invalidLogLevel", value: "loud" },
    });
    expect(parseCoreOverridesInput('{"log_level": "trace"}').ok).toBe(true);
  });
});

describe("formatCoreOverrides", () => {
  it("is empty for nothing and pretty JSON otherwise, round-tripping through the parser", () => {
    expect(formatCoreOverrides(undefined)).toBe("");
    expect(formatCoreOverrides(null)).toBe("");
    expect(formatCoreOverrides({})).toBe("");
    const overrides = { log_level: "debug" as const, sniff_enabled: false };
    const text = formatCoreOverrides(overrides);
    expect(text).toContain("\n");
    expect(parseCoreOverridesInput(text)).toEqual({ ok: true, overrides });
  });
});

describe("buildNodeProfile", () => {
  it("turns an untouched draft into the explicit defaults an update needs to send", () => {
    expect(buildNodeProfile(emptyNodeProfileDraft())).toEqual({
      ok: true,
      profile: { inbound_tags: [], listen_ports: [], core_overrides: {} },
    });
  });

  it("carries tags, ports and overrides through", () => {
    expect(
      buildNodeProfile({ tags: ["b", "a", "b"], portsText: "20002, 20000", overridesText: '{"log_level":"info"}' })
    ).toEqual({
      ok: true,
      profile: { inbound_tags: ["b", "a"], listen_ports: [20002, 20000], core_overrides: { log_level: "info" } },
    });
  });

  it("reports both problems at once", () => {
    expect(buildNodeProfile({ tags: [], portsText: "0 443 443", overridesText: "[" })).toMatchObject({
      ok: false,
      ports: { kind: "outOfRange", value: "0" },
      overrides: { kind: "invalidJson" },
    });
    const onlyPorts = buildNodeProfile({ tags: [], portsText: "abc", overridesText: "" });
    expect(onlyPorts).toEqual({ ok: false, ports: { kind: "notInteger", value: "abc" } });
  });

  it("rejects duplicate ports like the server does", () => {
    expect(buildNodeProfile({ tags: [], portsText: "443 443", overridesText: "" })).toEqual({
      ok: false,
      ports: { kind: "duplicate", value: "443" },
    });
  });

  it("reads Persian digits in the ports field", () => {
    expect(buildNodeProfile({ tags: [], portsText: "۲۰۰۰۰،۲۰۰۰۱", overridesText: "" })).toMatchObject({
      ok: true,
      profile: { listen_ports: [20000, 20001] },
    });
  });
});

describe("profileForCreate", () => {
  it("leaves out every default so a plain node is created with a plain body", () => {
    expect(profileForCreate({ inbound_tags: [], listen_ports: [], core_overrides: {} })).toEqual({});
  });

  it("sends only the fields that are set", () => {
    expect(profileForCreate({ inbound_tags: ["a"], listen_ports: [], core_overrides: {} })).toEqual({ inbound_tags: ["a"] });
    expect(profileForCreate({ inbound_tags: [], listen_ports: [443], core_overrides: { sniff_enabled: false } })).toEqual({
      listen_ports: [443],
      core_overrides: { sniff_enabled: false },
    });
  });
});

describe("draftFromNode / hasCustomProfile / summarizeNodeProfile", () => {
  it("a node without profile keys is the default", () => {
    const n = node();
    expect(hasCustomProfile(n)).toBe(false);
    expect(draftFromNode(n)).toEqual(emptyNodeProfileDraft());
    expect(summarizeNodeProfile(n)).toEqual({ tags: [], ports: [], overrideKeys: [] });
  });

  it("any one of the three makes it custom", () => {
    expect(hasCustomProfile(node({ inbound_tags: ["a"] }))).toBe(true);
    expect(hasCustomProfile(node({ listen_ports: [443] }))).toBe(true);
    expect(hasCustomProfile(node({ core_overrides: { log_level: "debug" } }))).toBe(true);
    expect(hasCustomProfile(node({ inbound_tags: [], listen_ports: [], core_overrides: {} }))).toBe(false);
  });

  it("seeds the form from the node and survives a round trip", () => {
    const n = node({
      inbound_tags: ["a", "b"],
      listen_ports: [20000, 20001],
      core_overrides: { log_level: "debug", sniff_enabled: false },
    });
    const draft = draftFromNode(n);
    expect(draft.tags).toEqual(["a", "b"]);
    expect(draft.portsText).toBe("20000, 20001");
    expect(buildNodeProfile(draft)).toEqual({
      ok: true,
      profile: { inbound_tags: ["a", "b"], listen_ports: [20000, 20001], core_overrides: n.core_overrides },
    });
    expect(summarizeNodeProfile(n)).toEqual({ tags: ["a", "b"], ports: [20000, 20001], overrideKeys: ["log_level", "sniff_enabled"] });
  });
});

describe("tag helpers", () => {
  it("toggleTag adds and removes without touching the input", () => {
    const selected = ["a"];
    expect(toggleTag(selected, "b")).toEqual(["a", "b"]);
    expect(toggleTag(selected, "a")).toEqual([]);
    expect(selected).toEqual(["a"]);
  });

  it("tagChoices keeps a tag the node names that no longer exists, so it can be unticked", () => {
    expect(tagChoices(["a", "b"], ["b", "gone"])).toEqual(["a", "b", "gone"]);
    expect(tagChoices([], [])).toEqual([]);
  });
});

describe("profileFieldOfServerError", () => {
  it("finds the field from the message prefix the panel puts on it", () => {
    expect(profileFieldOfServerError("listen_ports: invalid port 0 (must be 1-65535)")).toBe("listen_ports");
    expect(profileFieldOfServerError("inbound_tags: unknown inbound ghost")).toBe("inbound_tags");
    expect(profileFieldOfServerError("core_overrides: routing rule targets unknown outbound: x")).toBe("core_overrides");
    expect(profileFieldOfServerError("core_overrides must be a JSON object")).toBe("core_overrides");
    expect(profileFieldOfServerError("listen_ports must be an array of integers")).toBe("listen_ports");
  });

  it("finds the capacity field, which sits in the same Advanced section", () => {
    expect(profileFieldOfServerError("capacity: must be between 1 and 10000000")).toBe("capacity");
    expect(profileFieldOfServerError("capacity must be a whole number")).toBe("capacity");
  });

  it("returns null for everything else", () => {
    expect(profileFieldOfServerError("A node with this name already exists")).toBeNull();
    expect(profileFieldOfServerError("")).toBeNull();
  });
});
