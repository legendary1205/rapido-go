import { useQuery } from "@tanstack/react-query";
import { fetch } from "service/http";
import { UsagePoint } from "types/System";
import { queryKeys } from "utils/queryClient";

export const useSystemUsageHistoryQuery = (days = 14) =>
  useQuery({
    queryKey: queryKeys.systemUsageHistory(days),
    queryFn: () => fetch<UsagePoint[]>(`/system/usage-history?days=${days}`),
  });
