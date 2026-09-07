import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { fetch } from "service/http";
import { CoreConfig } from "types/CoreConfig";
import { queryKeys } from "utils/queryClient";

export const useCoreConfigQuery = () =>
  useQuery({
    queryKey: queryKeys.coreConfig,
    queryFn: () => fetch<CoreConfig>("/settings/core-config"),
  });

// PUT /api/settings/core-config is a full-object replace, not a per-section
// patch (see internal/httpapi/coreconfig.go's handleUpdateCoreConfig, same
// convention as PUT /hosts) - the caller must always send the whole shape
// back: log level, sniffing, every outbound, every routing rule, every DNS
// server. That invariant is CoreConfigAdmin.tsx's responsibility (it keeps
// one full in-memory copy and only ever PUTs the whole thing); this hook
// just performs the write and refreshes the cache with whatever the backend
// echoes back.
export const useSaveCoreConfigMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: CoreConfig) =>
      fetch<CoreConfig>("/settings/core-config", { method: "PUT", body }),
    onSuccess: (data) => {
      queryClient.setQueryData(queryKeys.coreConfig, data);
    },
  });
};
