// Mirrors internal/httpapi/hosts.go's hostDTO exactly - per the plan's key
// fact #7, this DTO is already 1:1 with what the old frontend's ad-hoc Host
// type expected, so nothing here needed adapting versus the Python-era shape.
export type ProxyHostSecurity = "inbound_default" | "none" | "tls";

export type Host = {
  id?: number;
  remark: string;
  address: string;
  port?: number | null;
  path?: string | null;
  sni?: string | null;
  host?: string | null;
  security: string;
  alpn: string;
  fingerprint: string;
  allowinsecure?: boolean | null;
  is_disabled?: boolean | null;
  mux_enable: boolean;
  fragment_setting?: string | null;
  noise_setting?: string | null;
  random_user_agent: boolean;
  use_sni_as_host: boolean;
};

// GET/PUT /api/hosts both key by inbound tag.
export type HostsMap = Record<string, Host[]>;
