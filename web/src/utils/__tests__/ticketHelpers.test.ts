import { describe, expect, it, vi } from "vitest";
import { AdminTicket } from "types/Ticket";
import { isAwaitingReply, lastMessageOf, relativeTime, absoluteTime } from "../ticketHelpers";

const ticket = (overrides: Partial<AdminTicket> = {}): AdminTicket => ({
  id: 1,
  subject: "Cannot connect",
  status: "open",
  created_at: "2026-01-01T00:00:00",
  updated_at: "2026-01-01T00:00:00",
  messages: [],
  username: "alice",
  owner: null,
  ...overrides,
});

describe("lastMessageOf", () => {
  it("returns null for a ticket with no messages", () => {
    expect(lastMessageOf(ticket())).toBeNull();
  });

  it("returns the last message, not the first", () => {
    const t = ticket({
      messages: [
        { id: 1, is_admin: false, body: "first", created_at: "2026-01-01T00:00:00" },
        { id: 2, is_admin: true, body: "second", created_at: "2026-01-01T01:00:00" },
      ],
    });
    expect(lastMessageOf(t)?.id).toBe(2);
  });
});

describe("isAwaitingReply", () => {
  it("is false for a closed ticket even if the customer spoke last", () => {
    const t = ticket({
      status: "closed",
      messages: [{ id: 1, is_admin: false, body: "hi", created_at: "2026-01-01T00:00:00" }],
    });
    expect(isAwaitingReply(t)).toBe(false);
  });

  it("is false for an open ticket with no messages at all", () => {
    expect(isAwaitingReply(ticket({ status: "open", messages: [] }))).toBe(false);
  });

  it("is false when the admin sent the last message", () => {
    const t = ticket({
      status: "open",
      messages: [
        { id: 1, is_admin: false, body: "question", created_at: "2026-01-01T00:00:00" },
        { id: 2, is_admin: true, body: "answer", created_at: "2026-01-01T01:00:00" },
      ],
    });
    expect(isAwaitingReply(t)).toBe(false);
  });

  it("is true for an open ticket whose last message came from the customer", () => {
    const t = ticket({
      status: "open",
      messages: [
        { id: 1, is_admin: true, body: "hi, how can I help?", created_at: "2026-01-01T00:00:00" },
        { id: 2, is_admin: false, body: "still broken", created_at: "2026-01-01T01:00:00" },
      ],
    });
    expect(isAwaitingReply(t)).toBe(true);
  });
});

describe("relativeTime", () => {
  it("reads a naive (no-timezone) API timestamp as UTC, not local time", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-01-01T01:00:00Z"));
    // No trailing Z on the API value - must still be treated as UTC.
    expect(relativeTime("en", "2026-01-01T00:00:00")).toBe("1 hour ago");
    vi.useRealTimers();
  });

  it("returns an empty string for an unparseable date", () => {
    expect(relativeTime("en", "not-a-date")).toBe("");
  });

  it("falls back to the runtime locale for an invalid language tag", () => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date("2026-01-01T00:01:00Z"));
    expect(() => relativeTime("not-a-real-locale-tag!!", "2026-01-01T00:00:00")).not.toThrow();
    vi.useRealTimers();
  });
});

describe("absoluteTime", () => {
  it("returns an empty string for an unparseable date", () => {
    expect(absoluteTime("en", "garbage")).toBe("");
  });

  it("formats a valid date to a non-empty string", () => {
    expect(absoluteTime("en", "2026-01-01T12:00:00Z")).not.toBe("");
  });
});
