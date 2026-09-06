import { FC, useMemo, useState } from "react";
import classNames from "classnames";
import { useTranslation } from "react-i18next";

import { useMonitoringHistoryQuery, useMonitoringQuery } from "hooks/useMonitoringQuery";
import { MonitoringHost } from "types/Monitoring";
import { formatRate, hostDisplayState, hostTone, meterTone } from "utils/monitoringHost";
import { buildSparklinePath } from "utils/sparkline";
import { Badge } from "./Badge";
import { Card, CardSubtitle, CardTitle } from "./Card";

// Ported from the old (Python-backed) dashboard's rapido-ui/Monitoring.tsx,
// trimmed to what this backend's GET /monitoring actually reports
// (internal/httpapi/monitoring.go's monitoringHostDTO). Dropped versus the
// old version, all because the Go node agent simply doesn't report them:
//  - uptime, load average, and the Xray running/version badge.
//  - per-request counts ("requests" breakdown).
//  - per-tunnel/per-peer WireGuard detail with individual handshake ages -
//    tunnels_up/tunnels_total are just counts here, so the tunnel section
//    below is a single "N/M tunnels up" badge, not a per-tunnel list.
// `Meter` is exported so OverviewNew.tsx's reinstated fleet-health section
// can reuse the exact same meter look without a second implementation.

const barColorClass: Record<ReturnType<typeof meterTone>, string> = {
  empty: "bg-white/10",
  green: "bg-emerald-500",
  yellow: "bg-yellow-500",
  red: "bg-red-500",
};

export const Meter: FC<{ label: string; value: number | null }> = ({ label, value }) => (
  <div>
    <div className="flex items-baseline justify-between">
      <span className="text-xs text-rapido-muted">{label}</span>
      <span className="tabular-nums text-xs font-medium text-rapido-text" dir="ltr">
        {value === null ? "—" : `${value}%`}
      </span>
    </div>
    <div className="mt-1 h-1.5 w-full overflow-hidden rounded-full bg-white/5">
      <div
        className={classNames("h-full rounded-full transition-all", barColorClass[meterTone(value)])}
        style={{ width: `${Math.min(100, Math.max(0, value ?? 0))}%` }}
      />
    </div>
  </div>
);

/**
 * A plain SVG sparkline - no charting library. One of these is drawn per
 * host per metric, and a chart library per card costs more than the picture
 * is worth.
 */
const Spark: FC<{ points: number[]; className?: string }> = ({ points, className }) => {
  const d = buildSparklinePath(points);
  if (!d) return <div className="h-8" />;
  return (
    <svg viewBox="0 0 100 30" preserveAspectRatio="none" className={classNames("h-8 w-full", className)}>
      <path d={d} fill="none" stroke="currentColor" strokeWidth="1.5" vectorEffect="non-scaling-stroke" />
    </svg>
  );
};

const HostCard: FC<{ host: MonitoringHost }> = ({ host }) => {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const { data: history, isLoading: historyLoading } = useMonitoringHistoryQuery(host.node_id, open);

  const state = hostDisplayState(host);
  const tone = hostTone(host);
  const statusLabel =
    state === "healthy"
      ? t("rapido.monitoring.online")
      : state === "stale"
      ? t("rapido.monitoring.stale")
      : state === "unhealthy"
      ? t("rapido.monitoring.unhealthy")
      : t("rapido.monitoring.unreachable");

  const borderTone =
    state === "healthy"
      ? "border-emerald-500/30"
      : state === "stale"
      ? "border-yellow-500/30"
      : state === "unhealthy"
      ? "border-red-500/30"
      : "border-rapido-border";

  const hasTunnels = (host.tunnels_total ?? 0) > 0;

  return (
    <Card className={classNames("flex flex-col gap-4", borderTone)}>
      <div className="flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <CardTitle className="flex flex-wrap items-center gap-2">
            <span className="truncate">{host.name}</span>
            <Badge tone={tone}>{statusLabel}</Badge>
            {host.node_id === null && <Badge tone="brand">{t("rapido.monitoring.panel")}</Badge>}
          </CardTitle>
          <CardSubtitle dir="ltr">{host.address || t("rapido.monitoring.thisMachine")}</CardSubtitle>
        </div>
        {state !== "no-data" && (
          <div className="text-end">
            <div className="text-xs text-rapido-muted">{t("rapido.monitoring.throughput")}</div>
            <div className="text-sm font-semibold text-rapido-text" dir="ltr">
              <span className="text-sky-400">↓ {formatRate(host.rx_rate)}</span>{" "}
              <span className="text-rapido-accent">↑ {formatRate(host.tx_rate)}</span>
            </div>
          </div>
        )}
      </div>

      {state === "no-data" ? (
        <div className="rounded-lg border border-rapido-border bg-rapido-raised px-3 py-2 text-xs text-rapido-muted">
          {t("rapido.monitoring.noAgent")}
        </div>
      ) : (
        <>
          <div className="grid grid-cols-3 gap-3">
            <Meter label={t("rapido.monitoring.cpu")} value={host.cpu_percent} />
            <Meter label={t("rapido.monitoring.memory")} value={host.mem_percent} />
            <Meter label={t("rapido.monitoring.disk")} value={host.disk_percent} />
          </div>

          <div className="flex flex-wrap gap-4 text-xs">
            <div>
              <span className="text-rapido-muted">{t("rapido.monitoring.connections")}: </span>
              <span className="font-medium text-rapido-text tabular-nums" dir="ltr">
                {host.connections ?? "—"}
              </span>
            </div>
            {hasTunnels && (
              <div>
                <span className="text-rapido-muted">{t("rapido.monitoring.tunnels")}: </span>
                <span className="font-medium text-rapido-text tabular-nums" dir="ltr">
                  {host.tunnels_up ?? 0}/{host.tunnels_total}
                </span>
              </div>
            )}
          </div>

          <button
            type="button"
            onClick={() => setOpen((v) => !v)}
            className="self-start text-xs text-rapido-accent hover:underline"
          >
            {open ? t("rapido.monitoring.hideHistory") : t("rapido.monitoring.showHistory")}
          </button>

          {open && (
            <div className="grid gap-3 sm:grid-cols-3">
              {historyLoading || !history ? (
                <div className="col-span-3 text-xs text-rapido-muted">{t("rapido.monitoring.loading")}</div>
              ) : history.length < 2 ? (
                <div className="col-span-3 text-xs text-rapido-muted">
                  {t("rapido.monitoring.notEnoughHistory")}
                </div>
              ) : (
                <>
                  <div>
                    <div className="text-xs text-rapido-muted">{t("rapido.monitoring.cpu")}</div>
                    <Spark points={history.map((p) => p.cpu ?? 0)} className="text-emerald-400" />
                  </div>
                  <div>
                    <div className="text-xs text-rapido-muted">↓ {t("rapido.monitoring.download")}</div>
                    <Spark points={history.map((p) => p.rx ?? 0)} className="text-sky-400" />
                  </div>
                  <div>
                    <div className="text-xs text-rapido-muted">↑ {t("rapido.monitoring.upload")}</div>
                    <Spark points={history.map((p) => p.tx ?? 0)} className="text-rapido-accent" />
                  </div>
                </>
              )}
            </div>
          )}
        </>
      )}
    </Card>
  );
};

export const Monitoring: FC = () => {
  const { t } = useTranslation();
  const { data: snap, isLoading, isError } = useMonitoringQuery();
  const hosts = useMemo(() => snap?.hosts ?? [], [snap]);

  const summary = useMemo(
    () => ({
      total: hosts.length,
      online: hosts.filter((h) => h.reachable).length,
      rx: hosts.reduce((a, h) => a + (h.rx_rate ?? 0), 0),
      tx: hosts.reduce((a, h) => a + (h.tx_rate ?? 0), 0),
      conns: hosts.reduce((a, h) => a + (h.connections ?? 0), 0),
      tunnelsUp: hosts.reduce((a, h) => a + (h.tunnels_up ?? 0), 0),
      tunnels: hosts.reduce((a, h) => a + (h.tunnels_total ?? 0), 0),
    }),
    [hosts]
  );

  return (
    <div className="flex flex-col gap-5">
      <div>
        <h1 className="text-xl font-semibold text-rapido-text">{t("rapido.monitoring.title")}</h1>
        <p className="text-sm text-rapido-muted">{t("rapido.monitoring.subtitle")}</p>
      </div>

      {isError && (
        <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-xs text-red-400">
          {t("rapido.monitoring.failed")}
        </div>
      )}

      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <Card>
          <CardSubtitle>{t("rapido.monitoring.hostsOnline")}</CardSubtitle>
          <div className="mt-1 tabular-nums text-2xl font-semibold text-rapido-text" dir="ltr">
            {summary.online}
            <span className="text-base text-rapido-muted"> / {summary.total}</span>
          </div>
        </Card>
        <Card>
          <CardSubtitle>{t("rapido.monitoring.totalThroughput")}</CardSubtitle>
          <div className="mt-1 tabular-nums text-sm font-semibold" dir="ltr">
            <div className="text-sky-400">↓ {formatRate(summary.rx)}</div>
            <div className="text-rapido-accent">↑ {formatRate(summary.tx)}</div>
          </div>
        </Card>
        <Card>
          <CardSubtitle>{t("rapido.monitoring.connections")}</CardSubtitle>
          <div className="mt-1 tabular-nums text-2xl font-semibold text-rapido-text" dir="ltr">
            {summary.conns}
          </div>
        </Card>
        <Card>
          <CardSubtitle>{t("rapido.monitoring.tunnels")}</CardSubtitle>
          <div className="mt-1 tabular-nums text-2xl font-semibold text-rapido-text" dir="ltr">
            {summary.tunnelsUp}
            <span className="text-base text-rapido-muted"> / {summary.tunnels}</span>
          </div>
        </Card>
      </div>

      {isLoading ? (
        <div className="text-sm text-rapido-muted">{t("rapido.monitoring.loading")}</div>
      ) : (
        <div className="grid gap-4 lg:grid-cols-2">
          {hosts.map((h) => (
            <HostCard key={h.node_id ?? "panel"} host={h} />
          ))}
        </div>
      )}
    </div>
  );
};

export default Monitoring;
