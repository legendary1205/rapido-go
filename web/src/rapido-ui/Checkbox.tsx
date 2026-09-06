import { InputHTMLAttributes, ReactNode, forwardRef } from "react";
import classNames from "classnames";

// Canonical checkbox, promoted from the old UserFormModal.tsx's
// `checkboxClass`. Renders bare (no wrapping <label>) when no `label` is
// given, so call sites that need custom label markup (the protocol/inbound
// picker's "unavailable on this server" suffix, for one) aren't forced into
// this component's own layout.
export type CheckboxProps = InputHTMLAttributes<HTMLInputElement> & {
  label?: ReactNode;
};

export const Checkbox = forwardRef<HTMLInputElement, CheckboxProps>(
  ({ className, label, ...rest }, ref) => {
    const box = (
      <input
        ref={ref}
        type="checkbox"
        className={classNames(
          "h-4 w-4 rounded border-rapido-border bg-rapido-bg accent-rapido-accent",
          className
        )}
        {...rest}
      />
    );
    if (label === undefined) return box;
    return (
      <label className="flex items-center gap-2 text-sm text-rapido-text">
        {box}
        <span>{label}</span>
      </label>
    );
  }
);
Checkbox.displayName = "Checkbox";
