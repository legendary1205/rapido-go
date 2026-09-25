import { useCallback, useEffect, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { fetch } from "service/http";
import { LogEntry, LogSource, LogsResponse } from "types/Logs";
import { appendCapped } from "utils/logLines";
import { queryKeys } from "utils/queryClient";

export const LOG_POLL_MS = 1500;
/** After a failed poll (Redis down, a hiccup) ask less often. */
export const LOG_ERROR_POLL_MS = 5000;
/** The very first request (after=0) asks for the newest 300 lines. */
export const LOG_FIRST_LIMIT = 300;
export const LOG_POLL_LIMIT = 500;

// GET /logs/sources is sudo-only, so callers pass `enabled: false` for anyone
// who is not (a disabled query never fires). Refreshed now and then so a node's
// status dot follows reality.
export const useLogSourcesQuery = (enabled = true) =>
  useQuery({
    queryKey: queryKeys.logSources,
    queryFn: () => fetch<LogSource[]>("/logs/sources"),
    refetchInterval: 15_000,
    refetchIntervalInBackground: false,
    enabled,
  });

export type LogsErrorKind = "unavailable" | "notFound" | "failed";

const statusOf = (cause: unknown): number | undefined => {
  const e = cause as { response?: { status?: number }; statusCode?: number; status?: number } | undefined;
  return e?.response?.status ?? e?.statusCode ?? e?.status;
};

const classify = (cause: unknown): LogsErrorKind => {
  const status = statusOf(cause);
  if (status === 503) return "unavailable";
  if (status === 404) return "notFound";
  return "failed";
};

const isHidden = () => typeof document !== "undefined" && document.visibilityState === "hidden";

export type LogStream = {
  /** Oldest first, at most MAX_LOG_ENTRIES. */
  entries: LogEntry[];
  /** At least one poll has been answered (successfully or not). */
  loaded: boolean;
  /** A node source only: the node is sending right now. */
  streaming: boolean;
  error: LogsErrorKind | null;
  /** Empties the buffer but keeps the cursor, so old lines do not come back. */
  clear: () => void;
};

/**
 * Live tail of one log source: GET /logs?source=&after=<cursor>&limit=, every
 * LOG_POLL_MS, into a bounded in-memory buffer.
 *
 * - The first request (cursor 0) fetches the newest 300 lines; later ones only
 *   what is newer than the cursor.
 * - Nothing is requested while `paused` or `enabled` is false, or while the tab
 *   is hidden (it resumes with an immediate request when the tab is shown).
 *   Pausing keeps the buffer and the cursor, so resuming continues where it left.
 * - Requests never overlap: the next one is scheduled when the previous ends.
 * - A 404 (the source is gone) stops the loop; anything else keeps retrying,
 *   slower, because Redis coming back is the normal way out of a 503.
 * - The loop and its in-flight request die with the component.
 *
 * The buffer belongs to one source: the caller mounts this under `key={source}`
 * so switching sources starts from a clean state.
 */
export const useLogStream = (source: string, { paused, enabled }: { paused: boolean; enabled: boolean }): LogStream => {
  const [entries, setEntries] = useState<LogEntry[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [streaming, setStreaming] = useState(false);
  const [error, setError] = useState<LogsErrorKind | null>(null);
  const cursor = useRef(0);

  const clear = useCallback(() => setEntries([]), []);

  useEffect(() => {
    if (!enabled || paused) return;

    let stopped = false;
    let parked = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let controller: AbortController | undefined;

    const tick = async () => {
      timer = undefined;
      if (stopped) return;
      if (isHidden()) {
        parked = true;
        return;
      }
      parked = false;

      controller = new AbortController();
      let delay = LOG_POLL_MS;
      try {
        const after = cursor.current;
        const res = await fetch<LogsResponse>("/logs", {
          query: { source, after, limit: after === 0 ? LOG_FIRST_LIMIT : LOG_POLL_LIMIT },
          signal: controller.signal,
        });
        if (stopped) return;
        const fresh = (res.entries ?? []).filter((e) => e.id > cursor.current);
        if (typeof res.next === "number" && res.next > cursor.current) cursor.current = res.next;
        if (fresh.length > 0) {
          const newest = fresh[fresh.length - 1].id;
          if (newest > cursor.current) cursor.current = newest;
          setEntries((prev) => appendCapped(prev, fresh));
        }
        setStreaming(!!res.streaming);
        setError(null);
        setLoaded(true);
      } catch (cause) {
        if (stopped) return;
        const kind = classify(cause);
        setError(kind);
        setLoaded(true);
        if (kind === "notFound") return;
        delay = LOG_ERROR_POLL_MS;
      }
      if (!stopped) timer = setTimeout(tick, delay);
    };

    const onVisibility = () => {
      if (!stopped && parked && !isHidden()) void tick();
    };

    document.addEventListener("visibilitychange", onVisibility);
    void tick();

    return () => {
      stopped = true;
      if (timer !== undefined) clearTimeout(timer);
      controller?.abort();
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [source, paused, enabled]);

  return { entries, loaded, streaming, error, clear };
};
