import { BadgeTone } from "rapido-ui/Badge";
import { MonitoringHost, MonitoringTunnel } from "types/Monitoring";

// "down-fallback" is a down tunnel whose exit traffic the node is currently
// sending over a plain direct connection: users still work, but they exit
// from the server's own IP, which is a different (milder) situation from a
// tunnel that is down with nothing behind it - hence its own status and its
// own colour rather than a flag on "down".
export type TunnelStatus = "up" | "down" | "missing" | "down-fallback";

type StatusFields = Pick<MonitoringTunnel, "up" | "present" | "fallback_active">;

export const tunnelStatus = (t: StatusFields): TunnelStatus => {
  // A missing interface can't be up whatever `up` claims, and it is the more
  // specific diagnosis, so it wins over a plain "down".
  if (!t.present) return "missing";
  if (t.up) return "up";
  return t.fallback_active ? "down-fallback" : "down";
};

export const isTunnelUp = (t: StatusFields): boolean => tunnelStatus(t) === "up";

export const tunnelTone = (status: TunnelStatus): BadgeTone => {
  switch (status) {
    case "up":
      return "green";
    case "down-fallback":
      return "yellow";
    case "down":
    case "missing":
      return "red";
  }
};

export type HandshakeAge =
  | { kind: "never" }
  | { kind: "ago"; unit: "seconds" | "minutes" | "hours" | "days"; value: number };

/**
 * How long ago the last handshake was, as a unit + whole value the UI can
 * put in a translated "N min ago". Prefers the server-computed age (immune to
 * this browser's clock being wrong) and only falls back to last_handshake
 * (unix seconds) when the age is absent; 0 there means unknown/never.
 */
export const handshakeAge = (
  t: Pick<MonitoringTunnel, "last_handshake" | "handshake_age_seconds">,
  nowSeconds: number = Date.now() / 1000
): HandshakeAge => {
  let age: number;
  if (typeof t.handshake_age_seconds === "number" && Number.isFinite(t.handshake_age_seconds)) {
    age = t.handshake_age_seconds;
  } else if (t.last_handshake > 0) {
    age = nowSeconds - t.last_handshake;
  } else {
    return { kind: "never" };
  }
  const seconds = Math.max(0, Math.floor(age));
  if (seconds < 60) return { kind: "ago", unit: "seconds", value: seconds };
  if (seconds < 3600) return { kind: "ago", unit: "minutes", value: Math.floor(seconds / 60) };
  if (seconds < 86400) return { kind: "ago", unit: "hours", value: Math.floor(seconds / 3600) };
  return { kind: "ago", unit: "days", value: Math.floor(seconds / 86400) };
};

const byName = (a: string, b: string): number =>
  a.localeCompare(b, undefined, { numeric: true, sensitivity: "base" }) || (a < b ? -1 : a > b ? 1 : 0);

/** Not-up tunnels first (that is what an admin opens the card for), then by name. */
export const sortTunnels = <T extends StatusFields & { name: string }>(tunnels: readonly T[]): T[] =>
  [...tunnels].sort((a, b) => Number(isTunnelUp(a)) - Number(isTunnelUp(b)) || byName(a.name, b.name));

export type TunnelProblem = {
  hostName: string;
  tunnel: MonitoringTunnel;
  status: Exclude<TunnelStatus, "up">;
};

/** "node: tunnel", the label the fleet-wide warning lists. */
export const tunnelProblemLabel = (p: TunnelProblem): string => `${p.hostName}: ${p.tunnel.name}`;

export type FleetTunnelSummary = {
  problems: TunnelProblem[];
  /** Every tunnel the fleet reports per-tunnel detail for. */
  total: number;
  /** Problems whose exit traffic is currently going out over the direct fallback. */
  withFallback: number;
  /** warning: every problem is a down tunnel covered by the direct fallback;
   * critical: anything else (down with nothing behind it, or a missing interface). */
  severity: "none" | "warning" | "critical";
};

/**
 * Everything the fleet-wide banner needs. Hosts that never reported (no
 * agent) are skipped - their tunnel list is empty or meaningless - while a
 * merely stale host still counts, its card already says its data is old.
 */
export const summarizeFleetTunnels = (
  hosts: readonly Pick<MonitoringHost, "name" | "reachable" | "tunnels">[]
): FleetTunnelSummary => {
  const problems: TunnelProblem[] = [];
  let total = 0;
  for (const host of hosts) {
    if (!host.reachable || !host.tunnels) continue;
    total += host.tunnels.length;
    for (const tunnel of sortTunnels(host.tunnels)) {
      const status = tunnelStatus(tunnel);
      if (status !== "up") problems.push({ hostName: host.name, tunnel, status });
    }
  }
  const withFallback = problems.filter((p) => p.tunnel.fallback_active).length;
  return {
    problems,
    total,
    withFallback,
    severity:
      problems.length === 0 ? "none" : problems.every((p) => p.status === "down-fallback") ? "warning" : "critical",
  };
};
