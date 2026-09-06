// Mirrors internal/httpapi/tickets.go's ticketMessageDTO/adminTicketDTO.
// GET /api/tickets is *wrapped* ({tickets, total}), unlike the customer
// endpoint which returns a bare array - there is no customer-facing ticket
// type here since this dashboard only ever renders the admin shape.
export type TicketStatus = "open" | "closed";

export type TicketMessage = {
  id: number;
  is_admin: boolean;
  body: string;
  created_at: string;
};

export type AdminTicket = {
  id: number;
  subject: string;
  status: TicketStatus;
  created_at: string;
  updated_at: string;
  messages: TicketMessage[];
  /** the customer the ticket belongs to */
  username: string;
  /** the reseller admin who owns that customer; null for an unowned user
   * (created under the bootstrap sudo account) - sudo-only in the UI. */
  owner: string | null;
};

export type TicketsListResponse = { tickets: AdminTicket[]; total: number };
