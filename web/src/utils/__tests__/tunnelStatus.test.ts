import { describe, expect, it } from "vitest";
import { MonitoringTunnel } from "types/Monitoring";
import {
  handshakeAge,
  isTunnelUp,
  sortTunnels,
  summarizeFleetTunnels,
  tunnelProblemLabel,
  tunnelStatus,
  tunnelTone,
} from "../tunnelStatus";

const tunnel = (overrides: Partial<MonitoringTunnel> = {}): MonitoringTunnel => ({
  name: "wg0",
  up: true,
  present: true,
  rx_bytes: 0,
  tx_bytes: 0,
  last_handshake: 0,
  handshake_age_seconds: null,
  probe_ms: null,
  fallback_active: false,
  ...overrides,
});

describe("tunnelStatus", () => {
  it("is up for a present, up tunnel", () => {
    expect(tunnelStatus(tunnel())).toBe("up");
    expect(isTunnelUp(tunnel())).toBe(true);
  });

  it("is down for a present tunnel that is down without a fallback", () => {
    expect(tunnelStatus(tunnel({ up: false }))).toBe("down");
  });

  it("is down-fallback when the tunnel is down and the direct fallback is carrying traffic", () => {
    expect(tunnelStatus(tunnel({ up: false, fallback_active: true }))).toBe("down-fallback");
  });

  it("is missing when the interface does not exist, even if the flags say otherwise", () => {
    expect(tunnelStatus(tunnel({ present: false, up: false }))).toBe("missing");
    expect(tunnelStatus(tunnel({ present: false, up: true }))).toBe("missing");
    expect(tunnelStatus(tunnel({ present: false, up: false, fallback_active: true }))).toBe("missing");
    expect(isTunnelUp(tunnel({ present: false, up: true }))).toBe(false);
  });

  it("ignores a fallback flag on a tunnel that is up", () => {
    expect(tunnelStatus(tunnel({ up: true, fallback_active: true }))).toBe("up");
  });
});

describe("tunnelTone", () => {
  it("maps each status to its badge colour: green up, amber fallback, red otherwise", () => {
    expect(tunnelTone("up")).toBe("green");
    expect(tunnelTone("down-fallback")).toBe("yellow");
    expect(tunnelTone("down")).toBe("red");
    expect(tunnelTone("missing")).toBe("red");
  });
});

describe("handshakeAge", () => {
  const now = 1_000_000;

  it("is never when there is neither an age nor a handshake time", () => {
    expect(handshakeAge({ last_handshake: 0, handshake_age_seconds: null }, now)).toEqual({ kind: "never" });
  });

  it("uses the server-computed age in preference to last_handshake", () => {
    expect(handshakeAge({ last_handshake: now - 9999, handshake_age_seconds: 12 }, now)).toEqual({
      kind: "ago",
      unit: "seconds",
      value: 12,
    });
  });

  it("falls back to last_handshake against the supplied clock when the age is missing", () => {
    expect(handshakeAge({ last_handshake: now - 180, handshake_age_seconds: null }, now)).toEqual({
      kind: "ago",
      unit: "minutes",
      value: 3,
    });
  });

  it("does not report never for a server age of zero", () => {
    expect(handshakeAge({ last_handshake: 0, handshake_age_seconds: 0 }, now)).toEqual({
      kind: "ago",
      unit: "seconds",
      value: 0,
    });
  });

  it("picks the unit at each boundary and floors the value", () => {
    const age = (s: number) => handshakeAge({ last_handshake: 0, handshake_age_seconds: s }, now);
    expect(age(59)).toEqual({ kind: "ago", unit: "seconds", value: 59 });
    expect(age(60)).toEqual({ kind: "ago", unit: "minutes", value: 1 });
    expect(age(3599)).toEqual({ kind: "ago", unit: "minutes", value: 59 });
    expect(age(3600)).toEqual({ kind: "ago", unit: "hours", value: 1 });
    expect(age(86399)).toEqual({ kind: "ago", unit: "hours", value: 23 });
    expect(age(86400)).toEqual({ kind: "ago", unit: "days", value: 1 });
    expect(age(2.9)).toEqual({ kind: "ago", unit: "seconds", value: 2 });
  });

  it("clamps a handshake that appears to be in the future (clock skew) to zero", () => {
    expect(handshakeAge({ last_handshake: now + 50, handshake_age_seconds: null }, now)).toEqual({
      kind: "ago",
      unit: "seconds",
      value: 0,
    });
  });
});

describe("sortTunnels", () => {
  it("puts down and missing tunnels before up ones, then sorts by name", () => {
    const sorted = sortTunnels([
      tunnel({ name: "wg-b" }),
      tunnel({ name: "wg-z", up: false }),
      tunnel({ name: "wg-a" }),
      tunnel({ name: "wg-m", present: false, up: false }),
      tunnel({ name: "wg-c", up: false, fallback_active: true }),
    ]);
    expect(sorted.map((t) => t.name)).toEqual(["wg-c", "wg-m", "wg-z", "wg-a", "wg-b"]);
  });

  it("orders numbered names naturally, not lexically", () => {
    const sorted = sortTunnels([tunnel({ name: "wg10" }), tunnel({ name: "wg2" }), tunnel({ name: "wg1" })]);
    expect(sorted.map((t) => t.name)).toEqual(["wg1", "wg2", "wg10"]);
  });

  it("does not mutate its input", () => {
    const input = [tunnel({ name: "b" }), tunnel({ name: "a", up: false })];
    sortTunnels(input);
    expect(input.map((t) => t.name)).toEqual(["b", "a"]);
  });
});

describe("summarizeFleetTunnels", () => {
  const host = (name: string, tunnels: MonitoringTunnel[] | null | undefined, reachable = true) => ({
    name,
    reachable,
    tunnels,
  });

  it("reports no problems and severity none when every tunnel is up", () => {
    const s = summarizeFleetTunnels([host("de", [tunnel({ name: "wg0" })]), host("Panel", null)]);
    expect(s).toEqual({ problems: [], total: 1, withFallback: 0, severity: "none" });
  });

  it("lists every not-up tunnel as node: tunnel, in host order then sorted within a host", () => {
    const s = summarizeFleetTunnels([
      host("de", [tunnel({ name: "wg2", up: false }), tunnel({ name: "wg1", present: false, up: false })]),
      host("nl", [tunnel({ name: "wg0", up: false })]),
    ]);
    expect(s.problems.map(tunnelProblemLabel)).toEqual(["de: wg1", "de: wg2", "nl: wg0"]);
    expect(s.problems.map((p) => p.status)).toEqual(["missing", "down", "down"]);
  });

  it("is a warning when every problem is a down tunnel covered by the fallback", () => {
    const s = summarizeFleetTunnels([host("de", [tunnel({ up: false, fallback_active: true })])]);
    expect(s.severity).toBe("warning");
    expect(s.withFallback).toBe(1);
  });

  it("is critical as soon as one problem has nothing behind it", () => {
    const s = summarizeFleetTunnels([
      host("de", [tunnel({ name: "a", up: false, fallback_active: true }), tunnel({ name: "b", up: false })]),
    ]);
    expect(s.severity).toBe("critical");
    expect(s.withFallback).toBe(1);
  });

  it("treats a missing interface as critical even when a fallback is carrying its traffic", () => {
    const s = summarizeFleetTunnels([host("de", [tunnel({ present: false, up: false, fallback_active: true })])]);
    expect(s.severity).toBe("critical");
    expect(s.withFallback).toBe(1);
  });

  it("skips hosts that never reported and hosts with no tunnel detail", () => {
    const s = summarizeFleetTunnels([
      host("dead", [tunnel({ up: false })], false),
      host("old-backend", undefined),
      host("Panel", []),
    ]);
    expect(s).toEqual({ problems: [], total: 0, withFallback: 0, severity: "none" });
  });
});
