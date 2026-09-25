import { FC } from "react";
import { useTranslation } from "react-i18next";
import { HostLoadEntry, HostLoadLevel } from "types/HostLoad";
import { hostLimit } from "utils/hostLoad";
import { LoadPill } from "rapido-ui/LoadPill";

/**
 * One host's live load as a pill (see LoadPill for the look). The tooltip has
 * what the pill cannot fit: how many connections are on this config and which
 * of its two limits the percent is held to - its own connections, or its
 * node's whole load, which every config on that node shares. A panel that does
 * not send the two numbers gets the plain "N of capacity" tooltip it always had.
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
  const known = (["free", "normal", "busy", "full"] as HostLoadLevel[]).includes(load.level) && Number.isFinite(load.percent);
  const percent = known ? Math.min(100, Math.max(0, load.percent)) : 0;
  const limit = known ? hostLimit(load) : null;

  let tooltip: string;
  if (!known) {
    tooltip = t("rapido.hosts.loadTooltipUnknown");
  } else if (limit) {
    tooltip = [
      t("rapido.hosts.loadConns", { count: load.conns }),
      t(limit.by === "node" ? "rapido.hosts.loadLimitNode" : "rapido.hosts.loadLimitConfig", {
        node: limit.node,
        port: limit.port,
      }),
    ].join("\n");
  } else {
    tooltip = t("rapido.hosts.loadTooltip", { count: load.conns, capacity, percent });
  }

  return <LoadPill level={known ? load.level : "unknown"} percent={percent} title={tooltip} className={className} />;
};

export default HostLoadPill;
