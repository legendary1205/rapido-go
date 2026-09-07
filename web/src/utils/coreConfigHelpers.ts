import { DNSServer, IMPLICIT_OUTBOUND_TAGS, Outbound, RoutingRule } from "types/CoreConfig";

// ---------------------------------------------------------------------------
// Comma-separated-text <-> string[]/number[] parsing, the same convention
// utils/buildIntegrationsPatch.ts already uses for webhook_addresses /
// telegram_admin_ids (a plain comma-separated <input>, not a bespoke tag-chip
// widget) - reused here for domain/domain_suffix/domain_keyword/ip_cidr/
// port_range/port, the multi-value routing-rule fields.

export const parseCommaList = (raw: string): string[] =>
  raw
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);

export const parseCommaIntList = (raw: string): number[] =>
  parseCommaList(raw)
    .map(Number)
    .filter((n) => Number.isFinite(n));

export const formatList = (arr?: string[] | null): string => (arr ?? []).join(", ");

export const formatIntList = (arr?: number[] | null): string => (arr ?? []).join(", ");

// ---------------------------------------------------------------------------
// Outbound-tag pickers. Every dropdown/checkbox-list in this page that
// targets an outbound (a routing rule's outbound_tag, a selector/urltest's
// member list) draws from the same universe validateCoreConfig checks
// against server-side: the two always-available implicit built-ins, plus
// every custom outbound tag currently defined - minus, for a selector/
// urltest's own member picker, the outbound being edited itself (it cannot
// list itself as a member).

// `implicit` tells the caller which of the two always-available built-ins
// this option is, so it can render a clear label (e.g. "Direct (built-in)")
// while still submitting the literal "direct"/"block" string as the value -
// these two are never in `outbounds`, so there's no name collision to
// disambiguate the other way.
export type OutboundOption = { value: string; implicit: boolean };

export const buildOutboundOptions = (
  outbounds: Outbound[],
  excludeTag?: string
): OutboundOption[] => [
  { value: "direct", implicit: true },
  { value: "block", implicit: true },
  ...outbounds
    .filter((ob) => ob.tag && ob.tag !== excludeTag)
    .map((ob) => ({ value: ob.tag, implicit: false })),
];

// ---------------------------------------------------------------------------
// Client-side "obviously required" validation - a UX nicety only. The
// server's validateCoreConfig (internal/httpapi/coreconfig.go) is the real
// source of truth and is re-checked on every PUT regardless; a failed save
// surfaces its `detail` message verbatim rather than this page trying to
// fully replicate every rule (duplicate tags, unknown outbound references,
// etc. are left to the server).
//
// Each function returns an i18n key (under rapido.coreConfig.*) rather than
// a literal message, same as the rest of this page - these are real
// user-facing sentences (unlike e.g. summarizeRoutingRule's field names
// below, which are technical/API terms left untranslated on purpose), so
// the caller is expected to render the result through t().

export const validateOutboundDraft = (ob: Outbound): string | null => {
  if (!ob.tag.trim()) return "rapido.coreConfig.errorTagRequired";
  if ((IMPLICIT_OUTBOUND_TAGS as readonly string[]).includes(ob.tag.trim())) {
    return "rapido.coreConfig.errorTagReserved";
  }
  if (ob.type === "socks" || ob.type === "http") {
    if (!ob.server?.trim() || !ob.server_port) {
      return "rapido.coreConfig.errorServerRequired";
    }
  }
  if (ob.type === "selector" || ob.type === "urltest") {
    if (!ob.outbounds || ob.outbounds.length === 0) {
      return "rapido.coreConfig.errorMemberRequired";
    }
  }
  return null;
};

export const validateRoutingRuleDraft = (rule: RoutingRule): string | null => {
  if (!rule.outbound_tag) return "rapido.coreConfig.errorOutboundRequired";
  return null;
};

export const validateDnsServerDraft = (srv: DNSServer): string | null => {
  if (!srv.tag.trim()) return "rapido.coreConfig.errorTagRequired";
  if (srv.type !== "local" && !srv.address?.trim()) {
    return "rapido.coreConfig.errorAddressRequired";
  }
  return null;
};

// ---------------------------------------------------------------------------
// Reordering for the routing rules list - rules apply top-to-bottom, so
// position is meaningful. Plain array-splice up/down rather than a
// drag-and-drop library: the list is short (an admin's own routing table),
// so two buttons are simpler than a new dependency for the same result. A
// move past either end is a no-op, returning the same array reference so
// callers can skip a state update / re-render when nothing actually moved.
export const moveItem = <T>(arr: T[], index: number, direction: -1 | 1): T[] => {
  const target = index + direction;
  if (index < 0 || index >= arr.length || target < 0 || target >= arr.length) {
    return arr;
  }
  const next = arr.slice();
  const [item] = next.splice(index, 1);
  next.splice(target, 0, item);
  return next;
};

// ---------------------------------------------------------------------------

// Short, order-preserving summary of a routing rule's match criteria, for
// the row view in the Routing Rules list - "match: any" for a rule with no
// criteria at all (valid: it matches every request and just names an
// outbound), otherwise every non-empty field joined with " · ".
export const summarizeRoutingRule = (rule: RoutingRule): string => {
  const parts: string[] = [];
  if (rule.domain?.length) parts.push(`domain: ${rule.domain.join(", ")}`);
  if (rule.domain_suffix?.length) parts.push(`domain_suffix: ${rule.domain_suffix.join(", ")}`);
  if (rule.domain_keyword?.length) parts.push(`domain_keyword: ${rule.domain_keyword.join(", ")}`);
  if (rule.ip_cidr?.length) parts.push(`ip_cidr: ${rule.ip_cidr.join(", ")}`);
  if (rule.ip_is_private) parts.push("ip_is_private");
  if (rule.port?.length) parts.push(`port: ${rule.port.join(", ")}`);
  if (rule.port_range?.length) parts.push(`port_range: ${rule.port_range.join(", ")}`);
  if (rule.network?.length) parts.push(`network: ${rule.network.join(", ")}`);
  if (rule.protocol?.length) parts.push(`protocol: ${rule.protocol.join(", ")}`);
  return parts.length ? parts.join(" · ") : "match: any";
};
