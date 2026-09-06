import { BadgeTone } from "rapido-ui/Badge";
import { formatBytes } from "utils/formatByte";
import { MonitoringHost } from "types/Monitoring";

// A host with `reachable: false` has never pushed a host_metrics row at all
// (see internal/httpapi/monitoring.go's toMonitoringHostDTO: a nil metric
// leaves every numeric field null) - that must never be conflated with a
// live "everything is 0%" reading, hence its own state distinct from a
// merely stale one.
export type HostDisplayState = "healthy" | "stale" | "unhealthy" | "no-data";

export const hostDisplayState = (
  host: Pick<MonitoringHost, "reachable" | "stale" | "healthy">
): HostDisplayState => {
  if (!host.reachable) return "no-data";
  if (host.stale) return "stale";
  if (!host.healthy) return "unhealthy";
  return "healthy";
};

export const hostTone = (
  host: Pick<MonitoringHost, "reachable" | "stale" | "healthy">
): BadgeTone => {
  switch (hostDisplayState(host)) {
    case "healthy":
      return "green";
    case "stale":
      return "yellow";
    case "unhealthy":
      return "red";
    case "no-data":
      return "gray";
  }
};

/** Bytes per second, in the same shape the rest of the panel shows sizes. */
export const formatRate = (v: number | null | undefined): string =>
  v === null || v === undefined ? "—" : `${formatBytes(v)}/s`;

/**
 * The same thresholds for every CPU/mem/disk meter, so a number that's amber
 * on one card means the same thing on the next: green below 70, amber to 90,
 * red above. "empty" is its own bucket (not "green") so a null reading (no
 * data yet) renders as an unfilled track, not a false-healthy full-green one.
 */
export type MeterTone = "empty" | "green" | "yellow" | "red";

export const meterTone = (v: number | null | undefined): MeterTone => {
  if (v === null || v === undefined) return "empty";
  if (v >= 90) return "red";
  if (v >= 70) return "yellow";
  return "green";
};
