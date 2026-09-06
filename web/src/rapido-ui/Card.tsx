import { FC, HTMLAttributes, PropsWithChildren } from "react";
import classNames from "classnames";

export const Card: FC<PropsWithChildren<HTMLAttributes<HTMLDivElement>>> = ({
  className,
  children,
  ...rest
}) => (
  <div
    className={classNames(
      "rounded-xl2 border border-rapido-border bg-rapido-surface p-5",
      className
    )}
    {...rest}
  >
    {children}
  </div>
);

export const CardTitle: FC<PropsWithChildren<HTMLAttributes<HTMLDivElement>>> = ({
  className,
  children,
  ...rest
}) => (
  <div
    className={classNames("text-sm font-semibold text-rapido-text", className)}
    {...rest}
  >
    {children}
  </div>
);

export const CardSubtitle: FC<PropsWithChildren<HTMLAttributes<HTMLDivElement>>> = ({
  className,
  children,
  ...rest
}) => (
  <div className={classNames("text-xs text-rapido-muted", className)} {...rest}>
    {children}
  </div>
);
