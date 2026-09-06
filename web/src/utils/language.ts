/**
 * The language detector reports whatever the browser sends, which for an
 * Iranian admin is normally "fa-IR" rather than "fa". Translations still work
 * (i18next is configured with load: "languageOnly", so fa-IR resolves to the
 * fa bundle), but anything that compares the raw string takes the wrong
 * branch: the page stays left-to-right while showing Persian, and the language
 * menu claims the current language is English. Both were live bugs.
 *
 * Always compare against the base language, never i18n.language directly.
 */
export const baseLanguage = (language: string): string =>
  (language || "").split("-")[0].toLowerCase();

/** Persian is the only right-to-left language this panel ships. */
export const isRtl = (language: string): boolean =>
  baseLanguage(language) === "fa";

export const dirOf = (language: string): "rtl" | "ltr" =>
  isRtl(language) ? "rtl" : "ltr";
