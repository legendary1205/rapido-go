// Mirrors internal/httpapi/xrayimport.go's xrayImportRequest/
// xrayImportResultDTO - POST /api/inbounds/import-xray parses a raw Xray
// core-config JSON file (internal/xrayimport) and, with confirm:true,
// writes every inbound/host/outbound/routing-rule/dns-server it can map
// onto this codebase's own shapes. With confirm:false (or omitted) it only
// previews: parses and reports counts + warnings, writes nothing.
export type XrayImportRequest = {
  config: string;
  confirm: boolean;
};

export type XrayImportResult = {
  applied: boolean;
  inbounds_created: number;
  outbounds_saved: number;
  routing_rules_saved: number;
  dns_servers_saved: number;
  warnings: string[];
};
