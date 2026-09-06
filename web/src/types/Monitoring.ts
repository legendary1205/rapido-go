// Mirrors internal/httpapi/monitoring.go's monitoringHostDTO/
// historyPointDTO. Unlike the old Python system, there is no per-tunnel/
// per-peer WireGuard detail here (tunnels_up/tunnels_total are just counts),
// and no uptime/load-average/xray fields at all - the Go node agent doesn't
// report them (see the Phase 7.3 plan's context on what host_metrics
// actually stores).
export type MonitoringHost = {
  /** null identifies the panel's own self-sample (name is literally "Panel"). */
  node_id: number | null;
  name: string;
  /** Omitted by the backend (json:",omitempty") for the panel's own row. */
  address?: string;
  /**
   * True once this host has ever pushed a report. False means no
   * host_metrics row exists at all yet - every numeric field below is then
   * null, and that must render as a distinct "no data yet" state, never as
   * zeros.
   */
  reachable: boolean;
  collected_at: string | null;
  /** The last report is more than 2 minutes old - show the last-known
   * numbers, but dimmed/flagged, not hidden. */
  stale: boolean;
  cpu_percent: number | null;
  mem_percent: number | null;
  disk_percent: number | null;
  rx_rate: number | null;
  tx_rate: number | null;
  connections: number | null;
  tunnels_up: number | null;
  tunnels_total: number | null;
  healthy: boolean;
};

export type MonitoringSnapshot = {
  hosts: MonitoringHost[];
  generated_at: string;
};

export type MonitoringHistoryPoint = {
  t: string;
  cpu: number | null;
  mem: number | null;
  rx: number | null;
  tx: number | null;
  conns: number | null;
};
