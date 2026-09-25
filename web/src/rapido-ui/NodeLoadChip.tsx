import { FC } from "react";
import { useTranslation } from "react-i18next";
import { NodeLoadEntry } from "types/HostLoad";
import { clampPercent, levelForPercent } from "utils/hostLoad";
import { LoadPill } from "rapido-ui/LoadPill";

/**
 * One node's whole load - every open client connection on it, whichever config
 * they came in through - as the same pill a host gets. The tooltip has the
 * numbers behind it, and says so when the 100% is the panel default rather than
 * something set on this node, since that is the one thing an admin can change.
 */
export const NodeLoadChip: FC<{ load: NodeLoadEntry; className?: string }> = ({ load, className }) => {
  const { t } = useTranslation();
  // A percent that is not a number is a node we know nothing usable about;
  // an empty spot beats a "NaN%" or a colour that claims more than it knows.
  if (!Number.isFinite(load.percent)) return null;
  const percent = clampPercent(load.percent);

  const tooltip = [
    t("rapido.nodes.loadTooltip", { count: load.conns, capacity: load.capacity, percent }),
    ...(load.capacity_source === "default" ? [t("rapido.nodes.loadDefaultCapacity")] : []),
  ].join(" · ");

  return <LoadPill level={levelForPercent(percent)} percent={percent} title={tooltip} className={className} />;
};

export default NodeLoadChip;
