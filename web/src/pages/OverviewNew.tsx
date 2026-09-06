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
import { useSystemStatsQuery } from "hooks/useSystemStatsQuery";
import { useSystemUsageHistoryQuery } from "hooks/useSystemUsageHistoryQuery";
import { formatBytes, numberWithCommas } from "utils/formatByte";
import { Card, CardSubtitle, CardTitle } from "rapido-ui/Card";
import { PulseDot } from "rapido-ui/Badge";
import "rapido-ui/tailwind.css";

// `FleetStatus`/`FleetHealth` (the two node-monitoring sections) and this
// page's own private QueryClientProvider are the only things removed versus
// the old OverviewNew.tsx - both needed GET /monitoring, which needs a
// node-reporting phase that hasn't landed yet (see the plan's context). The
// private QueryClientProvider going away is also a real fix, not just a
// simplification: it was its own cache that a write from the Users page
// could never invalidate, so Overview's numbers only ever updated on its own
// 10s poll instead of immediately after a create/delete.
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

const OverviewContent: FC = () => {
  const { t } = useTranslation();

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

      <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
        <HeroStat
          label={t("rapido.onlineNow")}
          value={statsLoading ? "…" : numberWithCommas(stats?.online_users ?? 0) ?? "0"}
          sub={t("rapido.activeLast24h")}
        />
        <StatCard
          label={t("rapido.totalUsers")}
          value={statsLoading ? "…" : numberWithCommas(stats?.total_user ?? 0) ?? "0"}
        />
        <StatCard
          label={t("status.active")}
          value={statsLoading ? "…" : numberWithCommas(stats?.users_active ?? 0) ?? "0"}
        />
      </div>

      <div className="mt-4 grid grid-cols-1 gap-4 sm:grid-cols-3">
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
    </div>
  );
};

export const OverviewNew: FC = () => <OverviewContent />;

export default OverviewNew;
