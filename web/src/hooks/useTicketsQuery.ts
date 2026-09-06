import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { fetch } from "service/http";
import { AdminTicket, TicketsListResponse, TicketStatus } from "types/Ticket";
import { queryKeys } from "utils/queryClient";

export type TicketsFilters = {
  status?: TicketStatus | "";
  limit?: number;
  offset?: number;
};

// Same "drop falsy values" convention as useUsersQuery.ts's cleanFilters -
// status="" (the "All" filter) must not become `?status=` on the wire, since
// the Go handler rejects any non-empty status other than open/closed.
const cleanFilters = (filters: TicketsFilters): Record<string, unknown> => {
  const out: Record<string, unknown> = {};
  if (filters.status) out.status = filters.status;
  if (filters.limit) out.limit = filters.limit;
  if (filters.offset) out.offset = filters.offset;
  return out;
};

// Matches the old dashboard's hand-rolled poll cadence for this page (see
// app/dashboard's TicketsAdmin.tsx POLL_INTERVAL_MS) - a support inbox is a
// work queue other admins and customers write to outside this tab, so it is
// worth refreshing on a timer. TanStack Query's refetchInterval covers that
// need directly; the old implementation's stamp-ref bookkeeping existed only
// to guard against races that a per-query-key cache already rules out here.
const POLL_INTERVAL_MS = 30_000;

export const useTicketsQuery = (filters: TicketsFilters) =>
  useQuery({
    queryKey: queryKeys.tickets(cleanFilters(filters)),
    queryFn: () =>
      fetch<TicketsListResponse>("/tickets", { query: cleanFilters(filters) }),
    // Keeps the previous page's rows on screen while a new filter/page loads,
    // instead of flashing a loading state - same as useUsersQuery.ts.
    placeholderData: (previous) => previous,
    refetchInterval: POLL_INTERVAL_MS,
  });

// GET /api/tickets/:id is scoped to the caller but not to the list's current
// status filter/page, so this is what keeps an open thread alive and fresh
// even after the ticket is closed and falls out of an "open" filtered list.
export const useTicketQuery = (id: number | null) =>
  useQuery({
    queryKey: queryKeys.ticket(id),
    queryFn: () => fetch<AdminTicket>(`/tickets/${id}`),
    enabled: id != null,
    refetchInterval: POLL_INTERVAL_MS,
  });

const invalidateTickets = (
  queryClient: ReturnType<typeof useQueryClient>,
  id: number
) => {
  // Prefix-matches every filter/page variant queryKeys.tickets(...) has
  // produced, same as useUsersQuery.ts's invalidateUsers("users").
  queryClient.invalidateQueries({ queryKey: ["tickets"] });
  queryClient.invalidateQueries({ queryKey: queryKeys.ticket(id) });
};

export const useReplyTicketMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, body }: { id: number; body: string }) =>
      fetch<AdminTicket>(`/tickets/${id}/messages`, {
        method: "POST",
        body: { body },
      }),
    onSuccess: (_data, variables) => invalidateTickets(queryClient, variables.id),
  });
};

export const useUpdateTicketStatusMutation = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: ({ id, status }: { id: number; status: TicketStatus }) =>
      fetch<AdminTicket>(`/tickets/${id}`, { method: "PUT", body: { status } }),
    onSuccess: (_data, variables) => invalidateTickets(queryClient, variables.id),
  });
};
