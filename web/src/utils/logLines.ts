import { LogEntry, LogLevel } from "types/Logs";

/** The most lines the page ever holds; older ones fall off the front. */
export const MAX_LOG_ENTRIES = 2000;

export type LevelFilter = "all" | "info" | "warn" | "error";

const LEVEL_RANK: Record<LogLevel, number> = { debug: 0, info: 1, warn: 2, error: 3 };

// Never trust the wire to be exactly the four documented words: a level the
// page cannot place is shown as info instead of vanishing or throwing.
export const normalizeLevel = (level: unknown): LogLevel => {
  const l = typeof level === "string" ? level.toLowerCase() : "";
  switch (l) {
    case "debug":
    case "trace":
      return "debug";
    case "warn":
    case "warning":
      return "warn";
    case "error":
    case "err":
    case "fatal":
    case "panic":
      return "error";
    default:
      return "info";
  }
};

const FILTER_MIN_RANK: Record<LevelFilter, number> = { all: 0, info: 1, warn: 2, error: 3 };

export const filterEntries = (entries: LogEntry[], level: LevelFilter, query: string): LogEntry[] => {
  const min = FILTER_MIN_RANK[level];
  const needle = query.trim().toLowerCase();
  if (min === 0 && !needle) return entries;
  return entries.filter(
    (e) =>
      LEVEL_RANK[normalizeLevel(e.level)] >= min &&
      (!needle || e.line.toLowerCase().includes(needle))
  );
};

/** Appends `fresh` (already newer than everything in `prev`) and keeps the newest `max`. */
export const appendCapped = (prev: LogEntry[], fresh: LogEntry[], max = MAX_LOG_ENTRIES): LogEntry[] => {
  if (fresh.length === 0) return prev;
  const merged = prev.length === 0 ? fresh : prev.concat(fresh);
  return merged.length > max ? merged.slice(merged.length - max) : merged;
};

export type ParsedLogLine = {
  msg: string;
  attrs: { key: string; value: string }[];
};

const RESERVED_KEYS = new Set(["time", "level", "msg"]);

const attrValue = (value: unknown): string => {
  if (typeof value === "string") return /[\s"=]/.test(value) || value === "" ? JSON.stringify(value) : value;
  if (value !== null && typeof value === "object") return JSON.stringify(value);
  return String(value);
};

/**
 * The panel and backend sources log one JSON object per line (Go's slog).
 * Returns its message and the remaining attributes, or null for anything else
 * (a node's plain text, a truncated line) so the caller falls back to the raw
 * text.
 */
export const parseLogLine = (line: string): ParsedLogLine | null => {
  if (line.charCodeAt(0) !== 123 /* { */) return null;
  let obj: unknown;
  try {
    obj = JSON.parse(line);
  } catch {
    return null;
  }
  if (obj === null || typeof obj !== "object" || Array.isArray(obj)) return null;
  const rec = obj as Record<string, unknown>;
  if (typeof rec.msg !== "string") return null;
  const attrs = Object.keys(rec)
    .filter((k) => !RESERVED_KEYS.has(k))
    .map((key) => ({ key, value: attrValue(rec[key]) }));
  return { msg: rec.msg, attrs };
};

const pad = (n: number, width = 2) => String(n).padStart(width, "0");

/** HH:MM:SS.mmm in the viewer's local time; falls back to the id (microseconds) if `ts` is unusable. */
export const formatLogTime = (entry: LogEntry): string => {
  let d = new Date(entry.ts);
  if (Number.isNaN(d.getTime())) d = new Date(entry.id / 1000);
  if (Number.isNaN(d.getTime())) return "--:--:--.---";
  return `${pad(d.getHours())}:${pad(d.getMinutes())}:${pad(d.getSeconds())}.${pad(d.getMilliseconds(), 3)}`;
};

/** One line per entry, for the .txt download. */
export const entriesToText = (entries: LogEntry[]): string =>
  entries.map((e) => `${e.ts} ${normalizeLevel(e.level).toUpperCase().padEnd(5)} ${e.line}`).join("\n") +
  (entries.length ? "\n" : "");
