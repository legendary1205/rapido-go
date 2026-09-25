import dayjs from "dayjs";
import Duration from "dayjs/plugin/duration";
import utc from "dayjs/plugin/utc";
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { lastSeenOf } from "utils/presence";

// main.tsx registers these; relativeExpiryDate needs them, and dayjs() reads
// the real clock, so the clock is pinned to the same instant the tests use.
dayjs.extend(utc);
dayjs.extend(Duration);

const NOW = Date.parse("2026-09-25T12:00:00Z");
beforeAll(() => {
  vi.useFakeTimers();
  vi.setSystemTime(NOW);
});
afterAll(() => {
  vi.useRealTimers();
});
const ago = (seconds: number) => new Date(NOW - seconds * 1000).toISOString();

describe("lastSeenOf", () => {
  it("trusts the live flag over online_at when it is present", () => {
    // Connected right now even though the traffic timestamp is old ...
    expect(lastSeenOf(ago(3600), true, NOW)).toEqual({ kind: "online" });
    expect(lastSeenOf(null, true, NOW)).toEqual({ kind: "online" });
    // ... and NOT connected although traffic was seen a minute ago.
    expect(lastSeenOf(ago(90), false, NOW)).toEqual({ kind: "seen", time: "1 min" });
  });

  it("says 'under a minute' (an empty time) for a fresh last activity that is no longer online", () => {
    expect(lastSeenOf(ago(20), false, NOW)).toEqual({ kind: "seen", time: "" });
    // A clock a little ahead of ours must not print a bogus "expires in".
    expect(lastSeenOf(ago(-30), false, NOW)).toEqual({ kind: "seen", time: "" });
  });

  it("reports never when there is no activity at all", () => {
    expect(lastSeenOf(null, false, NOW)).toEqual({ kind: "never" });
    expect(lastSeenOf(null, undefined, NOW)).toEqual({ kind: "never" });
    expect(lastSeenOf("not a date", false, NOW)).toEqual({ kind: "never" });
  });

  it("falls back to the 180 s window only when the backend sends no flag", () => {
    expect(lastSeenOf(ago(100), undefined, NOW)).toEqual({ kind: "online" });
    expect(lastSeenOf(ago(180), undefined, NOW)).toEqual({ kind: "online" });
    expect(lastSeenOf(ago(181), undefined, NOW)).toEqual({ kind: "seen", time: "3 mins" });
    expect(lastSeenOf(ago(7200), undefined, NOW)).toMatchObject({ kind: "seen" });
  });
});
