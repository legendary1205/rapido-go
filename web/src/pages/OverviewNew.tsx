import { FC } from "react";
import {
  Area,
  AreaChart,
  CartesianGrid,
  Cell,
  Pie,
  PieChart,
  ResponsiveContainer,
  Tooltip,
  XAxis,
  YAxis,
} from "recharts";
import { useTranslation } from "react-i18next";
import { Link } from "react-router-dom";
import { useIsSudo } from "hooks/useCurrentAdminQuery";
import { useSystemStatsQuery } from "hooks/useSystemStatsQuery";
import { useSystemUsageHistoryQuery } from "hooks/useSystemUsageHistoryQuery";
import { useMonitoringQuery } from "hooks/useMonitoringQuery";
import { MonitoringHost } from "types/Monitoring";
import { formatBytes, numberWithCommas } from "utils/formatByte";
import { hostDisplayState, hostTone } from "utils/monitoringHost";
import { Card, CardSubtitle, CardTitle } from "rapido-ui/Card";
import { PulseDot, BadgeTone } from "rapido-ui/Badge";
import { Meter } from "rapido-ui/Monitoring";
import "rapido-ui/tailwind.css";

// `FleetStatus`/`FleetHealth` (the two node-monitoring sections below) were
// the only things this page ever dropped versus the old (Python-backed)
// dashboard's own OverviewNew.tsx - both needed GET /monitoring, which
// needed a node-reporting phase that hadn't landed yet at the time. That
// phase is done now (internal/httpapi/monitoring.go), so both are
// reinstated here, reading from the exact same useMonitoringQuery() cache
// entry MonitoringPage's own cards read (hooks/useMonitoringQuery.ts) - one
// shared 30s poller for both pages, not two independent ones asking the
// backend for the same snapshot.
//
// This page's own private QueryClientProvider is NOT coming back, though:
// it used to be its own cache that nothing else could invalidate, so a write
// from the Users page (create/delete user) could never update Overview's
// stats - they only ever refreshed on this page's own poll. The single
// app-wide queryClient (utils/queryClient.ts) fixes that outright, and stays
// fixed with the fleet sections restored.
const STATUS_COLORS: Record<string, string> = {
  active: "#34d399",
  on_hold: "#facc15",
  limited: "#fb923c",
  // Not the brand accent (cyan) - that color means "interactive" everywhere
  // else on the page, and a legend swatch is not interactive.
  expired: "#a78bfa",
  disabled: "#6b7280",
};

const TOOLTIP_CONTENT_STYLE = {
  background: "#111c22",
  border: "1px solid #223039",
  borderRadius: 8,
  color: "#e7f1f4",
};

// Recharts paints tooltip item/label text from the series colour rather than
// inheriting contentStyle.color, which renders near-black on the dark surface.
const TOOLTIP_TEXT_STYLE = { color: "#e7f1f4" };

const HeroStat: FC<{ label: string; value: string; sub?: string }> = ({
  label,
  value,
  sub,
}) => (
  <Card className="flex min-w-0 flex-col justify-center">
    <CardSubtitle>{label}</CardSubtitle>
    <div className="mt-2 flex items-baseline gap-2.5">
      <PulseDot tone="green" />
      {/* tabular-nums: this redraws every 10s, and proportional digits
          reflow the whole line width on every poll. */}
      <span className="break-words text-4xl font-bold tabular-nums text-rapido-text sm:text-5xl">
        <span dir="ltr">{value}</span>
      </span>
    </div>
    {sub && <div className="mt-1 text-xs text-rapido-muted">{sub}</div>}
  </Card>
);

const StatCard: FC<{ label: string; value: string }> = ({ label, value }) => (
  <Card className="min-w-0">
    <CardSubtitle>{label}</CardSubtitle>
    <div className="mt-2 break-words text-2xl font-semibold tabular-nums text-rapido-text">
      <span dir="ltr">{value}</span>
    </div>
  </Card>
);

const FleetChip: FC<{ host: MonitoringHost }> = ({ host }) => {
  const { t } = useTranslation();
  const tone: BadgeTone = hostTone(host);
  const state = hostDisplayState(host);

  return (
    <div className="flex shrink-0 items-center gap-2.5 rounded-lg border border-rapido-border bg-rapido-raised px-3 py-2">
      <PulseDot tone={tone} live={state === "healthy"} />
      <div className="min-w-0">
        <div className="truncate text-xs font-medium text-rapido-text">{host.name}</div>
        <div className="text-[11px] tabular-nums text-rapido-muted" dir="ltr">
          {state === "no-data"
            ? t("rapido.monitoring.unreachable")
            : `${host.connections ?? "—"} ${t("rapido.monitoring.connections")}`}
        </div>
      </div>
    </div>
  );
};

// Both fleet sections read GET /monitoring, which is sudo-only: they are only
// mounted for a sudo admin (OverviewContent), and the query itself is also
// told so, so a reseller's browser never issues a request that must 403.
const FleetStatus: FC = () => {
  const { t } = useTranslation();
  const isSudo = useIsSudo();
  const { data: snap } = useMonitoringQuery(isSudo);
  const hosts = snap?.hosts ?? [];

  return (
    <Card className="flex min-w-0 flex-col">
      <CardSubtitle>{t("rapido.fleetStatus")}</CardSubtitle>
      {hosts.length === 0 ? (
        <div className="mt-3 text-sm text-rapido-muted">
          {t("rapido.tickets.loading")}
        </div>
      ) : (
        <div className="mt-3 flex gap-2.5 overflow-x-auto pb-1">
          {hosts.map((h) => (
            <FleetChip key={h.node_id ?? "panel"} host={h} />
          ))}
        </div>
      )}
    </Card>
  );
};

const NodeResourceCard: FC<{ host: MonitoringHost }> = ({ host }) => {
  const { t } = useTranslation();
  const tone: BadgeTone = hostTone(host);
  const state = hostDisplayState(host);

  return (
    <Card className="min-w-0">
      <div className="flex items-center gap-2">
        <PulseDot tone={tone} live={state === "healthy"} />
        <span className="truncate text-sm font-medium text-rapido-text">{host.name}</span>
      </div>
      {state === "no-data" ? (
        <div className="mt-3 text-xs text-rapido-muted">{t("rapido.monitoring.noAgent")}</div>
      ) : (
        <div className="mt-3 flex flex-col gap-2">
          <Meter label={t("rapido.monitoring.cpu")} value={host.cpu_percent} />
          <Meter label={t("rapido.monitoring.memory")} value={host.mem_percent} />
          <Meter label={t("rapido.monitoring.disk")} value={host.disk_percent} />
        </div>
      )}
    </Card>
  );
};

// Reads from the exact same GET /monitoring snapshot as FleetStatus above -
// same query key (useMonitoringQuery), so react-query serves this from cache
// instead of a second request. FleetStatus is the at-a-glance strip (is
// everything up); this is the layer under it (how loaded is each one),
// which is why it skips the sparkline history and tunnel badges
// rapido-ui/Monitoring.tsx's own cards show - that level of detail is a
// click away, not a second copy of it here.
const FleetHealth: FC = () => {
  const { t } = useTranslation();
  const isSudo = useIsSudo();
  const { data: snap } = useMonitoringQuery(isSudo);
  const hosts = snap?.hosts ?? [];

  if (hosts.length === 0) return null;

  return (
    <Card className="mt-6 min-w-0">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <CardTitle>{t("rapido.resourceStatus")}</CardTitle>
          <CardSubtitle>{t("rapido.resourceStatusDesc")}</CardSubtitle>
        </div>
        <Link to="/monitoring/" className="text-xs text-rapido-accent hover:underline">
          {t("rapido.monitoring.nav")}
        </Link>
      </div>
      <div className="mt-4 grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-3">
        {hosts.map((h) => (
          <NodeResourceCard key={h.node_id ?? "panel"} host={h} />
        ))}
      </div>
    </Card>
  );
};

const OverviewContent: FC = () => {
  const { t } = useTranslation();

  const isSudo = useIsSudo();
  const { data: stats, isLoading: statsLoading } = useSystemStatsQuery();
  const { data: history, isLoading: historyLoading } =
    useSystemUsageHistoryQuery(14);

  // `status` keys the colour map, `label` is what the tooltip shows.
  const donutData = stats
    ? [
        { status: "active", label: t("status.active"), value: stats.users_active },
        { status: "on_hold", label: t("status.on_hold"), value: stats.users_on_hold },
        { status: "limited", label: t("status.limited"), value: stats.users_limited },
        { status: "expired", label: t("status.expired"), value: stats.users_expired },
        {
          status: "disabled",
          label: t("status.disabled"),
          value: stats.users_disabled,
        },
      ].filter((d) => d.value > 0)
    : [];

  // incoming_bandwidth/outgoing_bandwidth are always 0 today - honestly, per
  // internal/httpapi/system.go's own comment, because there is no
  // usage-reporting pipeline from nodes yet. This card will read "0 B" until
  // that pipeline exists in a later phase, which is correct, not broken.
  const totalDataUsage = stats
    ? stats.incoming_bandwidth + stats.outgoing_bandwidth
    : 0;

  return (
    <div className="p-4 sm:p-6">
      <h1 className="mb-1 text-2xl font-bold">{t("rapido.overview")}</h1>
      <p className="mb-6 text-sm text-rapido-muted">{t("rapido.overviewSubtitle")}</p>

      <div className={`grid grid-cols-1 gap-4 ${isSudo ? "lg:grid-cols-3" : ""}`}>
        <HeroStat
          label={t("rapido.onlineNow")}
          value={statsLoading ? "…" : numberWithCommas(stats?.online_users ?? 0) ?? "0"}
          sub={t("rapido.activeLast24h")}
        />
        {isSudo && (
          <div className="lg:col-span-2">
            <FleetStatus />
          </div>
        )}
      </div>

      <div className="mt-4 grid grid-cols-1 gap-4 sm:grid-cols-3">
        <StatCard
          label={t("rapido.totalUsers")}
          value={statsLoading ? "…" : numberWithCommas(stats?.total_user ?? 0) ?? "0"}
        />
        <StatCard
          label={t("status.active")}
          value={statsLoading ? "…" : numberWithCommas(stats?.users_active ?? 0) ?? "0"}
        />
        <StatCard
          label={t("dataUsage")}
          value={statsLoading ? "…" : String(formatBytes(totalDataUsage))}
        />
      </div>

      <div className="mt-6 grid grid-cols-1 gap-4 lg:grid-cols-2">
        <Card className="min-w-0">
          <CardTitle>{t("rapido.userStatusTitle")}</CardTitle>
          <CardSubtitle>{t("rapido.userStatusDesc")}</CardSubtitle>
          {/* Recharts positions its tooltip and legend with physical left/top
              offsets computed from an LTR box; rendering the plot area LTR
              keeps them under the cursor on a Persian page. The axis content
              is numbers and dates, so nothing here reads right-to-left. */}
          <div dir="ltr" className="mt-4 h-64">
            {!statsLoading && donutData.length > 0 && (
              <ResponsiveContainer width="100%" height="100%">
                <PieChart>
                  <Pie
                    data={donutData}
                    dataKey="value"
                    nameKey="label"
                    innerRadius={60}
                    outerRadius={90}
                    paddingAngle={2}
                  >
                    {donutData.map((d) => (
                      <Cell key={d.status} fill={STATUS_COLORS[d.status]} />
                    ))}
                  </Pie>
                  <Tooltip
                    contentStyle={TOOLTIP_CONTENT_STYLE}
                    itemStyle={TOOLTIP_TEXT_STYLE}
                    labelStyle={TOOLTIP_TEXT_STYLE}
                  />
                </PieChart>
              </ResponsiveContainer>
            )}
          </div>
        </Card>

        <Card className="min-w-0">
          <CardTitle>{t("rapido.usageOverTimeTitle")}</CardTitle>
          <CardSubtitle>{t("rapido.usageOverTimeDesc")}</CardSubtitle>
          <div dir="ltr" className="mt-4 h-64">
            {!historyLoading && history && (
              <ResponsiveContainer width="100%" height="100%">
                <AreaChart data={history}>
                  <defs>
                    <linearGradient id="rapidoUsageFill" x1="0" y1="0" x2="0" y2="1">
                      <stop offset="0%" stopColor="#22d3ee" stopOpacity={0.5} />
                      <stop offset="100%" stopColor="#22d3ee" stopOpacity={0} />
                    </linearGradient>
                  </defs>
                  <CartesianGrid stroke="#223039" strokeDasharray="3 3" />
                  {/* minTickGap: 14 daily labels do not fit across the card on
                      a laptop, and Recharts was drawing them on top of each
                      other ("08-0208-0308-04"). It now drops labels until each
                      one has room, keeping the first and last. */}
                  <XAxis
                    dataKey="date"
                    tick={{ fill: "#7e97a0", fontSize: 12 }}
                    tickFormatter={(d: string) => d.slice(5)}
                    minTickGap={24}
                    interval="preserveStartEnd"
                    tickMargin={8}
                  />
                  {/* One decimal, not zero: rounding to whole units printed the
                      same "1 TB" on two adjacent gridlines. */}
                  <YAxis
                    tick={{ fill: "#7e97a0", fontSize: 12 }}
                    width={72}
                    tickFormatter={(v: number) => String(formatBytes(v, 1))}
                  />
                  <Tooltip
                    contentStyle={TOOLTIP_CONTENT_STYLE}
                    itemStyle={TOOLTIP_TEXT_STYLE}
                    labelStyle={TOOLTIP_TEXT_STYLE}
                    formatter={(v: number) => [
                      String(formatBytes(v)),
                      t("userDialog.usage"),
                    ]}
                  />
                  <Area
                    type="monotone"
                    dataKey="usage"
                    stroke="#22d3ee"
                    fill="url(#rapidoUsageFill)"
                    strokeWidth={2}
                  />
                </AreaChart>
              </ResponsiveContainer>
            )}
          </div>
        </Card>
      </div>

      {isSudo && <FleetHealth />}
    </div>
  );
};

export const OverviewNew: FC = () => <OverviewContent />;

export default OverviewNew;
