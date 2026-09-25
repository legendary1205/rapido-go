// Mirrors GET /api/logs/sources and GET /api/logs (sudo only).
export type LogLevel = "debug" | "info" | "warn" | "error";

export type LogSource = {
  /** "panel", "backend" or "node:<id>". */
  id: string;
  label: string;
  kind: "panel" | "backend" | "node";
  /** Nodes only: the node's own status (connected/connecting/error/disabled). */
  status?: string;
};

export type LogEntry = {
  /** Microseconds since epoch, monotonic per source. Fits a JS number
   * (about 1.8e15, well under 2^53), so it is used as-is as the cursor. */
  id: number;
  /** RFC3339Nano. */
  ts: string;
  level: LogLevel;
  /** The original line: a JSON object for the panel/backend sources, plain
   * text for a node. */
  line: string;
};

export type LogsResponse = {
  entries: LogEntry[];
  /** Newest id returned, or the request's `after` when nothing was returned. */
  next: number;
  /** Node sources only: a node-live report arrived in the last 15 s. */
  streaming: boolean;
};
