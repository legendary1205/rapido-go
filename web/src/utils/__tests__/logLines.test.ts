import { describe, expect, it } from "vitest";
import { LogEntry } from "types/Logs";
import {
  MAX_LOG_ENTRIES,
  appendCapped,
  entriesToText,
  filterEntries,
  formatLogTime,
  normalizeLevel,
  parseLogLine,
} from "utils/logLines";

const entry = (id: number, level: string, line: string): LogEntry => ({
  id,
  ts: new Date(id / 1000).toISOString(),
  level: level as LogEntry["level"],
  line,
});

describe("parseLogLine", () => {
  it("splits an slog JSON line into the message and the remaining attributes", () => {
    const parsed = parseLogLine(
      '{"time":"2026-09-25T04:00:00Z","level":"INFO","msg":"request done","status":200,"path":"/api/user","dur":"1.5ms","err":null,"opts":{"a":1},"note":"two words"}'
    );
    expect(parsed?.msg).toBe("request done");
    expect(parsed?.attrs).toEqual([
      { key: "status", value: "200" },
      { key: "path", value: "/api/user" },
      { key: "dur", value: "1.5ms" },
      { key: "err", value: "null" },
      { key: "opts", value: '{"a":1}' },
      { key: "note", value: '"two words"' },
    ]);
  });

  it("gives up on anything that is not a JSON object with a msg", () => {
    expect(parseLogLine("inbound/vless[main#20001]: process connection: EOF")).toBeNull();
    expect(parseLogLine('{"truncated": "line')).toBeNull();
    expect(parseLogLine('{"level":"info"}')).toBeNull();
    expect(parseLogLine("[1,2]")).toBeNull();
    expect(parseLogLine("")).toBeNull();
  });
});

describe("normalizeLevel", () => {
  it("maps the documented levels and common synonyms, and defaults to info", () => {
    expect(normalizeLevel("debug")).toBe("debug");
    expect(normalizeLevel("WARNING")).toBe("warn");
    expect(normalizeLevel("Error")).toBe("error");
    expect(normalizeLevel("fatal")).toBe("error");
    expect(normalizeLevel("wat")).toBe("info");
    expect(normalizeLevel(undefined)).toBe("info");
  });
});

describe("filterEntries", () => {
  const all = [
    entry(1, "debug", "cache warmed"),
    entry(2, "info", "started"),
    entry(3, "warn", "Slow query"),
    entry(4, "error", "db timeout"),
  ];

  it("keeps everything for 'all' with no search", () => {
    expect(filterEntries(all, "all", "")).toBe(all);
  });

  it("treats the level as a minimum", () => {
    expect(filterEntries(all, "info", "").map((e) => e.id)).toEqual([2, 3, 4]);
    expect(filterEntries(all, "warn", "").map((e) => e.id)).toEqual([3, 4]);
    expect(filterEntries(all, "error", "").map((e) => e.id)).toEqual([4]);
  });

  it("searches the raw line, case-insensitively, and combines with the level", () => {
    expect(filterEntries(all, "all", "  SLOW ").map((e) => e.id)).toEqual([3]);
    expect(filterEntries(all, "error", "slow")).toEqual([]);
  });
});

describe("appendCapped", () => {
  it("keeps only the newest entries once over the cap", () => {
    const prev = Array.from({ length: MAX_LOG_ENTRIES }, (_, i) => entry(i + 1, "info", `l${i}`));
    const fresh = [entry(MAX_LOG_ENTRIES + 1, "info", "new1"), entry(MAX_LOG_ENTRIES + 2, "info", "new2")];
    const out = appendCapped(prev, fresh);
    expect(out).toHaveLength(MAX_LOG_ENTRIES);
    expect(out[0].id).toBe(3);
    expect(out[out.length - 1].line).toBe("new2");
  });

  it("returns the same array when there is nothing to add", () => {
    const prev = [entry(1, "info", "a")];
    expect(appendCapped(prev, [])).toBe(prev);
  });
});

describe("formatting", () => {
  it("prints local HH:MM:SS.mmm from ts, and falls back to the id", () => {
    const d = new Date(2026, 8, 25, 4, 5, 6, 7);
    const e: LogEntry = { id: d.getTime() * 1000, ts: d.toISOString(), level: "info", line: "x" };
    expect(formatLogTime(e)).toBe("04:05:06.007");
    expect(formatLogTime({ ...e, ts: "garbage" })).toBe("04:05:06.007");
  });

  it("writes one line per entry for the download", () => {
    const text = entriesToText([entry(1000, "warn", "a b"), entry(2000, "error", "c")]);
    expect(text).toBe(
      `${new Date(1).toISOString()} WARN  a b\n${new Date(2).toISOString()} ERROR c\n`
    );
    expect(entriesToText([])).toBe("");
  });
});
