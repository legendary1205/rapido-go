// Mirrors internal/httpapi/node.go's nodeDTO/nodeCreateRequest/
// nodeUpdateRequest. "Node" here is the physical/VPS server a node agent
// runs on - not to be confused with types/Host.ts's Host, which is a proxy
// connection endpoint handed out in subscriptions.
export type NodeStatus = "connected" | "connecting" | "error" | "disabled";

export type Node = {
  id: number;
  name: string;
  address: string;
  port: number;
  api_port: number;
  status: NodeStatus;
  usage_coefficient: number;
};

// POST /api/node and PUT /api/node/:id both take the full field set -
// usage_coefficient omitted means "use the default" on create (the backend
// defaults to 1) and "keep the current value" on update (see
// nodeUpdateRequest's own doc comment in node.go).
export type NodeWritePayload = {
  name: string;
  address: string;
  port: number;
  api_port: number;
  usage_coefficient?: number;
};

// PUT-only: disabled omitted keeps the node's current enabled/disabled
// state. There is no separate enable/disable endpoint - this same PUT with
// disabled:true/false is it.
export type NodeUpdatePayload = NodeWritePayload & {
  disabled?: boolean;
};

// The response shape of POST /api/node only. certificate/key/ca_certificate
// and report_secret are each returned exactly this once - never retrievable
// again through any later GET (see node.go's handleCreateNode comment) - so
// this type should never be reused to describe cached/re-read data.
export type NodeCreateResult = {
  node: Node;
  certificate: string;
  key: string;
  ca_certificate: string;
  report_secret: string;
};

// GET /api/nodes/usage's rows - bytes, cumulative over the requested window.
// Every node gets a row even with zero traffic (see GetNodesUsage's own doc
// comment) - unlike the old Python system, node_id is never null here (there
// is no "traffic routed by the main server itself" concept in this backend).
export type NodeUsage = {
  node_id: number;
  node_name: string;
  uplink: number;
  downlink: number;
};

export type NodeUsageResult = {
  usages: NodeUsage[];
};
