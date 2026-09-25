import { FC } from "react";
import classNames from "classnames";
import { useTranslation } from "react-i18next";
import { HostLoadLevel } from "types/HostLoad";

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
 * The shared look of every load indicator: a rounded pill with a level-coloured
 * dot, the percent and the level word, over a soft fill that grows with the
 * percent (start to end, so it follows the page direction). What the number
 * counts - a config's connections, a whole node's - goes in `title`, so the
 * pill itself stays small enough to sit beside a tag or a name. The word
 * matters as much as the colour: green vs orange is not a distinction everyone
 * can make.
 *
 * The caller decides the level; "unknown" is drawn as a neutral "no data" pill
 * with a dash instead of a percent it cannot back.
 */
export const LoadPill: FC<{
  level: HostLoadLevel;
  /** 0-100; ignored when the level is unknown. */
  percent: number;
  title: string;
  className?: string;
}> = ({ level, percent, title, className }) => {
  const { t } = useTranslation();
  const known = level !== "unknown" && !!LEVEL_STYLE[level];
  const style = LEVEL_STYLE[known ? level : "unknown"];
  const width = known ? Math.min(100, Math.max(0, percent)) : 0;

  return (
    <span
      title={title}
      data-level={known ? level : "unknown"}
      className={classNames(
        "relative inline-flex shrink-0 items-center gap-1.5 overflow-hidden rounded-full border px-2.5 py-1 text-xs font-medium",
        style.pill,
        className
      )}
    >
      <span
        aria-hidden="true"
        className={classNames("absolute inset-y-0 start-0", style.fill)}
        style={{ width: `${width}%` }}
      />
      <span aria-hidden="true" className={classNames("relative h-1.5 w-1.5 shrink-0 rounded-full", style.dot)} />
      <span className="relative tabular-nums" dir="ltr">
        {known ? `${Math.round(width)}%` : "—"}
      </span>
      <span className="relative">{t(style.labelKey)}</span>
    </span>
  );
};

export default LoadPill;
