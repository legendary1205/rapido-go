import { useQuery } from "@tanstack/react-query";
import { fetch } from "service/http";
import { SystemStats } from "types/System";
import { queryKeys } from "utils/queryClient";

// Matches the old OverviewNew.tsx's own polling cadence for this query.
export const useSystemStatsQuery = () =>
  useQuery({
    queryKey: queryKeys.systemStats,
    queryFn: () => fetch<SystemStats>("/system"),
    refetchInterval: 10_000,
  });
