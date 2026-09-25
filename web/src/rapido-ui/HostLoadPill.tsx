import { FC } from "react";
import classNames from "classnames";
import { useTranslation } from "react-i18next";
import { HostLoadEntry, HostLoadLevel } from "types/HostLoad";

// Whole class strings, because Tailwind only emits classes it finds literally.
// The hues are the ones the subscription remark uses for the same levels
// (green / yellow / orange / red), so the dashboard and the customer's app
// agree on what a colour means.
const LEVEL_STYLE: Record<HostLoadLevel, { pill: string; fill: string; dot: string; labelKey: string }> = {
  free: {
    pill: "border-emerald-500/30 bg-emerald-500/10 text-emerald-400",
    fill: "bg-emerald-500/25",
    dot: "bg-emerald-400",
    labelKey: "rapido.hosts.loadFree",
  },
  normal: {
    pill: "border-yellow-500/30 bg-yellow-500/10 text-yellow-400",
    fill: "bg-yellow-500/25",
    dot: "bg-yellow-400",
    labelKey: "rapido.hosts.loadNormal",
  },
  busy: {
    pill: "border-orange-500/30 bg-orange-500/10 text-orange-400",
    fill: "bg-orange-500/25",
    dot: "bg-orange-400",
    labelKey: "rapido.hosts.loadBusy",
  },
  full: {
    pill: "border-red-500/30 bg-red-500/10 text-red-400",
    fill: "bg-red-500/25",
    dot: "bg-red-400",
    labelKey: "rapido.hosts.loadFull",
  },
  unknown: {
    pill: "border-rapido-border bg-rapido-raised text-rapido-muted",
    fill: "bg-transparent",
    dot: "bg-rapido-muted",
    labelKey: "rapido.hosts.loadUnknown",
  },
};

/**
 * One host's live load as a rounded pill: a level-coloured dot, the percent and
 * the level word, over a soft fill that grows with the percent (start to end,
 * so it follows the page direction). The open-connection count is in the
 * tooltip - the pill itself stays small enough to sit beside the inbound tag.
 * The word matters as much as the colour: green vs orange is not a distinction
 * everyone can make.
 */
export const HostLoadPill: FC<{ load: HostLoadEntry; capacity: number; className?: string }> = ({
  load,
  capacity,
  className,
}) => {
  const { t } = useTranslation();
  // A level this build does not know (the backend ahead of the frontend), or
  // a percent that is not a number, is drawn as a neutral "no data" pill
  // instead of crashing on a missing style or claiming a colour it cannot back.
  const level = LEVEL_STYLE[load.level] && load.level !== "unknown" && Number.isFinite(load.percent) ? load.level : "unknown";
  const known = level !== "unknown";
  const style = LEVEL_STYLE[level];
  const percent = known ? Math.min(100, Math.max(0, load.percent)) : 0;

  const tooltip = known
    ? t("rapido.hosts.loadTooltip", { count: load.conns, capacity, percent })
    : t("rapido.hosts.loadTooltipUnknown");

  return (
    <span
      title={tooltip}
      data-level={level}
      className={classNames(
        "relative inline-flex shrink-0 items-center gap-1.5 overflow-hidden rounded-full border px-2.5 py-1 text-xs font-medium",
        style.pill,
        className
      )}
    >
      <span
        aria-hidden="true"
        className={classNames("absolute inset-y-0 start-0", style.fill)}
        style={{ width: `${percent}%` }}
      />
      <span aria-hidden="true" className={classNames("relative h-1.5 w-1.5 shrink-0 rounded-full", style.dot)} />
      <span className="relative tabular-nums" dir="ltr">
        {known ? `${Math.round(percent)}%` : "—"}
      </span>
      <span className="relative">{t(style.labelKey)}</span>
    </span>
  );
};

export default HostLoadPill;
