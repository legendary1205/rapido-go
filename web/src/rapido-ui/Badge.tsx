import { FC, HTMLAttributes, PropsWithChildren } from "react";
import classNames from "classnames";

export type BadgeTone =
  | "green"
  | "sky"
  | "yellow"
  | "orange"
  | "red"
  // Named for what it renders, not what it once did - this tone is the
  // brand accent (cyan in the control-room palette), not literally purple.
  | "brand"
  | "gray";

const toneClasses: Record<BadgeTone, string> = {
  green: "bg-emerald-500/15 text-emerald-400",
  sky: "bg-sky-500/15 text-sky-400",
  yellow: "bg-yellow-500/15 text-yellow-400",
  orange: "bg-orange-500/15 text-orange-400",
  red: "bg-red-500/15 text-red-400",
  brand: "bg-rapido-accent/15 text-rapido-accent",
  gray: "bg-rapido-raised text-rapido-muted",
};

export const Badge: FC<
  PropsWithChildren<HTMLAttributes<HTMLSpanElement> & { tone?: BadgeTone }>
> = ({ tone = "gray", className, children, ...rest }) => (
  <span
    className={classNames(
      "inline-flex items-center gap-1 rounded-full px-2.5 py-1 text-xs font-medium",
      toneClasses[tone],
      className
    )}
    {...rest}
  >
    {children}
  </span>
);

const dotClasses: Record<BadgeTone, string> = {
  green: "bg-emerald-400",
  sky: "bg-sky-400",
  yellow: "bg-yellow-400",
  orange: "bg-orange-400",
  red: "bg-red-400",
  brand: "bg-rapido-accent",
  gray: "bg-rapido-muted",
};

/** A status dot - solid, with a soft expanding ring behind it while `live`.
 * The ring is what a glance actually catches; a static dot alone reads as
 * decoration, not as "this is happening right now". */
export const PulseDot: FC<{ tone?: BadgeTone; live?: boolean; className?: string }> = ({
  tone = "gray",
  live = true,
  className,
}) => (
  <span className={classNames("relative inline-flex h-2 w-2 shrink-0", className)}>
    {live && (
      <span
        className={classNames(
          "absolute inline-flex h-full w-full animate-ping rounded-full opacity-60",
          dotClasses[tone]
        )}
      />
    )}
    <span className={classNames("relative inline-flex h-2 w-2 rounded-full", dotClasses[tone])} />
  </span>
);
