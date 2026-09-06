import { FC, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import debounce from "lodash.debounce";
import classNames from "classnames";
import { useUsersQuery, UsersFilters } from "hooks/useUsersQuery";
import { useUsersUiStore } from "rapido-ui/usersUiStore";
import { User, Status } from "types/User";
import { formatBytes } from "utils/formatByte";
import { relativeExpiryDate } from "utils/dateFormatter";
import { setUsersPerPageLimitSize } from "utils/userPreferenceStorage";
import { Card, CardTitle, CardSubtitle } from "rapido-ui/Card";
import { Badge } from "rapido-ui/Badge";
import { Button } from "rapido-ui/Button";
import { Input } from "rapido-ui/Input";
import { Select } from "rapido-ui/Select";
import { ltrIsolate } from "rapido-ui/bidi";

import type { BadgeTone } from "rapido-ui/Badge";

// limited (out of data) and expired both read as red: from the admin's point
// of view they are the same situation - the customer cannot connect and needs
// to renew - so they should stand out identically when scanning the list.
const statusTone: Record<Status, BadgeTone> = {
  active: "green",
  on_hold: "yellow",
  limited: "red",
  expired: "red",
  disabled: "gray",
};

const statusFilterOptions: { value: Status | ""; labelKey: string }[] = [
  { value: "", labelKey: "rapido.allStatuses" },
  { value: "active", labelKey: "status.active" },
  { value: "on_hold", labelKey: "status.on_hold" },
  { value: "limited", labelKey: "status.limited" },
  { value: "expired", labelKey: "status.expired" },
  { value: "disabled", labelKey: "status.disabled" },
];

// `sort` is sent to GET /api/users, but the Go backend does not implement it
// yet (see hooks/useUsersQuery.ts's own comment - ListUsers.sql has a fixed
// `ORDER BY id`). Left in the UI so the control keeps working the moment
// backend sorting exists, rather than removing a feature the plan didn't ask
// to drop; a human should decide whether to hide this dropdown or implement
// the backend sort in the meantime (see the final report).
const sortOptions: { value: string; labelKey: string }[] = [
  { value: "-created_at", labelKey: "rapido.sortNewest" },
  { value: "created_at", labelKey: "rapido.sortOldest" },
  { value: "username", labelKey: "rapido.sortUsernameAz" },
  { value: "-used_traffic", labelKey: "rapido.sortMostUsed" },
  { value: "expire", labelKey: "rapido.sortSoonestExpiry" },
];

type LastSeen =
  | { kind: "online" }
  | { kind: "never" }
  | { kind: "seen"; time: string };

// online_at is the clearest "since when" signal User exposes for this.
const lastSeenOf = (onlineAt: string | null): LastSeen => {
  if (!onlineAt) return { kind: "never" };
  const unix = Math.floor(new Date(onlineAt).getTime() / 1000);
  if (Number.isNaN(unix)) return { kind: "never" };
  const diffSeconds = Math.floor(Date.now() / 1000) - unix;
  if (diffSeconds <= 180) return { kind: "online" };
  const { time } = relativeExpiryDate(unix);
  return time ? { kind: "seen", time } : { kind: "online" };
};

// Card edge + faint tint per state. Written as whole class strings because
// Tailwind only generates classes it can find literally in the source.
//
// The `!` matters: Card already sets `border-rapido-border`, and when two
// utilities set the same property the winner is decided by their order in the
// generated stylesheet, not by the order they appear in the class attribute.
// Without it the grey default won and every card looked identical - which is
// exactly what the colouring was supposed to fix.
type CardTone = BadgeTone;

const cardToneClasses: Record<CardTone, string> = {
  green: "!border-emerald-500/60 bg-emerald-500/[0.04]",
  sky: "!border-sky-500/60 bg-sky-500/[0.04]",
  yellow: "!border-yellow-500/60 bg-yellow-500/[0.04]",
  orange: "!border-orange-500/60 bg-orange-500/[0.04]",
  red: "!border-red-500/60 bg-red-500/[0.04]",
  brand: "!border-rapido-accent/60 bg-rapido-accent/[0.04]",
  gray: "",
};

// Green pulsing dot while the customer is connected, steady blue once they
// are only "last seen", grey if they never connected - so presence is
// readable at a glance without reading the text.
const PresenceDot: FC<{ kind: LastSeen["kind"] }> = ({ kind }) => {
  if (kind === "online") {
    return (
      <span className="relative flex h-2 w-2 shrink-0">
        <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-emerald-400 opacity-75" />
        <span className="relative inline-flex h-2 w-2 rounded-full bg-emerald-400" />
      </span>
    );
  }
  return (
    <span
      className={classNames(
        "h-2 w-2 shrink-0 rounded-full",
        kind === "seen" ? "bg-sky-400" : "bg-rapido-muted/50"
      )}
    />
  );
};

const PAGE_SIZES = [10, 25, 50, 100, 200];

// First page, last page, and a window around the current one, with "gap"
// markers standing in for the runs that are collapsed - 7700 users at 10 per
// page would otherwise mean 770 buttons.
const buildPageItems = (
  current: number,
  pageCount: number
): (number | "gap")[] => {
  if (pageCount <= 7) {
    return Array.from({ length: pageCount }, (_, i) => i + 1);
  }
  const pages = new Set<number>([1, pageCount, current]);
  if (current - 1 > 1) pages.add(current - 1);
  if (current + 1 < pageCount) pages.add(current + 1);
  if (current <= 3) [2, 3, 4].forEach((p) => p < pageCount && pages.add(p));
  if (current >= pageCount - 2)
    [pageCount - 1, pageCount - 2, pageCount - 3].forEach(
      (p) => p > 1 && pages.add(p)
    );

  const sorted = Array.from(pages).sort((a, b) => a - b);
  const items: (number | "gap")[] = [];
  sorted.forEach((page, index) => {
    if (index > 0 && page - sorted[index - 1] > 1) items.push("gap");
    items.push(page);
  });
  return items;
};

const UserActions: FC<{ user: User }> = ({ user }) => {
  const { t } = useTranslation();
  const setEditingUser = useUsersUiStore((s) => s.setEditingUser);
  const setDeletingUser = useUsersUiStore((s) => s.setDeletingUser);
  const setResetUsageUser = useUsersUiStore((s) => s.setResetUsageUser);
  const setRevokeSubscriptionUser = useUsersUiStore((s) => s.setRevokeSubscriptionUser);
  const setSubscriptionLinkUser = useUsersUiStore((s) => s.setSubscriptionLinkUser);

  return (
    <div className="flex flex-wrap items-center gap-1.5">
      <Button variant="chip" tone="accent" onClick={() => setEditingUser(user)}>
        {t("rapido.edit")}
      </Button>
      <Button variant="chip" tone="amber" onClick={() => setResetUsageUser(user)}>
        {t("userDialog.resetUsage")}
      </Button>
      <Button variant="chip" tone="amber" onClick={() => setRevokeSubscriptionUser(user)}>
        {t("userDialog.revokeSubscription")}
      </Button>
      <Button variant="chip" tone="sky" onClick={() => setSubscriptionLinkUser(user)}>
        {t("rapido.subscriptionLink")}
      </Button>
      <Button variant="chip" tone="red" onClick={() => setDeletingUser(user)}>
        {t("delete")}
      </Button>
    </div>
  );
};

const UserRow: FC<{ user: User }> = ({ user }) => {
  const { t } = useTranslation();
  const protocols = Object.keys(user.proxies);
  const hasLimit = !!user.data_limit;
  const usedPercent = hasLimit
    ? Math.min((user.used_traffic / (user.data_limit as number)) * 100, 100)
    : 0;
  const expiryInfo = relativeExpiryDate(user.expire);
  const lastSeen = lastSeenOf(user.online_at);

  // A connected customer is shown green regardless of status, since presence is
  // the more immediate signal; otherwise the card carries its status colour, so
  // the edge always matches the badge and the list can be read by colour alone.
  const statusToneOf = statusTone[user.status] ?? "gray";
  const cardTone: CardTone =
    lastSeen.kind === "online"
      ? "green"
      : statusToneOf === "red"
      ? "red"
      : lastSeen.kind === "never"
      ? "brand"
      : lastSeen.kind === "seen"
      ? "sky"
      : statusToneOf;

  return (
    <Card className={classNames("p-4", cardToneClasses[cardTone])}>
      <div className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            <CardTitle className="min-w-0 break-all text-base" dir="ltr">
              {user.username}
            </CardTitle>
            <span className="flex items-center gap-1.5">
              <PresenceDot kind={lastSeen.kind} />
              <CardSubtitle
                className={classNames(
                  lastSeen.kind === "online" && "text-emerald-400"
                )}
              >
                {lastSeen.kind === "online" && t("rapido.onlineNowLabel")}
                {lastSeen.kind === "never" && t("rapido.neverConnected")}
                {lastSeen.kind === "seen" &&
                  t("rapido.lastSeen", { time: ltrIsolate(lastSeen.time) })}
              </CardSubtitle>
            </span>
          </div>
          <div className="flex flex-wrap items-center gap-1.5">
            <Badge tone={statusTone[user.status] ?? "gray"}>
              {t(`status.${user.status}`)}
            </Badge>
            {protocols.map((protocol) => (
              <Badge key={protocol} tone="brand">
                {protocol}
              </Badge>
            ))}
          </div>
        </div>

        <div className="flex flex-col gap-1.5">
          <div className="flex items-center justify-between gap-2 text-xs text-rapido-muted">
            <span className="tabular-nums" dir="ltr">
              {formatBytes(user.used_traffic)}
            </span>
            <span
              className={hasLimit ? "tabular-nums" : undefined}
              dir={hasLimit ? "ltr" : undefined}
            >
              {hasLimit
                ? formatBytes(user.data_limit as number)
                : t("rapido.unlimited")}
            </span>
          </div>
          <div className="h-1.5 w-full overflow-hidden rounded-full bg-rapido-border">
            <div
              className={classNames(
                "h-full rounded-full",
                user.status === "limited" ? "bg-red-500" : "bg-rapido-accent"
              )}
              style={{ width: `${usedPercent}%` }}
            />
          </div>
        </div>

        <div className="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 text-xs text-rapido-muted">
          <span
            className={
              expiryInfo.status === "expired" ? "text-red-400" : undefined
            }
          >
            {expiryInfo.status === "expires" &&
              t("expires", { time: ltrIsolate(expiryInfo.time) })}
            {expiryInfo.status === "expired" &&
              t("expired", { time: ltrIsolate(expiryInfo.time) })}
          </span>
          <span>
            {t("rapido.totalLabel", {
              value: ltrIsolate(String(formatBytes(user.lifetime_used_traffic))),
            })}
          </span>
        </div>

        <div className="border-t border-rapido-border pt-2">
          <UserActions user={user} />
        </div>
      </div>
    </Card>
  );
};

export const UsersTable: FC = () => {
  const { t } = useTranslation();
  const filters = useUsersUiStore((s) => s.filters);
  const setFilters = useUsersUiStore((s) => s.setFilters);
  const setCreatingNewUser = useUsersUiStore((s) => s.setCreatingNewUser);

  const { data, isLoading } = useUsersQuery(filters);
  const users = data?.users ?? [];
  const total = data?.total ?? 0;

  const [searchInput, setSearchInput] = useState(filters.search ?? "");

  const debouncedSearch = useMemo(
    () =>
      debounce((value: string) => {
        setFilters({ search: value, offset: 0 });
      }, 300),
    [setFilters]
  );

  useEffect(() => {
    return () => debouncedSearch.cancel();
  }, [debouncedSearch]);

  const limit = filters.limit || 10;
  const offset = filters.offset || 0;
  const rangeStart = total === 0 ? 0 : offset + 1;
  const rangeEnd = Math.min(offset + limit, total);
  const pageCount = Math.max(1, Math.ceil(total / limit));
  const currentPage = Math.floor(offset / limit) + 1;
  const pageItems = useMemo(
    () => buildPageItems(currentPage, pageCount),
    [currentPage, pageCount]
  );

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-3">
        <Input
          type="text"
          value={searchInput}
          placeholder={t("rapido.searchUsers")}
          className="min-w-[220px] flex-1"
          onChange={(e) => {
            const value = e.target.value;
            setSearchInput(value);
            debouncedSearch(value);
          }}
        />

        <Select
          value={filters.status ?? ""}
          onChange={(e) =>
            setFilters({
              status: (e.target.value || undefined) as UsersFilters["status"],
              offset: 0,
            })
          }
        >
          {statusFilterOptions.map((opt) => (
            <option key={opt.value} value={opt.value}>
              {t(opt.labelKey)}
            </option>
          ))}
        </Select>

        <Select
          value={filters.sort}
          onChange={(e) => setFilters({ sort: e.target.value })}
        >
          {sortOptions.map((opt) => (
            <option key={opt.value} value={opt.value}>
              {t(opt.labelKey)}
            </option>
          ))}
        </Select>

        {/* ms-auto, not ml-auto: pushing an item to the end of a flex row means
            an auto margin on its *start* side. In RTL the free space is already
            on the left, so margin-left:auto absorbed nothing. */}
        <Button
          variant="primary"
          className="ms-auto shrink-0"
          onClick={() => setCreatingNewUser(true)}
        >
          <span aria-hidden="true">+ </span>
          {t("rapido.newUser")}
        </Button>
      </div>

      {isLoading && (
        <div className="py-12 text-center text-sm text-rapido-muted">
          {t("rapido.loadingUsers")}
        </div>
      )}

      {!isLoading && users.length === 0 && (
        <div className="py-12 text-center text-sm text-rapido-muted">
          {t("usersTable.noUserMatched")}
        </div>
      )}

      {!isLoading && users.length > 0 && (
        <div className="flex flex-col gap-3">
          {users.map((user) => (
            <UserRow key={user.username} user={user} />
          ))}
        </div>
      )}

      <div className="flex flex-wrap items-center justify-between gap-3 text-xs text-rapido-muted">
        <div className="flex items-center gap-3">
          <span>
            {t("rapido.range", { start: rangeStart, end: rangeEnd, total })}
          </span>
          <label className="flex items-center gap-1.5">
            {t("itemsPerPage")}
            <Select
              value={limit}
              className="px-2 py-1 text-xs"
              onChange={(e) => {
                const next = Number(e.target.value);
                // Remembered across sessions, same preference the old
                // dashboard used, so switching views doesn't reset it.
                setUsersPerPageLimitSize(String(next));
                setFilters({ limit: next, offset: 0 });
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
            onClick={() => setFilters({ offset: Math.max(offset - limit, 0) })}
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
                onClick={() => setFilters({ offset: (item - 1) * limit })}
              >
                {item}
              </Button>
            )
          )}

          <Button
            variant="chip"
            disabled={currentPage >= pageCount}
            onClick={() => setFilters({ offset: offset + limit })}
          >
            {t("next")}
          </Button>
        </div>
      </div>
    </div>
  );
};

export default UsersTable;
