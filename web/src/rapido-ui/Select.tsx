import { SelectHTMLAttributes, forwardRef } from "react";
import classNames from "classnames";

// Canonical <select>, same palette/focus ring as Input.tsx. `max-w-full` is
// load-bearing, not decoration: a <select> sizes itself by its widest
// <option>, and the Persian sort-order labels on the Users page are long
// enough to push an unconstrained one past a 390px screen (see the old
// UsersTable.tsx's own comment on this).
export type SelectProps = SelectHTMLAttributes<HTMLSelectElement>;

export const Select = forwardRef<HTMLSelectElement, SelectProps>(
  ({ className, children, ...rest }, ref) => (
    <select
      ref={ref}
      className={classNames(
        "max-w-full rounded-lg border border-rapido-border bg-rapido-bg px-3 py-2 text-sm text-rapido-text focus:outline-none focus:ring-1 focus:ring-rapido-accent",
        className
      )}
      {...rest}
    >
      {children}
    </select>
  )
);
Select.displayName = "Select";
