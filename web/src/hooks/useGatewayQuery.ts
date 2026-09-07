import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { fetch } from "service/http";
import { GatewayPeer, GatewayPeerRequest, GatewaySettings, GatewayTestResult } from "types/Gateway";
import { queryKeys } from "utils/queryClient";

// GET /api/settings/gateway - lazily creates this panel's own secret on
// the backend's first-ever access (see internal/httpapi/gateway.go's
// ensureGatewaySettings), so this always returns a real value, never a
// "not set up yet" state to handle here.
export const useGatewaySettingsQuery = () =>
  useQuery({
    queryKey: queryKeys.gatewaySettings,
    queryFn: () => fetch<GatewaySettings>("/settings/gateway"),
  });

export const useUpdateGatewaySettingsMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: { name?: string; rotate_secret?: boolean }) =>
      fetch<GatewaySettings>("/settings/gateway", { method: "PUT", body }),
    onSuccess: () => queryClient.invalidateQueries({ queryKey: queryKeys.gatewaySettings }),
  });
};

export const useGatewayPeersQuery = () =>
  useQuery({
    queryKey: queryKeys.gatewayPeers,
    queryFn: () => fetch<GatewayPeer[]>("/settings/gateway/peers"),
  });

const invalidatePeers = (queryClient: ReturnType<typeof useQueryClient>) =>
  queryClient.invalidateQueries({ queryKey: queryKeys.gatewayPeers });

export const useCreateGatewayPeerMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: GatewayPeerRequest) =>
      fetch<GatewayPeer>("/settings/gateway/peers", { method: "POST", body }),
    onSuccess: () => invalidatePeers(queryClient),
  });
};

export const useUpdateGatewayPeerMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: number; body: GatewayPeerRequest }) =>
      fetch<GatewayPeer>(`/settings/gateway/peers/${id}`, { method: "PUT", body }),
    onSuccess: () => invalidatePeers(queryClient),
  });
};

export const useDeleteGatewayPeerMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => fetch(`/settings/gateway/peers/${id}`, { method: "DELETE" }),
    onSuccess: () => invalidatePeers(queryClient),
  });
};

// Not cached/invalidating anything - a live outbound ping the backend
// makes right now, not a read of stored state.
export const useTestGatewayPeerMutation = () =>
  useMutation({
    mutationFn: (id: number) =>
      fetch<GatewayTestResult>(`/settings/gateway/peers/${id}/test`, { method: "POST" }),
  });
