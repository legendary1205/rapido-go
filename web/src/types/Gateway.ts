// Mirrors internal/httpapi/gateway.go's gatewaySettingsDTO - this panel's
// own identity for the multi-panel load balancer. `secret` is returned in
// full (not masked, unlike every other secret in types/Backup.ts's
// siblings): its whole purpose is to be copy-pasted into a peer panel's
// "add peer" form.
export type GatewaySettings = {
  name: string;
  secret: string;
};

// Mirrors gatewayPeerDTO - a panel THIS install calls out to. `secret`
// here is the PEER's own inbound secret (what this panel sends as its
// Bearer token when calling that peer), not this panel's own.
export type GatewayPeer = {
  id: number;
  name: string;
  base_url: string;
  secret: string;
  enabled: boolean;
};

export type GatewayPeerRequest = {
  name: string;
  base_url: string;
  secret: string;
  enabled?: boolean;
};

export type GatewayTestResult = {
  ok: boolean;
  panel_name?: string;
  detail?: string;
  // Both absent if the ping itself succeeded but the follow-up status call
  // (sub-phase 3) didn't - see internal/httpapi/gateway.go's
  // handleTestGatewayPeer, that failure never turns a real "ok" into "not ok".
  crowdedness?: number;
  host_count?: number;
};
