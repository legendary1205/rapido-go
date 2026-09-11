import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { fetch } from "service/http";
import { InboundsByProtocol } from "types/Inbound";
import { XrayImportRequest, XrayImportResult } from "types/XrayImport";
import { queryKeys } from "utils/queryClient";

// GET /api/inbounds - protocol -> tag list (see types/Inbound.ts on why this
// is simpler than the old dashboard's per-inbound-metadata shape). Shared by
// the Users and User Templates forms via rapido-ui/InboundsPicker.tsx, and
// by rapido-ui/HostsAdmin.tsx's own "which tag to add a host under" picker.
// Inbound management itself (create/edit/delete) now lives entirely in the
// merged Xray Config page (rapido-ui/XrayConfigAdmin.tsx, hooks/
// useXrayConfigQuery.ts) - this hook is read-only from here on.
export const useInboundsQuery = () =>
  useQuery({
    queryKey: queryKeys.inbounds,
    queryFn: () => fetch<InboundsByProtocol>("/inbounds"),
  });

// POST /api/inbounds/import-xray - called twice per real import: once
// with confirm:false (a pure preview - parses and reports counts/warnings,
// writes nothing) and, only if the admin reviews that and proceeds, again
// with confirm:true (actually writes). Both calls use this same mutation;
// the caller decides the body's `confirm` value. A confirmed import
// touches inbounds, hosts, and core-config, so all of their caches (plus
// the merged xrayConfig query that reads two of them at once) are
// invalidated on success - but only when something was actually written (a
// preview response comes back with the exact same shape, so gate the
// invalidation on `applied` rather than skip it for preview calls via a
// separate code path).
export const useImportXrayConfigMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: XrayImportRequest) =>
      fetch<XrayImportResult>("/inbounds/import-xray", { method: "POST", body }),
    onSuccess: (result) => {
      if (!result.applied) return;
      queryClient.invalidateQueries({ queryKey: queryKeys.inbounds });
      queryClient.invalidateQueries({ queryKey: queryKeys.hosts });
      queryClient.invalidateQueries({ queryKey: queryKeys.xrayConfig });
    },
  });
};
