import { FC, useEffect, useMemo, useRef, useState } from "react";
import { useTranslation } from "react-i18next";
import classNames from "classnames";
import {
  TicketsFilters,
  useReplyTicketMutation,
  useTicketQuery,
  useTicketsQuery,
  useUpdateTicketStatusMutation,
} from "hooks/useTicketsQuery";
import { useCurrentAdminQuery } from "hooks/useCurrentAdminQuery";
import { AdminTicket, TicketMessage, TicketStatus } from "types/Ticket";
import { errorText } from "service/errors";
import { absoluteTime, isAwaitingReply, relativeTime } from "utils/ticketHelpers";
import { Card, CardSubtitle, CardTitle } from "rapido-ui/Card";
import { Badge, PulseDot } from "rapido-ui/Badge";
import { Button } from "rapido-ui/Button";
import { Select } from "rapido-ui/Select";

// Server-side cap (tickets.go's ticketBodyMaxLen) - enforced here too so an
// over-long reply is stopped before it costs a round trip. There is no cap
// on the admin side per the backend's own comment (an admin must always be
// able to answer), only on length, so this is purely a UX guard, not an
// anti-abuse one.
const BODY_MAX_LENGTH = 4000;

const PAGE_SIZES = [10, 25, 50, 100, 200];
const DEFAULT_PAGE_SIZE = 25;

type StatusFilter = "" | TicketStatus;

const FILTER_OPTIONS: { value: StatusFilter; labelKey: string }[] = [
  // "Open" first and selected by default: this page is a work queue, and a
  // closed ticket is archive. Starting on "All" would bury today's
  // unanswered tickets under every resolved one on a busy panel.
  { value: "open", labelKey: "rapido.tickets.filterOpen" },
  { value: "closed", labelKey: "rapido.tickets.filterClosed" },
  { value: "", labelKey: "rapido.tickets.filterAll" },
];

// Same windowing approach as UsersTable.tsx's own buildPageItems (first,
// last and a window around the current page, "gap" standing in for the
// collapsed runs) - duplicated rather than shared, matching that file's own
// precedent of each page carrying its own copy.
const buildPageItems = (current: number, pageCount: number): (number | "gap")[] => {
  if (pageCount <= 7) {
    return Array.from({ length: pageCount }, (_, i) => i + 1);
  }
  const pages = new Set<number>([1, pageCount, current]);
  if (current - 1 > 1) pages.add(current - 1);
  if (current + 1 < pageCount) pages.add(current + 1);
  if (current <= 3) [2, 3, 4].forEach((p) => p < pageCount && pages.add(p));
  if (current >= pageCount - 2)
    [pageCount - 1, pageCount - 2, pageCount - 3].forEach((p) => p > 1 && pages.add(p));

  const sorted = Array.from(pages).sort((a, b) => a - b);
  const items: (number | "gap")[] = [];
  sorted.forEach((page, index) => {
    if (index > 0 && page - sorted[index - 1] > 1) items.push("gap");
    items.push(page);
  });
  return items;
};

const statusLabelKey = (status: TicketStatus): string =>
  status === "open" ? "rapido.tickets.statusOpen" : "rapido.tickets.statusClosed";

// Input.tsx has no multiline variant yet, so this mirrors its exact classes
// (same reason the old dashboard hand-copied an identical `inputClass` for
// its own reply box).
const textareaClass =
  "w-full resize-y rounded-lg border border-rapido-border bg-rapido-bg px-3 py-2 text-sm text-rapido-text placeholder:text-rapido-muted focus:outline-none focus:ring-1 focus:ring-rapido-accent disabled:cursor-not-allowed disabled:opacity-50";

// ---------------------------------------------------------------------------
// Row
// ---------------------------------------------------------------------------

const TicketRow: FC<{
  ticket: AdminTicket;
  selected: boolean;
  showOwner: boolean;
  onSelect: (ticket: AdminTicket) => void;
}> = ({ ticket, selected, showOwner, onSelect }) => {
  const { t, i18n } = useTranslation();
  const awaiting = isAwaitingReply(ticket);

  return (
    <Card
      role="button"
      tabIndex={0}
      aria-current={selected ? "true" : undefined}
      onClick={() => onSelect(ticket)}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          onSelect(ticket);
        }
      }}
      className={classNames(
        "cursor-pointer p-4 transition-colors hover:border-rapido-accent/60",
        // Tinted edge so a ticket waiting on an answer is obvious while
        // scrolling, without having to read each row's badges.
        awaiting && "!border-orange-500/60 bg-orange-500/[0.04]",
        selected && "!border-rapido-accent bg-rapido-accent/[0.06]"
      )}
    >
      <div className="flex flex-col gap-2">
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div className="flex min-w-0 items-center gap-2">
            {awaiting && <PulseDot tone="orange" />}
            {/* dir="auto": subjects are written by customers, so their
                direction follows the text itself, not the admin's chosen
                interface language. */}
            <CardTitle dir="auto" className="truncate text-base">
              {ticket.subject}
            </CardTitle>
          </div>
          <div className="flex flex-wrap items-center gap-1.5">
            {awaiting && <Badge tone="orange">{t("rapido.tickets.awaitingReply")}</Badge>}
            <Badge tone={ticket.status === "open" ? "green" : "gray"}>
              {t(statusLabelKey(ticket.status))}
            </Badge>
          </div>
        </div>

        <div className="flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 text-xs text-rapido-muted">
          <span dir="ltr" className="min-w-0 break-all text-rapido-text">
            {ticket.username}
          </span>
          {/* Only a sudo admin sees more than one reseller's tickets, so for
              a reseller this column would be a constant and pure noise. */}
          {showOwner && (
            <span>
              {t("rapido.tickets.ownerLabel", {
                value: ticket.owner || t("rapido.tickets.noOwner"),
              })}
            </span>
          )}
          {/* "#" is a bidi-neutral character: adjacent to digits in an RTL
              run it resolves to the paragraph direction and gets drawn
              *after* the number ("42#" instead of "#42"). */}
          <span dir="ltr">#{ticket.id}</span>
        </div>

        <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 text-xs text-rapido-muted">
          <span className="tabular-nums" title={absoluteTime(i18n.language, ticket.updated_at)}>
            {t("rapido.tickets.updatedAgo", {
              time: relativeTime(i18n.language, ticket.updated_at),
            })}
          </span>
          <span className="tabular-nums">
            {t("rapido.tickets.messageCount", { value: ticket.messages.length })}
          </span>
        </div>
      </div>
    </Card>
  );
};

// ---------------------------------------------------------------------------
// Thread
// ---------------------------------------------------------------------------

const MessageBubble: FC<{ message: TicketMessage; customer: string }> = ({
  message,
  customer,
}) => {
  const { t, i18n } = useTranslation();
  const stamp = absoluteTime(i18n.language, message.created_at);

  return (
    <div
      className={classNames(
        "flex max-w-[85%] flex-col gap-1 rounded-xl2 border px-3 py-2",
        // In a column flex container align-self resolves along the inline
        // axis, so "the admin's side" follows dir and stays correct in RTL.
        message.is_admin
          ? "self-end border-rapido-accent/40 bg-rapido-accent/10"
          : "self-start border-rapido-border bg-rapido-bg"
      )}
    >
      <div className="flex min-w-0 flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5">
        <span
          dir="auto"
          className={classNames(
            "min-w-0 break-all text-xs font-semibold",
            message.is_admin ? "text-rapido-accent" : "text-rapido-text"
          )}
        >
          {message.is_admin ? t("rapido.tickets.adminAuthor") : customer}
        </span>
        {/* dir="auto", not "ltr": a Persian-locale timestamp is itself RTL
            while the English one is LTR - the first strong character should
            decide, it only needs isolating from the author name beside it. */}
        <span dir="auto" className="shrink-0 text-[11px] text-rapido-muted">
          {stamp}
        </span>
      </div>
      <div dir="auto" className="whitespace-pre-wrap break-words text-sm text-rapido-text">
        {message.body}
      </div>
    </div>
  );
};

const TicketThread: FC<{
  ticket: AdminTicket;
  showOwner: boolean;
  onBack: () => void;
}> = ({ ticket, showOwner, onBack }) => {
  const { t, i18n } = useTranslation();
  const [body, setBody] = useState("");
  const messagesRef = useRef<HTMLDivElement | null>(null);
  const closed = ticket.status === "closed";

  const replyTicket = useReplyTicketMutation();
  const updateStatus = useUpdateTicketStatusMutation();

  // Land on the newest message whenever this thread grows - an admin opening
  // a long ticket wants the latest question, not the first one. The parent
  // mounts a fresh TicketThread (via a `key={ticket.id}`) for every ticket
  // selected, so this only ever tracks one ticket's own messages.
  const messageCount = ticket.messages.length;
  useEffect(() => {
    const node = messagesRef.current;
    if (node) node.scrollTop = node.scrollHeight;
  }, [messageCount]);

  const trimmed = body.trim();

  const submit = () => {
    if (!trimmed || replyTicket.isPending || closed) return;
    // Only clear the box once the reply is actually accepted, so a failed
    // send does not throw away what was typed.
    replyTicket.mutate({ id: ticket.id, body: trimmed }, { onSuccess: () => setBody("") });
  };

  return (
    <Card className="flex flex-col gap-4 p-4">
      <div className="flex flex-col gap-2">
        <div className="flex flex-wrap items-start justify-between gap-2">
          <div className="flex min-w-0 flex-col gap-1">
            <CardTitle dir="auto" className="break-words text-base">
              {ticket.subject}
            </CardTitle>
            <CardSubtitle className="break-all">
              {t("rapido.tickets.customerLabel", { value: ticket.username })}
              {showOwner &&
                ` · ${t("rapido.tickets.ownerLabel", {
                  value: ticket.owner || t("rapido.tickets.noOwner"),
                })}`}
            </CardSubtitle>
          </div>
          <div className="flex flex-wrap items-center gap-1.5">
            {isAwaitingReply(ticket) && (
              <Badge tone="orange">{t("rapido.tickets.awaitingReply")}</Badge>
            )}
            <Badge tone={closed ? "gray" : "green"}>{t(statusLabelKey(ticket.status))}</Badge>
          </div>
        </div>

        <div className="flex flex-wrap items-center justify-between gap-2 text-xs text-rapido-muted">
          <span className="tabular-nums" title={absoluteTime(i18n.language, ticket.created_at)}>
            {t("rapido.tickets.openedAgo", {
              time: relativeTime(i18n.language, ticket.created_at),
            })}
          </span>
          <div className="flex items-center gap-1.5">
            <Button variant="chip" className="lg:hidden" onClick={onBack}>
              {t("rapido.tickets.backToList")}
            </Button>
            <Button
              variant="chip"
              tone={closed ? "accent" : "amber"}
              disabled={updateStatus.isPending}
              onClick={() =>
                updateStatus.mutate({ id: ticket.id, status: closed ? "open" : "closed" })
              }
            >
              {updateStatus.isPending
                ? t("rapido.pleaseWait")
                : closed
                ? t("rapido.tickets.reopen")
                : t("rapido.tickets.closeTicket")}
            </Button>
          </div>
        </div>

        {updateStatus.isError && (
          <div className="text-xs text-red-400" role="alert">
            {errorText(updateStatus.error, t("rapido.tickets.statusFailed"))}
          </div>
        )}
      </div>

      <div
        ref={messagesRef}
        className="flex max-h-[50vh] flex-col gap-3 overflow-y-auto border-y border-rapido-border py-3 lg:max-h-[calc(100vh-24rem)]"
      >
        {ticket.messages.map((message) => (
          <MessageBubble key={message.id} message={message} customer={ticket.username} />
        ))}
      </div>

      <div className="flex flex-col gap-2">
        {closed && (
          <div className="rounded-lg border border-rapido-border bg-rapido-bg px-3 py-2 text-xs text-rapido-muted">
            {t("rapido.tickets.closedNotice")}
          </div>
        )}
        <textarea
          value={body}
          rows={4}
          maxLength={BODY_MAX_LENGTH}
          disabled={closed || replyTicket.isPending}
          placeholder={t("rapido.tickets.replyPlaceholder")}
          aria-label={t("rapido.tickets.replyPlaceholder")}
          // An admin running the panel in English still answers Persian
          // customers in Persian; dir="auto" flips the composer as soon as
          // the first Persian character is typed instead of staying
          // left-aligned.
          dir="auto"
          className={textareaClass}
          onChange={(e) => setBody(e.target.value)}
        />
        {replyTicket.isError && (
          <div className="text-xs text-red-400" role="alert">
            {errorText(replyTicket.error, t("rapido.tickets.sendFailed"))}
          </div>
        )}
        <div className="flex flex-wrap items-center justify-between gap-2">
          <span className="text-xs tabular-nums text-rapido-muted">
            {t("rapido.tickets.charactersLeft", { value: BODY_MAX_LENGTH - body.length })}
          </span>
          <Button
            variant="primary"
            disabled={closed || replyTicket.isPending || !trimmed}
            onClick={submit}
          >
            {replyTicket.isPending ? t("rapido.pleaseWait") : t("rapido.tickets.send")}
          </Button>
        </div>
      </div>
    </Card>
  );
};

// ---------------------------------------------------------------------------
// Page body
// ---------------------------------------------------------------------------

export const TicketsAdmin: FC = () => {
  const { t } = useTranslation();
  const {
    data: currentAdmin,
    isPending: adminPending,
    isSuccess: adminSuccess,
  } = useCurrentAdminQuery();
  // Fail closed, exactly as Shell.tsx does: while /admin is still in flight
  // the admin is treated as non-sudo, so the reseller column never flashes
  // in for someone who should not be reading it.
  const isSudo = !adminPending && adminSuccess && !!currentAdmin?.is_sudo;

  const [statusFilter, setStatusFilter] = useState<StatusFilter>("open");
  const [limit, setLimit] = useState(DEFAULT_PAGE_SIZE);
  const [offset, setOffset] = useState(0);
  const [selectedId, setSelectedId] = useState<number | null>(null);

  const filters: TicketsFilters = { status: statusFilter, limit, offset };
  const { data, isLoading, isError, error, isFetching, refetch } = useTicketsQuery(filters);
  const tickets = data?.tickets ?? [];
  const total = data?.total ?? 0;

  // Closing the last ticket of a page (or switching to a sparser filter) can
  // leave the offset past the end of the result set - an empty page with
  // rows available on page 1 looks like a bug.
  useEffect(() => {
    if (offset > 0 && offset >= total) setOffset(0);
  }, [offset, total]);

  // GET /api/tickets/:id is not limited to the current filter/page, so it is
  // what keeps a thread alive (and current) even after the ticket it shows
  // falls out of the visible list - e.g. closing a ticket while viewing the
  // "open" filter.
  const listRow = tickets.find((row) => row.id === selectedId) ?? null;
  const {
    data: ticketDetail,
    isError: detailIsError,
    error: detailError,
  } = useTicketQuery(selectedId);
  // The list response already carries every message per ticket, so the
  // thread renders instantly from whichever row is on screen; the dedicated
  // by-id query then takes over as the freshest source once it resolves.
  const thread = ticketDetail ?? listRow;

  const rangeStart = total === 0 ? 0 : offset + 1;
  const rangeEnd = Math.min(offset + limit, total);
  const pageCount = Math.max(1, Math.ceil(total / limit));
  const currentPage = Math.floor(offset / limit) + 1;
  const pageItems = useMemo(() => buildPageItems(currentPage, pageCount), [currentPage, pageCount]);
  const awaitingCount = useMemo(() => tickets.filter(isAwaitingReply).length, [tickets]);

  const listErrorMessage = isError ? errorText(error, t("rapido.tickets.loadFailed")) : null;

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-3">
        <div
          role="group"
          aria-label={t("rapido.statusLabel")}
          className="flex items-center gap-1 rounded-lg border border-rapido-border p-1"
        >
          {FILTER_OPTIONS.map((option) => (
            <Button
              key={option.value || "all"}
              variant="chip"
              tone={statusFilter === option.value ? "accent" : "neutral"}
              aria-pressed={statusFilter === option.value}
              onClick={() => {
                setStatusFilter(option.value);
                setOffset(0);
                setSelectedId(null);
              }}
            >
              {t(option.labelKey)}
            </Button>
          ))}
        </div>

        {awaitingCount > 0 && (
          <Badge tone="orange" className="tabular-nums">
            {t("rapido.tickets.awaitingCount", { value: awaitingCount })}
          </Badge>
        )}

        <Button variant="chip" className="ms-auto" disabled={isFetching} onClick={() => refetch()}>
          {t("rapido.tickets.refresh")}
        </Button>
      </div>

      {/* grid-cols-1 is load-bearing, not decoration: without an explicit
          track the single column below `lg` is implicit and auto-sized, so
          it grows to the widest thing inside it - a customer pasting their
          subscription URL would make the thread pane wider than a phone
          screen. Tailwind's grid-cols-1 is minmax(0,1fr), which caps the
          track at the container and lets break-words actually break it. */}
      <div className="grid grid-cols-1 gap-4 lg:grid-cols-2 xl:grid-cols-[minmax(0,26rem)_minmax(0,1fr)]">
        {/* On a phone the two panes cannot share the screen, so the list
            steps aside while a thread is open (the thread carries a Back
            button). */}
        <div
          className={classNames(
            "flex min-w-0 flex-col gap-3",
            selectedId != null && "hidden lg:flex"
          )}
        >
          {isLoading && (
            <div className="py-12 text-center text-sm text-rapido-muted">
              {t("rapido.tickets.loading")}
            </div>
          )}

          {!isLoading && listErrorMessage && (
            <Card className="border-red-500/40 bg-red-500/[0.03] p-4" role="alert">
              <CardTitle className="mb-1 text-red-400">{t("rapido.tickets.loadFailed")}</CardTitle>
              <CardSubtitle>{listErrorMessage}</CardSubtitle>
              <Button variant="chip" className="mt-3" onClick={() => refetch()}>
                {t("rapido.tickets.refresh")}
              </Button>
            </Card>
          )}

          {!isLoading && !listErrorMessage && tickets.length === 0 && (
            <div className="py-12 text-center text-sm text-rapido-muted">
              {statusFilter ? t("rapido.tickets.emptyFiltered") : t("rapido.tickets.empty")}
            </div>
          )}

          {!isLoading &&
            !listErrorMessage &&
            tickets.map((ticket) => (
              <TicketRow
                key={ticket.id}
                ticket={ticket}
                selected={ticket.id === selectedId}
                showOwner={isSudo}
                onSelect={(row) => setSelectedId(row.id)}
              />
            ))}

          <div className="flex flex-wrap items-center justify-between gap-3 text-xs text-rapido-muted">
            <div className="flex items-center gap-3">
              <span className="tabular-nums">
                {t("rapido.range", { start: rangeStart, end: rangeEnd, total })}
              </span>
              <label className="flex items-center gap-1.5">
                {t("itemsPerPage")}
                <Select
                  value={limit}
                  className="px-2 py-1 text-xs"
                  onChange={(e) => {
                    setLimit(Number(e.target.value));
                    setOffset(0);
                  }}
                >
                  {PAGE_SIZES.map((size) => (
                    <option key={size} value={size}>
                      {size}
                    </option>
                  ))}
                </Select>
              </label>
            </div>

            <div className="flex flex-wrap items-center gap-1.5">
              <Button
                variant="chip"
                disabled={currentPage === 1}
                onClick={() => setOffset(Math.max(offset - limit, 0))}
              >
                {t("previous")}
              </Button>

              {pageItems.map((item, index) =>
                item === "gap" ? (
                  <span key={`gap-${index}`} className="px-1">
                    …
                  </span>
                ) : (
                  <Button
                    key={item}
                    variant="chip"
                    tone={item === currentPage ? "accent" : "neutral"}
                    aria-current={item === currentPage ? "page" : undefined}
                    className="min-w-[2rem]"
                    onClick={() => setOffset((item - 1) * limit)}
                  >
                    {item}
                  </Button>
                )
              )}

              <Button
                variant="chip"
                disabled={currentPage >= pageCount}
                onClick={() => setOffset(offset + limit)}
              >
                {t("next")}
              </Button>
            </div>
          </div>
        </div>

        <div
          className={classNames(
            "min-w-0 lg:sticky lg:top-4 lg:self-start",
            selectedId == null && "hidden lg:block"
          )}
        >
          {thread ? (
            <div className="flex flex-col gap-2">
              {/* The thread is seeded from the list row, which already
                  carries every message, so a failed refresh is a warning
                  over still-usable content rather than a blank pane. */}
              {detailIsError && (
                <div
                  role="alert"
                  className="rounded-lg border border-red-500/40 bg-red-500/[0.03] px-3 py-2 text-xs text-red-400"
                >
                  {errorText(detailError, t("rapido.tickets.threadLoadFailed"))}
                </div>
              )}
              <TicketThread
                key={thread.id}
                ticket={thread}
                showOwner={isSudo}
                onBack={() => setSelectedId(null)}
              />
            </div>
          ) : selectedId != null && detailIsError ? (
            <Card className="border-red-500/40 bg-red-500/[0.03] p-4" role="alert">
              <CardTitle className="mb-1 text-red-400">
                {t("rapido.tickets.threadLoadFailed")}
              </CardTitle>
              <CardSubtitle>{errorText(detailError, t("rapido.tickets.threadLoadFailed"))}</CardSubtitle>
              <Button variant="chip" className="mt-3" onClick={() => setSelectedId(null)}>
                {t("rapido.close")}
              </Button>
            </Card>
          ) : (
            <Card className="p-8 text-center text-sm text-rapido-muted">
              {t("rapido.tickets.selectPrompt")}
            </Card>
          )}
        </div>
      </div>
    </div>
  );
};

export default TicketsAdmin;
