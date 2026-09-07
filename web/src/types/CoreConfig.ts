// Mirrors internal/httpapi/coreconfig.go's DTOs exactly (field names, JSON
// keys, and the fixed enums the server validates against in
// validateCoreConfig). This is a genuinely new, structured shape - not a
// port of the old (Xray) dashboard's raw-JSON CoreSettings.tsx, which has no
// equivalent here because the underlying engine is sing-box, not Xray, and
// the two config shapes are not interchangeable.

export const LOG_LEVELS = [
  "trace",
  "debug",
  "info",
  "warn",
  "error",
  "fatal",
  "panic",
] as const;
export type LogLevel = (typeof LOG_LEVELS)[number];

export const OUTBOUND_TYPES = [
  "direct",
  "block",
  "socks",
  "http",
  "selector",
  "urltest",
] as const;
export type OutboundType = (typeof OUTBOUND_TYPES)[number];

// The two tags a node always has even with zero custom outbounds configured
// (cmd/node/main.go's buildOptions always emits both) - reserved, so an
// admin can never define a custom outbound under either name (see
// validateCoreConfig's own check in coreconfig.go).
export const IMPLICIT_OUTBOUND_TAGS = ["direct", "block"] as const;
export type ImplicitOutboundTag = (typeof IMPLICIT_OUTBOUND_TAGS)[number];

export type Outbound = {
  tag: string;
  type: OutboundType;
  server?: string;
  server_port?: number;
  username?: string;
  password?: string;
  /** selector/urltest member tags only - ignored for every other type. */
  outbounds?: string[];
};

export const NETWORK_TYPES = ["tcp", "udp", "icmp"] as const;
export type NetworkType = (typeof NETWORK_TYPES)[number];

export const PROTOCOL_TYPES = [
  "tls",
  "http",
  "quic",
  "dns",
  "stun",
  "bittorrent",
  "dtls",
  "ssh",
  "rdp",
  "ntp",
] as const;
export type ProtocolType = (typeof PROTOCOL_TYPES)[number];

export type RoutingRule = {
  domain?: string[];
  domain_suffix?: string[];
  domain_keyword?: string[];
  ip_cidr?: string[];
  ip_is_private?: boolean;
  port?: number[];
  /** Each entry is a literal "start:end" string, e.g. "1000:2000". */
  port_range?: string[];
  network?: NetworkType[];
  protocol?: ProtocolType[];
  /** Must resolve to "direct", "block", or a tag in this config's own
   * outbounds - enforced server-side by validateCoreConfig. */
  outbound_tag: string;
};

export const DNS_SERVER_TYPES = ["local", "udp", "tcp", "tls", "https"] as const;
export type DNSServerType = (typeof DNS_SERVER_TYPES)[number];

export type DNSServer = {
  tag: string;
  type: DNSServerType;
  /** Required for every type except "local". */
  address?: string;
  port?: number;
  /** "https" only. */
  path?: string;
};

export type CoreConfig = {
  log_level: string;
  sniff_enabled: boolean;
  outbounds: Outbound[];
  routing_rules: RoutingRule[];
  dns_servers: DNSServer[];
  updated_at?: string | null;
};
