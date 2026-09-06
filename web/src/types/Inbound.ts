// GET /api/inbounds returns a raw map of protocol -> tag list
// (map[string][]string on the Go side - see internal/httpapi/inbounds.go's
// handleListInbounds) rather than the old Python-targeting frontend's
// objects-with-metadata shape (tag/protocol/network/tls/port). Per the plan's
// key fact #3, this makes inbound selection simpler than before: there is no
// per-inbound metadata to display, only tags to check off.
export type InboundsByProtocol = Record<string, string[]>;
