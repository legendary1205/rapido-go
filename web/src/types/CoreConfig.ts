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
  "shadowsocks",
  "vmess",
  "trojan",
  "vless",
  "hysteria2",
  "tuic",
  "selector",
  "urltest",
] as const;
export type OutboundType = (typeof OUTBOUND_TYPES)[number];

// The realistically-used sing-box outbound protocols (confirmed with the
// user rather than assumed) - not sing-box's full catalog. WireGuard is
// deliberately excluded: sing-box models it as an "Endpoint", a
// structurally different top-level config section this rewrite doesn't
// support yet, not just another outbound type (see coreconfig.go's own
// doc comment). Exotic/legacy protocols (Hysteria v1, ShadowsocksR, Naive,
// Tor, SSH, ShadowTLS, AnyTLS) aren't exposed here either.

export const SHADOWSOCKS_METHODS = [
  "none",
  "aes-128-gcm",
  "aes-192-gcm",
  "aes-256-gcm",
  "chacha20-ietf-poly1305",
  "xchacha20-ietf-poly1305",
  "2022-blake3-aes-128-gcm",
  "2022-blake3-aes-256-gcm",
  "2022-blake3-chacha20-poly1305",
] as const;
export type ShadowsocksMethod = (typeof SHADOWSOCKS_METHODS)[number];

export const VMESS_SECURITY_TYPES = ["auto", "none", "zero", "aes-128-gcm", "chacha20-poly1305"] as const;
export type VmessSecurity = (typeof VMESS_SECURITY_TYPES)[number];

export const CONGESTION_CONTROL_TYPES = ["cubic", "new_reno", "bbr"] as const;
export type CongestionControl = (typeof CONGESTION_CONTROL_TYPES)[number];

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

  /** vmess / vless / tuic */
  uuid?: string;
  /** vless (optional - e.g. "xtls-rprx-vision") */
  flow?: string;
  /** shadowsocks */
  method?: ShadowsocksMethod;
  /** vmess encryption */
  security?: VmessSecurity;
  /** tuic */
  congestion_control?: CongestionControl;

  /** Shared TLS subset for vmess/trojan/vless (genuinely optional there)
   * and hysteria2/tuic (mandatory at the transport level for those two -
   * the form hides this toggle and always sends tls_enabled: true for
   * them, since QUIC requires TLS). */
  tls_enabled?: boolean;
  tls_server_name?: string;
  tls_insecure?: boolean;
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
