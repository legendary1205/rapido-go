import { AdminTicket, TicketMessage } from "types/Ticket";

// ---------------------------------------------------------------------------
// Derived state over a ticket
// ---------------------------------------------------------------------------

export const lastMessageOf = (ticket: AdminTicket): TicketMessage | null =>
  ticket.messages.length > 0 ? ticket.messages[ticket.messages.length - 1] : null;

/**
 * The whole point of this page: an open ticket whose last message came from
 * the customer is sitting there waiting on an admin. Ported from the old
 * dashboard's TicketsAdmin.tsx (same name, same rule).
 */
export const isAwaitingReply = (ticket: AdminTicket): boolean => {
  const last = lastMessageOf(ticket);
  return ticket.status === "open" && !!last && !last.is_admin;
};

// ---------------------------------------------------------------------------
// Dates
// ---------------------------------------------------------------------------

// The API serialises naive UTC datetimes with no offset suffix. new Date()
// reads those as *local* time, which would silently shift every timestamp by
// the admin's UTC offset, so the missing "Z" is put back before parsing.
const HAS_TIMEZONE = /(?:Z|z|[+-]\d{2}:?\d{2})$/;

export const parseApiDate = (value: string): Date =>
  new Date(HAS_TIMEZONE.test(value) ? value : `${value}Z`);

const RELATIVE_UNITS: [Intl.RelativeTimeFormatUnit, number][] = [
  ["year", 365 * 24 * 3600],
  ["month", 30 * 24 * 3600],
  ["day", 24 * 3600],
  ["hour", 3600],
  ["minute", 60],
  ["second", 1],
];

// Intl gives a properly localised "2 days ago" / "۲ روز پیش" / "2 дня назад"
// for free, so relative times need no plural keys of their own in four
// languages. Formatters are cached because building one is not cheap and a
// list of 200 rows would otherwise build 200 of them per render.
const relativeFormatters = new Map<string, Intl.RelativeTimeFormat>();

const relativeFormatter = (locale: string): Intl.RelativeTimeFormat => {
  const cached = relativeFormatters.get(locale);
  if (cached) return cached;
  let formatter: Intl.RelativeTimeFormat;
  try {
    formatter = new Intl.RelativeTimeFormat(locale, { numeric: "auto" });
  } catch {
    // An unexpected/invalid tag from the language detector must not blank the
    // page - fall back to the runtime default locale.
    formatter = new Intl.RelativeTimeFormat(undefined, { numeric: "auto" });
  }
  relativeFormatters.set(locale, formatter);
  return formatter;
};

export const relativeTime = (locale: string, value: string): string => {
  const date = parseApiDate(value);
  const time = date.getTime();
  if (Number.isNaN(time)) return "";
  const seconds = Math.round((time - Date.now()) / 1000);
  const magnitude = Math.abs(seconds);
  const unit =
    RELATIVE_UNITS.find(([, size]) => magnitude >= size) ??
    RELATIVE_UNITS[RELATIVE_UNITS.length - 1];
  return relativeFormatter(locale).format(Math.round(seconds / unit[1]), unit[0]);
};

export const absoluteTime = (locale: string, value: string): string => {
  const date = parseApiDate(value);
  if (Number.isNaN(date.getTime())) return "";
  try {
    return date.toLocaleString(locale, { dateStyle: "medium", timeStyle: "short" });
  } catch {
    return date.toLocaleString();
  }
};
