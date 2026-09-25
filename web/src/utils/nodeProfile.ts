// A node's profile - which inbounds and ports it serves and what it overrides
// in the fleet Core Config - as the Nodes form edits it. The pure half lives
// here so the rules the server enforces (internal/httpapi/nodeprofile.go) are
// checked next to the field instead of surfacing as a rejected save.

import { LOG_LEVELS, NODE_CORE_OVERRIDE_KEYS, NodeCoreOverrides } from "types/CoreConfig";
import { Node } from "types/Node";
import { PortInputError, formatInboundPorts, parseInboundPortInput } from "./inboundPorts";

export type OverridesInputError =
  | { kind: "invalidJson"; message: string }
  | { kind: "notObject" }
  | { kind: "unknownKey"; key: string }
  | { kind: "notString"; key: string }
  | { kind: "notBoolean"; key: string }
  | { kind: "notList"; key: string }
  | { kind: "invalidLogLevel"; value: string };

export type OverridesInputResult =
  | { ok: true; overrides: NodeCoreOverrides }
  | { ok: false; error: OverridesInputError };

const isPlainObject = (v: unknown): v is Record<string, unknown> =>
  typeof v === "object" && v !== null && !Array.isArray(v);

const isListOfObjects = (v: unknown): boolean => Array.isArray(v) && v.every(isPlainObject);

/**
 * Parses the overrides textarea. Empty input is the default (no overrides),
 * not an error. Like the server, an unknown key is rejected rather than
 * ignored - a typo would otherwise leave the node on the fleet value the
 * admin believes they replaced. A key set to null counts as absent. What
 * this cannot check is whether the MERGED config is valid (that needs the
 * fleet config); the server answers that on save.
 */
export const parseCoreOverridesInput = (text: string): OverridesInputResult => {
  if (text.trim() === "") return { ok: true, overrides: {} };
  let parsed: unknown;
  try {
    parsed = JSON.parse(text);
  } catch (e) {
    return { ok: false, error: { kind: "invalidJson", message: e instanceof Error ? e.message : String(e) } };
  }
  if (!isPlainObject(parsed)) return { ok: false, error: { kind: "notObject" } };

  const known: readonly string[] = NODE_CORE_OVERRIDE_KEYS;
  const overrides: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(parsed)) {
    if (!known.includes(key)) return { ok: false, error: { kind: "unknownKey", key } };
    if (value === null || value === undefined) continue;
    switch (key) {
      case "log_level":
        if (typeof value !== "string") return { ok: false, error: { kind: "notString", key } };
        if (!(LOG_LEVELS as readonly string[]).includes(value)) {
          return { ok: false, error: { kind: "invalidLogLevel", value } };
        }
        break;
      case "sniff_enabled":
        if (typeof value !== "boolean") return { ok: false, error: { kind: "notBoolean", key } };
        break;
      default:
        if (!isListOfObjects(value)) return { ok: false, error: { kind: "notList", key } };
    }
    overrides[key] = value;
  }
  return { ok: true, overrides: overrides as NodeCoreOverrides };
};

export const isEmptyOverrides = (o: NodeCoreOverrides | null | undefined): boolean =>
  !o || Object.keys(o).length === 0;

/** The inverse of parseCoreOverridesInput, for seeding the textarea. */
export const formatCoreOverrides = (o: NodeCoreOverrides | null | undefined): string =>
  isEmptyOverrides(o) ? "" : JSON.stringify(o, null, 2);

export type NodeProfileDraft = {
  /** Ticked inbound tags; none means every inbound. */
  tags: string[];
  /** What the ports field holds; empty means every port. */
  portsText: string;
  /** What the overrides textarea holds; empty means no overrides. */
  overridesText: string;
};

export const emptyNodeProfileDraft = (): NodeProfileDraft => ({ tags: [], portsText: "", overridesText: "" });

export const draftFromNode = (node: Node): NodeProfileDraft => ({
  tags: [...(node.inbound_tags ?? [])],
  portsText: formatInboundPorts(node.listen_ports),
  overridesText: formatCoreOverrides(node.core_overrides),
});

export type NodeProfileFields = {
  inbound_tags: string[];
  listen_ports: number[];
  core_overrides: NodeCoreOverrides;
};

export type NodeProfileResult =
  | { ok: true; profile: NodeProfileFields }
  | { ok: false; ports?: PortInputError; overrides?: OverridesInputError };

/**
 * Validates a whole draft. Both fields are checked so the form can show both
 * problems at once. Every list in a successful result is explicit - [] and {}
 * mean "back to the default", which is what an update has to send to clear a
 * field the node currently has set.
 */
export const buildNodeProfile = (draft: NodeProfileDraft): NodeProfileResult => {
  const ports = parseInboundPortInput(draft.portsText);
  const overrides = parseCoreOverridesInput(draft.overridesText);
  if (ports.ok && overrides.ok) {
    return {
      ok: true,
      profile: {
        inbound_tags: [...new Set(draft.tags)],
        listen_ports: ports.ports,
        core_overrides: overrides.overrides,
      },
    };
  }
  return {
    ok: false,
    ...(ports.ok ? {} : { ports: ports.error }),
    ...(overrides.ok ? {} : { overrides: overrides.error }),
  };
};

/**
 * The profile fields a create request should carry: only the non-default
 * ones, so a plain node is created with exactly the body it always was.
 */
export const profileForCreate = (p: NodeProfileFields): Partial<NodeProfileFields> => ({
  ...(p.inbound_tags.length > 0 ? { inbound_tags: p.inbound_tags } : {}),
  ...(p.listen_ports.length > 0 ? { listen_ports: p.listen_ports } : {}),
  ...(!isEmptyOverrides(p.core_overrides) ? { core_overrides: p.core_overrides } : {}),
});

/** Adds the tag if absent, removes it if present. */
export const toggleTag = (selected: readonly string[], tag: string): string[] =>
  selected.includes(tag) ? selected.filter((t) => t !== tag) : [...selected, tag];

/** Every tag to offer: the known ones, then any the node names that no longer exist. */
export const tagChoices = (known: readonly string[], selected: readonly string[]): string[] => [
  ...known,
  ...selected.filter((tag) => !known.includes(tag)),
];

export const hasCustomProfile = (n: Pick<Node, "inbound_tags" | "listen_ports" | "core_overrides">): boolean =>
  (n.inbound_tags?.length ?? 0) > 0 || (n.listen_ports?.length ?? 0) > 0 || !isEmptyOverrides(n.core_overrides);

export type NodeProfileSummary = { tags: string[]; ports: number[]; overrideKeys: string[] };

export const summarizeNodeProfile = (
  n: Pick<Node, "inbound_tags" | "listen_ports" | "core_overrides">
): NodeProfileSummary => ({
  tags: n.inbound_tags ?? [],
  ports: n.listen_ports ?? [],
  overrideKeys: Object.keys(n.core_overrides ?? {}),
});

export type ProfileField = "inbound_tags" | "listen_ports" | "core_overrides";

/**
 * Which profile field a server error is about. The panel starts each of its
 * validation messages with the field name ("listen_ports: invalid port 0",
 * "core_overrides: routing rule targets unknown outbound: x"), which is what
 * lets the form show it under that field instead of only at the bottom.
 */
export const profileFieldOfServerError = (message: string): ProfileField | null => {
  const fields: ProfileField[] = ["inbound_tags", "listen_ports", "core_overrides"];
  return fields.find((f) => message.startsWith(f)) ?? null;
};
