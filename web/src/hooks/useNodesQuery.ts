import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { fetch } from "service/http";
import {
  Node,
  NodeCreateResult,
  NodeUpdatePayload,
  NodeUsageResult,
  NodeWritePayload,
} from "types/Node";
import { queryKeys } from "utils/queryClient";

export const useNodesQuery = () =>
  useQuery({
    queryKey: queryKeys.nodes,
    queryFn: () => fetch<Node[]>("/nodes"),
    // A node mid-handshake settles within seconds; poll only while one
    // actually is connecting, so a healthy fleet makes no background
    // requests at all (mirrors the old dashboard's own hasPending gate).
    refetchInterval: (query) =>
      (query.state.data ?? []).some((n) => n.status === "connecting") ? 3000 : false,
  });

const invalidateNodes = (queryClient: ReturnType<typeof useQueryClient>) =>
  queryClient.invalidateQueries({ queryKey: queryKeys.nodes });

export const useCreateNodeMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: NodeWritePayload) =>
      fetch<NodeCreateResult>("/node", { method: "POST", body }),
    onSuccess: () => invalidateNodes(queryClient),
  });
};

export const useUpdateNodeMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: number; body: NodeUpdatePayload }) =>
      fetch<Node>(`/node/${id}`, { method: "PUT", body }),
    onSuccess: () => invalidateNodes(queryClient),
  });
};

export const useDeleteNodeMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => fetch(`/node/${id}`, { method: "DELETE" }),
    onSuccess: () => invalidateNodes(queryClient),
  });
};

// start/end are RFC3339 bounds. Omitted entirely (not sent as empty strings)
// so the backend applies its own default trailing-30-days window (see
// handleGetNodesUsage's own doc comment) rather than the frontend
// re-implementing that default.
export const useNodesUsageQuery = (start?: string, end?: string) =>
  useQuery({
    queryKey: queryKeys.nodesUsage(start, end),
    queryFn: () => {
      const params = new URLSearchParams();
      if (start) params.set("start", start);
      if (end) params.set("end", end);
      const qs = params.toString();
      return fetch<NodeUsageResult>(`/nodes/usage${qs ? `?${qs}` : ""}`);
    },
  });
