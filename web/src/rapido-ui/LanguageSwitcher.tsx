import { FC, useState } from "react";
import { useTranslation } from "react-i18next";
import classNames from "classnames";
import { baseLanguage } from "utils/language";

const LANGUAGES: { code: string; label: string }[] = [
  { code: "en", label: "English" },
  { code: "fa", label: "فارسی" },
  { code: "zh", label: "中文" },
  { code: "ru", label: "Русский" },
];

export type LanguageSwitcherProps = {
  /** Where the menu is placed relative to the trigger button. */
  openDirection?: "up" | "down";
  /**
   * Which inline edge of the trigger the menu hangs from. The sidebar trigger
   * spans the whole rail so either edge works, but the header trigger sits
   * against the viewport's inline end - anchoring that one to `start` pushes a
   * 9rem menu off-screen and gives the page a horizontal scrollbar.
   */
  align?: "start" | "end";
};

export const LanguageSwitcher: FC<LanguageSwitcherProps> = ({
  openDirection = "down",
  align = "start",
}) => {
  const { t, i18n } = useTranslation();
  const [open, setOpen] = useState(false);
  // Matched on the base language: the detector reports "fa-IR" for most
  // Iranian browsers, which would otherwise fall through to English here and
  // tell the admin they are viewing a language they are not.
  const current =
    LANGUAGES.find((l) => l.code === baseLanguage(i18n.language)) ??
    LANGUAGES[0];

  return (
    <div className="relative">
      <button
        type="button"
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-label={t("rapido.language")}
        onClick={() => setOpen((o) => !o)}
        // Closing on blur must be deferred, otherwise the menu unmounts before
        // the click on one of its items is dispatched.
        onBlur={() => setTimeout(() => setOpen(false), 150)}
        className="flex w-full max-w-full items-center gap-2 rounded-lg px-3 py-2 text-start text-sm font-medium text-rapido-muted transition-colors hover:bg-rapido-raised hover:text-rapido-text"
      >
        <span aria-hidden="true">🌐</span>
        <span className="truncate">{current.label}</span>
      </button>
      {open && (
        <div
          role="listbox"
          className={classNames(
            "absolute z-50 min-w-[9rem] max-w-[calc(100vw-2rem)] overflow-hidden rounded-lg border border-rapido-border bg-rapido-surface shadow-lg",
            align === "end" ? "end-0" : "start-0",
            openDirection === "up" ? "bottom-full mb-1" : "top-full mt-1"
          )}
        >
          {LANGUAGES.map((lang) => (
            <button
              key={lang.code}
              type="button"
              role="option"
              aria-selected={lang.code === current.code}
              onClick={() => {
                i18n.changeLanguage(lang.code);
                setOpen(false);
              }}
              className={classNames(
                "block w-full px-3 py-2 text-start text-sm hover:bg-rapido-raised",
                lang.code === current.code
                  ? "text-rapido-accent"
                  : "text-rapido-muted hover:text-rapido-text"
              )}
            >
              {lang.label}
            </button>
          ))}
        </div>
      )}
    </div>
  );
};
