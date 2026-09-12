import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { fetch } from "service/http";
import {
  Admin,
  AdminCreatePayload,
  AdminModifyPayload,
  InactiveAdminsDeleted,
  InactiveAdminsResult,
} from "types/Admin";
import { queryKeys } from "utils/queryClient";

export const useAdminsQuery = () =>
  useQuery({
    queryKey: queryKeys.admins,
    queryFn: () => fetch<Admin[]>("/admins"),
  });

const invalidateAdmins = (queryClient: ReturnType<typeof useQueryClient>) =>
  queryClient.invalidateQueries({ queryKey: queryKeys.admins });

export const useCreateAdminMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: AdminCreatePayload) =>
      fetch<Admin>("/admin", { method: "POST", body }),
    onSuccess: () => invalidateAdmins(queryClient),
  });
};

export const useUpdateAdminMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      username,
      body,
    }: {
      username: string;
      body: AdminModifyPayload;
    }) => fetch<Admin>(`/admin/${username}`, { method: "PUT", body }),
    onSuccess: () => invalidateAdmins(queryClient),
  });
};

export const useDeleteAdminMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (username: string) =>
      fetch(`/admin/${username}`, { method: "DELETE" }),
    onSuccess: () => invalidateAdmins(queryClient),
  });
};

// GET/DELETE /api/admin/inactive both take a `days` cutoff chosen at click
// time (not a stable query key an admin would want cached/refetched in the
// background), so these are mutations rather than a parameterized query -
// same pattern the old InactiveAdmins.tsx used with plain useState, just
// with the loading/error bookkeeping centralized.
export const usePreviewInactiveAdminsMutation = () =>
  useMutation({
    mutationFn: (days: number) =>
      fetch<InactiveAdminsResult>(`/admin/inactive?days=${days}`),
  });

export const useDeleteInactiveAdminsMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (days: number) =>
      fetch<InactiveAdminsDeleted>(`/admin/inactive?days=${days}`, {
        method: "DELETE",
      }),
    onSuccess: () => invalidateAdmins(queryClient),
  });
};

// The three bulk per-admin actions the old dashboard had and this one was
// missing (see AdminsAdmin.tsx's own note on this) - each is a single POST,
// no request body. `users_affected` in the response is shown to the admin
// directly rather than silently discarded, so a 0-affected click ("already
// all active" etc.) doesn't read as if nothing happened.
export const useDisableAdminUsersMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (username: string) =>
      fetch<{ detail: string; users_affected: number }>(
        `/admin/${username}/users/disable`,
        { method: "POST" }
      ),
    onSuccess: () => invalidateAdmins(queryClient),
  });
};

export const useActivateAdminUsersMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (username: string) =>
      fetch<{ detail: string; users_affected: number }>(
        `/admin/${username}/users/activate`,
        { method: "POST" }
      ),
    onSuccess: () => invalidateAdmins(queryClient),
  });
};

export const useResetAdminUsageMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (username: string) =>
      fetch<Admin>(`/admin/usage/reset/${username}`, { method: "POST" }),
    onSuccess: () => invalidateAdmins(queryClient),
  });
};
