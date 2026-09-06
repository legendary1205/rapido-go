import { useQuery } from "@tanstack/react-query";
import { fetch } from "service/http";
import { MonitoringHistoryPoint, MonitoringSnapshot } from "types/Monitoring";
import { queryKeys } from "utils/queryClient";

// Shared verbatim between MonitoringPage's own cards and OverviewNew.tsx's
// reinstated fleet sections - both read this exact query key, so react-query
// serves them from one cache entry/poller instead of two independent 30s
// pollers hitting the backend for the same snapshot.
export const useMonitoringQuery = () =>
  useQuery({
    queryKey: queryKeys.monitoring,
    queryFn: () => fetch<MonitoringSnapshot>("/monitoring"),
    // Matches the collector's own write cadence - asking more often than
    // that would just re-render the same numbers.
    refetchInterval: 30_000,
  });

// Fetched on demand (enabled) rather than eagerly for every card, since a
// history chart is behind a per-card "show history" toggle most viewers
// never open. hours is left unset so the backend applies its own default
// window (handleGetMonitoringHistory defaults to 6 hours when omitted).
export const useMonitoringHistoryQuery = (nodeId: number | null, enabled: boolean) =>
  useQuery({
    queryKey: queryKeys.monitoringHistory(nodeId),
    queryFn: () => {
      const qs = nodeId === null ? "" : `?node_id=${nodeId}`;
      return fetch<MonitoringHistoryPoint[]>(`/monitoring/history${qs}`);
    },
    enabled,
  });
