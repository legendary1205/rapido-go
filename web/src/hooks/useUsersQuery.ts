import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { fetch } from "service/http";
import {
  Status,
  User,
  UserCreatePayload,
  UsersListResponse,
  UserWritePayload,
} from "types/User";
import { queryKeys } from "utils/queryClient";

export type UsersFilters = {
  search?: string;
  status?: Status;
  sort?: string;
  limit?: number;
  offset?: number;
};

// Drops falsy values so e.g. an empty search string doesn't become
// `?search=` on the wire - mirrors the old dashboard's DashboardContext
// fetchUsers, which deleted any falsy key from the query object before
// sending it.
const cleanFilters = (filters: UsersFilters): Record<string, unknown> => {
  const out: Record<string, unknown> = {};
  (Object.keys(filters) as (keyof UsersFilters)[]).forEach((key) => {
    const value = filters[key];
    if (value) out[key] = value;
  });
  return out;
};

// GET /api/users accepts search/status/offset/limit - NOT `sort`. This is a
// real gap in the current Go backend (users.sql's ListUsers query has a
// fixed `ORDER BY id`, and handleListUsers in internal/httpapi/user.go never
// reads a sort query param at all), not something the frontend can safely
// paper over: the list is server-paginated, so re-sorting one page
// client-side would misrepresent what the other pages contain. `sort` is
// still sent (an unrecognized query param is simply ignored server-side) so
// the UsersTable dropdown keeps working the moment sorting is implemented,
// but until then it has no visible effect - flagged here rather than quietly
// dropped or quietly faked.
export const useUsersQuery = (filters: UsersFilters) =>
  useQuery({
    queryKey: queryKeys.users(cleanFilters(filters)),
    queryFn: () =>
      fetch<UsersListResponse>("/users", { query: cleanFilters(filters) }),
    placeholderData: (previous) => previous,
  });

const invalidateUsers = (queryClient: ReturnType<typeof useQueryClient>) => {
  queryClient.invalidateQueries({ queryKey: ["users"] });
  queryClient.invalidateQueries({ queryKey: queryKeys.systemStats });
};

export const useCreateUserMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (body: UserCreatePayload) =>
      fetch<User>("/user", { method: "POST", body }),
    onSuccess: () => invalidateUsers(queryClient),
  });
};

export const useUpdateUserMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({
      username,
      body,
    }: {
      username: string;
      body: UserWritePayload;
    }) => fetch<User>(`/user/${username}`, { method: "PUT", body }),
    onSuccess: () => invalidateUsers(queryClient),
  });
};

export const useDeleteUserMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (username: string) =>
      fetch(`/user/${username}`, { method: "DELETE" }),
    onSuccess: () => invalidateUsers(queryClient),
  });
};

export const useResetUserUsageMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (username: string) =>
      fetch<User>(`/user/${username}/reset`, { method: "POST" }),
    onSuccess: () => invalidateUsers(queryClient),
  });
};

export const useRevokeUserSubMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (username: string) =>
      fetch<User>(`/user/${username}/revoke_sub`, { method: "POST" }),
    onSuccess: () => invalidateUsers(queryClient),
  });
};
