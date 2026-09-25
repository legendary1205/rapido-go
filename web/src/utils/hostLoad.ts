// The pure half of the load indicators: what a percent means, and which of a
// config's two limits is the one it is being held to.

import { HostLoadEntry, HostLoadLevel, HostsLoad, NodeLoadEntry } from "types/HostLoad";

const isNumber = (v: unknown): v is number => typeof v === "number" && Number.isFinite(v);

/**
 * The level a percent falls in. The thresholds are the ones the panel uses for
 * a host (free < 40, normal < 70, busy < 90, full from 90), so a node chip and
 * a host pill of the same colour mean the same thing. Not a number is
 * "unknown", never a colour it cannot back.
 */
export const levelForPercent = (percent: number): HostLoadLevel => {
  if (!isNumber(percent)) return "unknown";
  if (percent < 40) return "free";
  if (percent < 70) return "normal";
  if (percent < 90) return "busy";
  return "full";
};

/** A percent as a whole number in 0..100, for display. */
export const clampPercent = (percent: number): number => Math.round(Math.min(100, Math.max(0, percent)));

export type HostLimit = {
  /** Which limit holds the config's percent where it is. */
  by: "node" | "config";
  /** How full the node(s) are as a whole, 0..100. */
  node: number;
  /** How full this config alone is, 0..100. */
  port: number;
};

/**
 * Which limit applies to a host: its node's (every config on it shares the
 * node's CPU, so a quiet config on a busy node is as full as the node) or its
 * own. The panel takes the larger of the two as the host's percent, so the
 * larger is the one that applies; a tie goes to the config. Null when the panel
 * did not send both numbers (one older than per-node capacity).
 */
export const hostLimit = (load: Pick<HostLoadEntry, "node_percent" | "port_percent">): HostLimit | null => {
  if (!isNumber(load.node_percent) || !isNumber(load.port_percent)) return null;
  const node = clampPercent(load.node_percent);
  const port = clampPercent(load.port_percent);
  return { by: load.node_percent > load.port_percent ? "node" : "config", node, port };
};

/** The reporting nodes of a load response by node id; empty when it has none. */
export const nodeLoadById = (load: Pick<HostsLoad, "nodes"> | null | undefined): Map<number, NodeLoadEntry> =>
  new Map((load?.nodes ?? []).map((n) => [n.id, n] as const));
