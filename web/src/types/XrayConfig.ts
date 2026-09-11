// Mirrors internal/httpapi/xrayconfig.go's xrayConfigDTO - CoreConfig and
// the inbounds list merged into one document/one endpoint
// (GET/PUT /api/settings/xray-config), replacing the separate Core Config
// and Inbounds pages this project used to have. See that file's own doc
// comment on why: "Xray settings" (core config + inbounds) is one thing an
// admin thinks about, not two - the split cost two JSON editors and two
// Apply buttons for zero benefit.
import { CoreConfig } from "./CoreConfig";
import { Inbound, InboundSyncEntry } from "./Inbound";

export type XrayConfig = CoreConfig & { inbounds: Inbound[] };

// PUT's request body - inbounds go in using the same upsert-shaped fields
// POST /api/inbounds/sync already accepts (InboundSyncEntry), since the
// server's own handleUpdateXrayConfig converts them identically either way.
export type XrayConfigWritePayload = Omit<XrayConfig, "inbounds" | "updated_at"> & {
  inbounds: InboundSyncEntry[];
};
