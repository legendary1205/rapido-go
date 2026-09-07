import { QueryClient } from "@tanstack/react-query";

// One root QueryClient for the whole app (see main.tsx), not one per page.
// The old dashboard had two: the app-wide `react-query` v3 client in
// utils/react-query.ts, and OverviewNew.tsx's own private
// `@tanstack/react-query` v5 QueryClient. That second client meant a write
// from the Users page (create/delete user) could never invalidate the
// Overview's cached stats - it lived in a cache nothing else could see. A
// single client fixes that outright: hooks/useUsersQuery.ts's mutations
// invalidate the same `["system-stats"]` key useSystemStatsQuery.ts reads.
export const queryClient = new QueryClient();

// Central query-key registry so a mutation in one hook file can invalidate a
// query defined in another without either importing the other's internals.
export const queryKeys = {
  currentAdmin: ["current-admin"] as const,
  users: (filters: Record<string, unknown>) => ["users", filters] as const,
  inbounds: ["inbounds"] as const,
  inboundsDetail: ["inbounds-detail"] as const,
  hosts: ["hosts"] as const,
  coreConfig: ["core-config"] as const,
  admins: ["admins"] as const,
  tickets: (filters: Record<string, unknown>) => ["tickets", filters] as const,
  ticket: (id: number | null) => ["ticket", id] as const,
  userTemplates: ["user-templates"] as const,
  systemStats: ["system-stats"] as const,
  systemUsageHistory: (days: number) =>
    ["system-usage-history", days] as const,
  integrationSettings: ["integration-settings"] as const,
  nodes: ["nodes"] as const,
  nodesUsage: (start?: string, end?: string) =>
    ["nodes-usage", start ?? null, end ?? null] as const,
  // Shared verbatim between MonitoringPage's own cards and OverviewNew.tsx's
  // reinstated fleet sections - same key, so react-query serves both from
  // one cache entry/poller instead of two independent ones (the exact bug
  // OverviewNew.tsx's own private QueryClientProvider used to cause, see
  // that file's history).
  monitoring: ["monitoring"] as const,
  monitoringHistory: (nodeId: number | null) =>
    ["monitoring-history", nodeId] as const,
  backups: ["backups"] as const,
  gatewaySettings: ["gateway-settings"] as const,
  gatewayPeers: ["gateway-peers"] as const,
};
