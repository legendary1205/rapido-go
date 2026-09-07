// GET /api/inbounds returns a raw map of protocol -> tag list
// (map[string][]string on the Go side - see internal/httpapi/inbounds.go's
// handleListInbounds) rather than the old Python-targeting frontend's
// objects-with-metadata shape (tag/protocol/network/tls/port). Per the plan's
// key fact #3, this makes inbound selection simpler than before: there is no
// per-inbound metadata to display, only tags to check off. Still used as-is
// by InboundsPicker.tsx (Users/User Templates forms) and by the KirBot
// filtering logic server-side - InboundsAdmin.tsx's own management page uses
// the richer Inbound/InboundCreatePayload shapes below instead, from the
// separate GET /api/inbounds/detail endpoint.
export type InboundsByProtocol = Record<string, string[]>;

export type InboundNetwork = "tcp" | "ws" | "grpc" | "kcp" | "quic" | "splithttp" | "xhttp";
export type InboundSecurity = "none" | "tls" | "reality";

// Mirrors internal/httpapi/inbounds.go's inboundDetailDTO - the full-fidelity
// shape GET /api/inbounds/detail returns, as opposed to the plain tag list
// above. reality_* fields are only meaningful (and only ever set) when
// security is "reality"; tls_* only when security is "tls" - a real
// customer-facing certificate/key pair, NOT the panel's own node-mTLS CA.
// An inbound with security "tls" and no tls_certificate/tls_key yet is
// valid but stays out of automatic node sync (see
// ListAutoSyncInbounds's own doc comment) - InboundsAdmin.tsx surfaces
// this with a warning rather than letting it look silently broken.
export type Inbound = {
  tag: string;
  protocol: string;
  network: string;
  header_type: string;
  security: string;
  reality_private_key?: string;
  reality_short_ids?: string[];
  reality_server_name?: string;
  reality_server_port?: number;
  tls_certificate?: string;
  tls_key?: string;
  tls_server_name?: string;
};

// POST /api/inbounds/sync's per-entry shape (internal/httpapi/inbounds.go's
// inboundSyncEntry) - the same request body both creates a brand-new
// inbound and re-syncs an existing one's fields (an UPSERT by tag).
export type InboundSyncEntry = {
  tag: string;
  protocol: string;
  network?: string;
  header_type?: string;
  security?: string;
  reality_private_key?: string;
  reality_short_ids?: string[];
  reality_server_name?: string;
  reality_server_port?: number;
  tls_certificate?: string;
  tls_key?: string;
  tls_server_name?: string;
};
