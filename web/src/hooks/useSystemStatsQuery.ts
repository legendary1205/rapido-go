import { useQuery } from "@tanstack/react-query";
import { fetch } from "service/http";
import { SystemStats } from "types/System";
import { queryKeys } from "utils/queryClient";

// 5 s, not the old 10 s: `online_users` is an exact live count now (open
// connections reported by the nodes every 5 s), so the card is only as live as
// this poll. Only the visible tab polls - refetchIntervalInBackground is off,
// so a backgrounded dashboard costs the panel nothing.
export const useSystemStatsQuery = () =>
  useQuery({
    queryKey: queryKeys.systemStats,
    queryFn: () => fetch<SystemStats>("/system"),
    refetchInterval: 5_000,
    refetchIntervalInBackground: false,
  });
