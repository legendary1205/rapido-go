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
// security is "reality".
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
};
