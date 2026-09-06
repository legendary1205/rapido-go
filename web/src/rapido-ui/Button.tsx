import { ButtonHTMLAttributes, forwardRef } from "react";
import classNames from "classnames";
import { btnBase, tones, ButtonTone } from "./buttonStyles";

// Promotes the three button looks that were each copied class-string by
// class-string across the old dashboard:
//  - "chip": the small bordered/tinted button from buttonStyles.ts's
//    btnBase+tones, used all over Hosts/Admins/InactiveAdmins/Integrations.
//  - "primary": the filled accent button (Login's submit, UsersTable's
//    "+ New user", UserFormModal's submit).
//  - "secondary": the bordered neutral button (every modal's Cancel,
//    UserActionModals.tsx's SecondaryButton).
export type ButtonVariant = "chip" | "primary" | "secondary";

export type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> & {
  variant?: ButtonVariant;
  /** Only meaningful for variant="chip" - picks a buttonStyles.ts tone. */
  tone?: ButtonTone;
};

const primaryClass =
  "rounded-lg bg-rapido-accent px-4 py-2 text-sm font-medium text-white transition-opacity hover:opacity-90 disabled:cursor-not-allowed disabled:opacity-50";

const secondaryClass =
  "rounded-lg border border-rapido-border px-3 py-2 text-sm text-rapido-text transition-colors hover:bg-rapido-raised disabled:cursor-not-allowed disabled:opacity-50";

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(
  ({ variant = "secondary", tone = "neutral", className, type = "button", ...rest }, ref) => {
    const variantClass =
      variant === "chip"
        ? classNames(btnBase, tones[tone])
        : variant === "primary"
        ? primaryClass
        : secondaryClass;

    return (
      <button
        ref={ref}
        type={type}
        className={classNames(variantClass, className)}
        {...rest}
      />
    );
  }
);
Button.displayName = "Button";
