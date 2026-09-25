// Mirrors GET /api/hosts/load (sudo only): the live "which config is emptier"
// numbers - open connections on a host's port, on the node(s) serving it - and
// how loaded each reporting node is as a whole.
export type HostLoadLevel = "free" | "normal" | "busy" | "full" | "unknown";

export type HostLoadEntry = {
  host_id: number;
  remark: string;
  address: string;
  port: number;
  /** Open client connections on this config right now. */
  conns: number;
  /** 0-100: the larger of port_percent and node_percent, clamped. */
  percent: number;
  level: HostLoadLevel;
  node_ids: number[];
  /** How full the node(s) this config sits on are as a whole (every config on
   * a node shares its CPU). Absent from a panel older than per-node capacity. */
  node_percent?: number;
  /** How full this config alone is against those nodes' capacity. Absent from
   * a panel older than per-node capacity. */
  port_percent?: number;
};

/** Where a node's capacity came from: its own setting, or the panel default. */
export type NodeCapacitySource = "node" | "default";

/** One reporting node's overall load: all its open client connections. */
export type NodeLoadEntry = {
  id: number;
  name: string;
  /** Open client connections on the node right now, across every config. */
  conns: number;
  /** The connections that mean 100% load for this node. */
  capacity: number;
  capacity_source: NodeCapacitySource;
  /** 0-100, conns / capacity clamped. */
  percent: number;
};

export type HostsLoad = {
  /** The panel-wide default capacity: the connections that count as 100% for
   * a node that has no capacity of its own. */
  capacity: number;
  /** True when hosts without any {VARIABLE} in the remark get {LOAD}
   * appended in subscriptions automatically. */
  indicator: boolean;
  sort_by_load: boolean;
  updated_at: string;
  hosts: HostLoadEntry[];
  /** Every node that is reporting load. Absent from a panel older than
   * per-node capacity; a node that is not reporting is simply not in it. */
  nodes?: NodeLoadEntry[];
};
