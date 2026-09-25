// Mirrors GET /api/hosts/load (sudo only): the live "which config is emptier"
// numbers - open connections on a host's port, on the node(s) serving it.
export type HostLoadLevel = "free" | "normal" | "busy" | "full" | "unknown";

export type HostLoadEntry = {
  host_id: number;
  remark: string;
  address: string;
  port: number;
  /** Open client connections right now. */
  conns: number;
  /** 0-100, conns / capacity clamped. */
  percent: number;
  level: HostLoadLevel;
  node_ids: number[];
};

export type HostsLoad = {
  /** Connections per config that count as 100%. */
  capacity: number;
  /** True when hosts without any {VARIABLE} in the remark get {LOAD}
   * appended in subscriptions automatically. */
  indicator: boolean;
  sort_by_load: boolean;
  updated_at: string;
  hosts: HostLoadEntry[];
};
