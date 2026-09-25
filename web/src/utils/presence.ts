import { relativeExpiryDate } from "utils/dateFormatter";

export type LastSeen =
  | { kind: "online" }
  | { kind: "never" }
  /** `time` is "" when the last activity was under a minute ago. */
  | { kind: "seen"; time: string };

/** The panel's own fallback window: a user is "online" if their traffic was seen this recently. */
export const ONLINE_WINDOW_SECONDS = 180;

/**
 * Is the customer connected, and if not, since when.
 *
 * `online` is the exact answer the backend gives from live connection counts
 * (open on a node within the last 15 s). It is authoritative whenever it is
 * present, in both directions: `false` means "not connected right now" even if
 * `online_at` is a minute old. Only a backend that does not send it at all
 * (`undefined`) gets the old rule - traffic seen within the last 180 s.
 */
export const lastSeenOf = (
  onlineAt: string | null,
  online?: boolean,
  nowMs: number = Date.now()
): LastSeen => {
  if (online === true) return { kind: "online" };
  if (!onlineAt) return { kind: "never" };
  const unix = Math.floor(new Date(onlineAt).getTime() / 1000);
  if (Number.isNaN(unix)) return { kind: "never" };
  const diffSeconds = Math.floor(nowMs / 1000) - unix;
  if (online === undefined && diffSeconds <= ONLINE_WINDOW_SECONDS) return { kind: "online" };
  // Under a minute (or a clock a little ahead of ours) has no unit to print.
  return { kind: "seen", time: diffSeconds < 60 ? "" : relativeExpiryDate(unix).time };
};
