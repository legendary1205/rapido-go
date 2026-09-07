import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { fetch } from "service/http";
import { Inbound, InboundSyncEntry, InboundsByProtocol } from "types/Inbound";
import { XrayImportRequest, XrayImportResult } from "types/XrayImport";
import { queryKeys } from "utils/queryClient";

// GET /api/inbounds - protocol -> tag list (see types/Inbound.ts on why this
// is simpler than the old dashboard's per-inbound-metadata shape). Shared by
// the Users and User Templates forms via rapido-ui/InboundsPicker.tsx.
export const useInboundsQuery = () =>
  useQuery({
    queryKey: queryKeys.inbounds,
    queryFn: () => fetch<InboundsByProtocol>("/inbounds"),
  });

// GET /api/inbounds/detail - the full-fidelity list InboundsAdmin.tsx
// renders (tag/protocol/network/security/reality fields), separate from the
// plain tag list above.
export const useInboundsDetailQuery = () =>
  useQuery({
    queryKey: queryKeys.inboundsDetail,
    queryFn: () => fetch<Inbound[]>("/inbounds/detail"),
  });

const invalidateInboundQueries = (queryClient: ReturnType<typeof useQueryClient>) => {
  // Both the plain tag-list (InboundsPicker.tsx, and this same page's own
  // "which tag to add a host under" picker on Hosts) and the detailed list
  // need to reflect a create/delete - they're two different GETs of
  // overlapping server state, not two independent caches.
  queryClient.invalidateQueries({ queryKey: queryKeys.inbounds });
  queryClient.invalidateQueries({ queryKey: queryKeys.inboundsDetail });
};

// POST /api/inbounds/sync is an upsert-by-tag that both creates a brand-new
// inbound and re-syncs an existing one's fields - InboundsAdmin.tsx's own
// create form always sends a single-element array, the same endpoint
// go-rewrite-project's earlier phases already used for bulk sync.
export const useSyncInboundMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (entry: InboundSyncEntry) =>
      fetch<{ synced: number; created: number }>("/inbounds/sync", {
        method: "POST",
        body: [entry],
      }),
    onSuccess: () => invalidateInboundQueries(queryClient),
  });
};

// Same endpoint as useSyncInboundMutation, but for the "Full inbounds
// (JSON)" card on InboundsAdmin.tsx - sends every entry the admin edited
// in one request instead of always wrapping a single one.
export const useSyncInboundsBulkMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (entries: InboundSyncEntry[]) =>
      fetch<{ synced: number; created: number }>("/inbounds/sync", {
        method: "POST",
        body: entries,
      }),
    onSuccess: () => invalidateInboundQueries(queryClient),
  });
};

// DELETE /api/inbounds/:tag cascades to that inbound's hosts server-side
// (see internal/db/queries/inbounds.sql's DeleteInboundByTag) - the hosts
// query is invalidated too so a still-open Hosts page tab doesn't keep
// showing a tag that no longer exists.
export const useDeleteInboundMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (tag: string) =>
      fetch(`/inbounds/${encodeURIComponent(tag)}`, { method: "DELETE" }),
    onSuccess: () => {
      invalidateInboundQueries(queryClient);
      queryClient.invalidateQueries({ queryKey: queryKeys.hosts });
    },
  });
};

// POST /api/inbounds/import-xray - called twice per real import: once
// with confirm:false (a pure preview - parses and reports counts/warnings,
// writes nothing) and, only if the admin reviews that and proceeds, again
// with confirm:true (actually writes). Both calls use this same mutation;
// the caller decides the body's `confirm` value. A confirmed import
// touches hosts and core-config too, not just inbounds, so all three
// caches are invalidated on success - but only when something was
// actually written (a preview response comes back with the exact same
// shape, so gate the invalidation on `applied` rather than skip it for
// preview calls via a separate code path).
export const useImportXrayConfigMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: XrayImportRequest) =>
      fetch<XrayImportResult>("/inbounds/import-xray", { method: "POST", body }),
    onSuccess: (result) => {
      if (!result.applied) return;
      invalidateInboundQueries(queryClient);
      queryClient.invalidateQueries({ queryKey: queryKeys.hosts });
      queryClient.invalidateQueries({ queryKey: queryKeys.coreConfig });
    },
  });
};
