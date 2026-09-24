import { Outbound, RoutingRule } from "types/CoreConfig";
import { validateInboundPortField } from "utils/inboundPorts";

// The Xray Config page edits the whole document as JSON, so what follows is
// pure edit/validate helpers over plain objects: the guided view rewrites the
// JSON through them, and Apply is gated on validateCoreConfigDoc so a payload
// the server would reject never leaves the browser.

const GROUP_OR_BLOCK_TYPES = new Set(["selector", "urltest", "block"]);

/** A "leaf" outbound actually dials somewhere; selector/urltest pick among
 * other outbounds and block dials nothing. */
export const isLeafOutboundType = (type: unknown): boolean =>
  typeof type === "string" && type !== "" && !GROUP_OR_BLOCK_TYPES.has(type);

const hasBindInterface = (value: unknown): value is string =>
  typeof value === "string" && value.trim() !== "";

/** direct_fallback only makes sense for a leaf outbound pinned to an interface
 * (there has to be an interface that can go down). */
export const outboundSupportsDirectFallback = (o: { type?: unknown; bind_interface?: unknown }): boolean =>
  isLeafOutboundType(o.type) && hasBindInterface(o.bind_interface);

const omit = <T extends object>(obj: T, key: keyof T): T => {
  const copy = { ...obj };
  delete copy[key];
  return copy;
};

/** Clearing the interface also clears direct_fallback: the flag is meaningless
 * (and rejected by the server) without one. */
export const setOutboundBindInterface = (o: Outbound, value: string): Outbound => {
  const trimmed = value.trim();
  if (trimmed === "") return omit(omit(o, "bind_interface"), "direct_fallback");
  return { ...o, bind_interface: trimmed };
};

/** Never writes `false` - an unset flag and a false one mean the same, and
 * omitting keeps the JSON clean. */
export const setOutboundDirectFallback = (o: Outbound, on: boolean): Outbound =>
  on && outboundSupportsDirectFallback(o) ? { ...o, direct_fallback: true } : omit(o, "direct_fallback");

/** inbound_port is only valid alongside a non-empty inbound, so emptying the
 * inbound selection drops the ports with it. */
export const setRuleInbounds = (r: RoutingRule, tags: string[]): RoutingRule =>
  tags.length === 0 ? omit(omit(r, "inbound"), "inbound_port") : { ...r, inbound: tags };

export const setRuleInboundPorts = (r: RoutingRule, ports: number[]): RoutingRule =>
  ports.length === 0 || !r.inbound || r.inbound.length === 0 ? omit(r, "inbound_port") : { ...r, inbound_port: ports };

const OTHER_MATCHERS = [
  "domain",
  "domain_suffix",
  "domain_keyword",
  "ip_cidr",
  "port",
  "port_range",
  "network",
  "protocol",
] as const;

/** One short "field: a, b (+n)" string per matcher the rule uses besides
 * inbound/inbound_port, for the read-only rule summary. Field names are the
 * JSON keys on purpose - they are what the admin sees in the editor below. */
export const otherMatcherSummary = (r: RoutingRule, maxValues = 3): string[] => {
  const parts: string[] = [];
  for (const key of OTHER_MATCHERS) {
    const values = r[key] as ReadonlyArray<string | number> | undefined;
    if (!Array.isArray(values) || values.length === 0) continue;
    const shown = values.slice(0, maxValues).join(", ");
    const hidden = values.length - maxValues;
    parts.push(hidden > 0 ? `${key}: ${shown} (+${hidden})` : `${key}: ${shown}`);
  }
  if (r.ip_is_private) parts.push("ip_is_private");
  return parts;
};

export type ConfigIssueKind =
  | "inboundPortNotList"
  | "inboundPortNeedsInbound"
  | "inboundPortNotInteger"
  | "inboundPortOutOfRange"
  | "inboundPortDuplicate"
  | "directFallbackNotBoolean"
  | "directFallbackNeedsInterface"
  | "directFallbackWrongType";

export type ConfigIssue = {
  scope: "rule" | "outbound";
  /** Zero-based position in routing_rules / outbounds. */
  index: number;
  /** The rule's outbound_tag or the outbound's own tag, for the message. */
  ref: string;
  kind: ConfigIssueKind;
  /** The offending port / type, when the message names it. */
  value?: string;
};

const isObject = (v: unknown): v is Record<string, unknown> => typeof v === "object" && v !== null && !Array.isArray(v);

/**
 * Checks the two contract rules that the JSON editor would otherwise only
 * learn about from a rejected Apply: a rule's inbound_port, and an outbound's
 * direct_fallback. Everything else is left to the server's own validation.
 */
export const validateCoreConfigDoc = (doc: unknown): ConfigIssue[] => {
  const issues: ConfigIssue[] = [];
  if (!isObject(doc)) return issues;

  if (Array.isArray(doc.routing_rules)) {
    doc.routing_rules.forEach((rule, index) => {
      if (!isObject(rule)) return;
      const problem = validateInboundPortField(rule.inbound_port, rule.inbound);
      if (!problem) return;
      const ref = typeof rule.outbound_tag === "string" ? rule.outbound_tag : "";
      const kind: ConfigIssueKind =
        problem.kind === "notList"
          ? "inboundPortNotList"
          : problem.kind === "needsInbound"
          ? "inboundPortNeedsInbound"
          : problem.kind === "notInteger"
          ? "inboundPortNotInteger"
          : problem.kind === "outOfRange"
          ? "inboundPortOutOfRange"
          : "inboundPortDuplicate";
      issues.push({ scope: "rule", index, ref, kind, value: "value" in problem ? problem.value : undefined });
    });
  }

  if (Array.isArray(doc.outbounds)) {
    doc.outbounds.forEach((outbound, index) => {
      if (!isObject(outbound) || outbound.direct_fallback === undefined || outbound.direct_fallback === null) return;
      const ref = typeof outbound.tag === "string" ? outbound.tag : "";
      if (typeof outbound.direct_fallback !== "boolean") {
        issues.push({ scope: "outbound", index, ref, kind: "directFallbackNotBoolean" });
        return;
      }
      if (!outbound.direct_fallback) return;
      if (!isLeafOutboundType(outbound.type)) {
        issues.push({ scope: "outbound", index, ref, kind: "directFallbackWrongType", value: String(outbound.type ?? "") });
      } else if (!hasBindInterface(outbound.bind_interface)) {
        issues.push({ scope: "outbound", index, ref, kind: "directFallbackNeedsInterface" });
      }
    });
  }

  return issues;
};
