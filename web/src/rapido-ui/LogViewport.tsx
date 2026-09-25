import { FC, memo, useLayoutEffect, useMemo, useRef, useState } from "react";
import classNames from "classnames";
import { useTranslation } from "react-i18next";
import { LogEntry, LogLevel } from "types/Logs";
import { formatLogTime, normalizeLevel, parseLogLine } from "utils/logLines";

// Every row is exactly this tall (one line, never wrapped), which is what
// makes windowing trivial and cheap: the scroll height is rows * ROW_HEIGHT and
// only the rows in view (plus a little either side) exist in the DOM. A long
// line scrolls sideways instead of growing its row.
export const LOG_ROW_HEIGHT = 22;
const OVERSCAN_ROWS = 12;
// jsdom has no layout, and a freshly mounted element can report 0 before its
// first measurement; both fall back to a tall-enough window rather than none.
const FALLBACK_VIEW_HEIGHT = 600;
// How close to the bottom still counts as "at the bottom" - a fractional
// scrollTop or a scrollbar's worth of rounding must not switch follow off.
const FOLLOW_SLACK_PX = 24;

// A single pathological line (a dumped payload) must not make every row as wide
// as itself. The download and the search still use the full text.
const MAX_LINE_CHARS = 4000;

const levelBadge: Record<LogLevel, string> = {
  debug: "bg-rapido-raised text-rapido-muted",
  info: "bg-sky-500/15 text-sky-400",
  warn: "bg-amber-500/15 text-amber-400",
  error: "bg-red-500/15 text-red-400",
};

const levelText: Record<LogLevel, string> = {
  debug: "text-rapido-muted",
  info: "text-rapido-text",
  warn: "text-amber-200",
  error: "text-red-300",
};

const LogRow = memo(({ entry }: { entry: LogEntry }) => {
  const level = normalizeLevel(entry.level);
  const tooLong = entry.line.length > MAX_LINE_CHARS;
  const parsed = useMemo(() => (tooLong ? null : parseLogLine(entry.line)), [entry.line, tooLong]);

  return (
    <div
      data-level={level}
      title={entry.ts}
      style={{ height: LOG_ROW_HEIGHT }}
      className={classNames(
        "flex min-w-full w-max items-center gap-2 whitespace-pre px-3 font-mono text-xs leading-none hover:bg-rapido-raised/60",
        level === "error" && "bg-red-500/[0.05]"
      )}
    >
      <span className="shrink-0 tabular-nums text-rapido-muted">{formatLogTime(entry)}</span>
      <span
        className={classNames(
          "w-[3.25rem] shrink-0 rounded px-1 py-0.5 text-center text-[10px] font-semibold uppercase",
          levelBadge[level]
        )}
      >
        {level}
      </span>
      {parsed ? (
        <>
          <span className={levelText[level]}>{parsed.msg}</span>
          {parsed.attrs.map((a) => (
            <span key={a.key} className="text-rapido-muted">
              <span className="opacity-60">{a.key}=</span>
              {a.value}
            </span>
          ))}
        </>
      ) : (
        <span className={levelText[level]}>{tooLong ? `${entry.line.slice(0, MAX_LINE_CHARS)}…` : entry.line}</span>
      )}
    </div>
  );
});
LogRow.displayName = "LogRow";

/**
 * The scrolling log area: windowed rows, always left-to-right (log text is
 * machine output; a Persian page must not mirror timestamps, paths and JSON),
 * and pinned to the newest line until the user scrolls up.
 */
export const LogViewport: FC<{ rows: LogEntry[]; emptyText: string }> = ({ rows, emptyText }) => {
  const { t } = useTranslation();
  const ref = useRef<HTMLDivElement>(null);
  const [scrollTop, setScrollTop] = useState(0);
  const [measured, setMeasured] = useState(0);
  const [follow, setFollow] = useState(true);

  const totalHeight = rows.length * LOG_ROW_HEIGHT;
  const viewHeight = measured || FALLBACK_VIEW_HEIGHT;

  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const measure = () => setMeasured(el.clientHeight);
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(el);
    return () => observer.disconnect();
  }, []);

  // Pin to the newest line whenever the content changes while following. Done
  // in a layout effect (and mirrored into state) so the window that paints is
  // already the bottom one, not a frame of stale rows.
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el || !follow) return;
    el.scrollTop = totalHeight;
    setScrollTop(el.scrollTop);
  }, [follow, totalHeight, rows]);

  const onScroll = () => {
    const el = ref.current;
    if (!el) return;
    setScrollTop(el.scrollTop);
    // Measured against the row count rather than el.scrollHeight so it means
    // the same thing before and after the window re-renders.
    const atBottom = totalHeight - el.scrollTop - (el.clientHeight || FALLBACK_VIEW_HEIGHT) <= FOLLOW_SLACK_PX;
    setFollow(atBottom);
  };

  const jumpToLatest = () => {
    const el = ref.current;
    setFollow(true);
    if (el) {
      el.scrollTop = totalHeight;
      setScrollTop(el.scrollTop);
    }
  };

  // A browser never scrolls past the end, but the state can briefly say so
  // (rows removed by a filter before the next scroll event), and a window that
  // starts beyond the last row would render nothing.
  const top = Math.min(scrollTop, Math.max(0, totalHeight - viewHeight));
  const first = Math.max(0, Math.floor(top / LOG_ROW_HEIGHT) - OVERSCAN_ROWS);
  const last = Math.min(rows.length, Math.ceil((top + viewHeight) / LOG_ROW_HEIGHT) + OVERSCAN_ROWS);
  const visible = rows.slice(first, last);

  return (
    <div className="relative h-[calc(100vh-24rem)] min-h-[20rem] rounded-lg border border-rapido-border bg-rapido-bg">
      {/* Only the log text is forced left-to-right. The overlay message and the
          jump button below are translated UI, so they keep the page direction -
          inside an LTR box a Persian sentence would lose its full stop to the
          wrong end. */}
      <div
        ref={ref}
        onScroll={onScroll}
        dir="ltr"
        role="log"
        aria-live="off"
        data-testid="log-viewport"
        className="h-full overflow-auto"
      >
        <div style={{ height: totalHeight, minWidth: "max-content" }}>
          <div style={{ transform: `translateY(${first * LOG_ROW_HEIGHT}px)` }}>
            {visible.map((entry) => (
              <LogRow key={entry.id} entry={entry} />
            ))}
          </div>
        </div>
      </div>

      {rows.length === 0 && (
        <div className="pointer-events-none absolute inset-0 flex items-center justify-center px-6 text-center text-sm text-rapido-muted">
          {emptyText}
        </div>
      )}

      {!follow && rows.length > 0 && (
        <button
          type="button"
          onClick={jumpToLatest}
          className="absolute bottom-3 end-4 rounded-full border border-rapido-accent/40 bg-rapido-surface px-3 py-1.5 text-xs font-medium text-rapido-accent shadow-lg transition-colors hover:border-rapido-accent hover:bg-rapido-raised"
        >
          {t("rapido.logs.jumpToLatest")} ↓
        </button>
      )}
    </div>
  );
};

export default LogViewport;
