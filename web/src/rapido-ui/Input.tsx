import { InputHTMLAttributes, forwardRef } from "react";
import classNames from "classnames";

// Canonical text input. The old dashboard had this class string copied
// (with small drifts) into UsersTable, HostsAdmin, AdminsAdmin,
// InactiveAdmins and Integrations - all five agreeing on `bg-rapido-bg` +
// `focus:ring-1 focus:ring-rapido-accent`, which is what this promotes.
// (Login.tsx and the old UserFormModal instead used `focus:ring-2
// focus:ring-rapido-accent/50` - a two-input minority the majority pattern
// wins over here, a deliberate small visual consolidation, not a port bug.)
export type InputProps = InputHTMLAttributes<HTMLInputElement> & {
  hasError?: boolean;
};

export const Input = forwardRef<HTMLInputElement, InputProps>(
  ({ className, hasError, disabled, ...rest }, ref) => (
    <input
      ref={ref}
      disabled={disabled}
      className={classNames(
        "w-full rounded-lg border bg-rapido-bg px-3 py-2 text-sm text-rapido-text placeholder:text-rapido-muted focus:outline-none focus:ring-1 focus:ring-rapido-accent",
        hasError ? "border-red-500" : "border-rapido-border",
        disabled && "cursor-not-allowed opacity-60",
        className
      )}
      {...rest}
    />
  )
);
Input.displayName = "Input";
