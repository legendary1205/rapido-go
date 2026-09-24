// A routing rule's optional `inbound_port`: "only match connections that
// arrived on these local listen ports of the selected inbound(s)". The server
// requires a non-empty `inbound`, every port in 1..65535 and no duplicates -
// this file is the client-side mirror of that, so the admin sees the problem
// next to the field instead of as a rejected Apply.

export const MIN_PORT = 1;
export const MAX_PORT = 65535;

export type PortInputError =
  | { kind: "notInteger"; value: string }
  | { kind: "outOfRange"; value: string }
  | { kind: "duplicate"; value: string };

export type PortInputResult =
  | { ok: true; ports: number[] }
  | { ok: false; error: PortInputError };

// Persian and Arabic-Indic digits are what a Persian keyboard types by
// default; treating them as the digits they are beats "not a whole number".
const EASTERN_DIGITS: Record<string, string> = {
  "۰": "0", "۱": "1", "۲": "2", "۳": "3", "۴": "4",
  "۵": "5", "۶": "6", "۷": "7", "۸": "8", "۹": "9",
  "٠": "0", "١": "1", "٢": "2", "٣": "3", "٤": "4",
  "٥": "5", "٦": "6", "٧": "7", "٨": "8", "٩": "9",
};

/** Separators: commas (incl. the Arabic comma), semicolons and whitespace. */
const SEPARATORS = /[\s,;،؛]+/;

/**
 * Parses what the admin typed into the ports field. Empty input is a valid
 * "no port restriction" (ok, zero ports), not an error. Order is kept as
 * typed; the first problem found is the one reported.
 */
export const parseInboundPortInput = (input: string): PortInputResult => {
  const normalized = input.replace(/[۰-۹٠-٩]/g, (d) => EASTERN_DIGITS[d]);
  const tokens = normalized.split(SEPARATORS).filter((tok) => tok !== "");
  const ports: number[] = [];
  const seen = new Set<number>();
  for (const token of tokens) {
    if (!/^\d+$/.test(token)) return { ok: false, error: { kind: "notInteger", value: token } };
    const port = Number(token);
    if (port < MIN_PORT || port > MAX_PORT) return { ok: false, error: { kind: "outOfRange", value: token } };
    if (seen.has(port)) return { ok: false, error: { kind: "duplicate", value: token } };
    seen.add(port);
    ports.push(port);
  }
  return { ok: true, ports };
};

/** The inverse of parseInboundPortInput, for seeding the field. */
export const formatInboundPorts = (ports: readonly number[] | null | undefined): string =>
  (ports ?? []).join(", ");

/**
 * A compact rendering of a port list: at most `maxShown` ports, then an
 * ellipsis when the rest is cut. `total` lets the caller append its own
 * localized "(N ports)" - only meaningful when `truncated`.
 */
export const summarizePorts = (
  ports: readonly number[] | null | undefined,
  maxShown = 5
): { text: string; total: number; truncated: boolean } => {
  const list = ports ?? [];
  if (list.length <= maxShown) return { text: list.join(", "), total: list.length, truncated: false };
  return { text: `${list.slice(0, maxShown).join(", ")}, …`, total: list.length, truncated: true };
};

/**
 * GET /api/inbounds (protocol -> entries) reduced to tag -> every listen
 * port. A backend that predates `ports` only sends the single `port`, so fall
 * back to that; 0 means "no host yet", not a port.
 */
export const portsByTag = (
  raw: Record<string, ReadonlyArray<string | { tag: string; port?: number; ports?: number[] }>> | null | undefined
): Record<string, number[]> => {
  const byTag: Record<string, number[]> = {};
  for (const entries of Object.values(raw ?? {})) {
    for (const entry of entries ?? []) {
      if (typeof entry === "string") continue;
      byTag[entry.tag] = Array.isArray(entry.ports) ? entry.ports : entry.port ? [entry.port] : [];
    }
  }
  return byTag;
};

export type InboundPortIssue =
  | { kind: "notList" }
  | { kind: "needsInbound" }
  | PortInputError;

/**
 * Checks a rule's raw `inbound_port` (as it sits in the parsed JSON, so
 * possibly hand-edited into a wrong shape) against the same rules the server
 * enforces. Absent, null and an empty list all mean "no restriction".
 */
export const validateInboundPortField = (ports: unknown, inbound: unknown): InboundPortIssue | null => {
  if (ports === undefined || ports === null) return null;
  if (!Array.isArray(ports)) return { kind: "notList" };
  if (ports.length === 0) return null;
  const hasInbound = Array.isArray(inbound) && inbound.length > 0;
  if (!hasInbound) return { kind: "needsInbound" };
  const seen = new Set<number>();
  for (const p of ports) {
    if (typeof p !== "number" || !Number.isInteger(p)) return { kind: "notInteger", value: String(p) };
    if (p < MIN_PORT || p > MAX_PORT) return { kind: "outOfRange", value: String(p) };
    if (seen.has(p)) return { kind: "duplicate", value: String(p) };
    seen.add(p);
  }
  return null;
};
