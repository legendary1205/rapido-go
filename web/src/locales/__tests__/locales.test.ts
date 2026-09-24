import { describe, expect, it } from "vitest";
import i18n from "locales/i18n";
import en from "../../../public/statics/locales/en.json";
import fa from "../../../public/statics/locales/fa.json";
import ru from "../../../public/statics/locales/ru.json";
import zh from "../../../public/statics/locales/zh.json";

const locales: Record<string, Record<string, string>> = { en, fa, ru, zh };

// The Xray Config and Monitoring namespaces are the ones every locale must
// cover completely. (Other namespaces have known older gaps - e.g. ru/zh have
// no rapido.templates.* - which are not this test's business.)
const NAMESPACES = ["rapido.xrayConfig.", "rapido.monitoring."];
const PLURAL_SUFFIX = /_(zero|one|two|few|many|other)$/;

const inScope = (key: string) => NAMESPACES.some((ns) => key.startsWith(ns));
const baseKey = (key: string) => key.replace(PLURAL_SUFFIX, "");
const placeholders = (value: string) => [...new Set(value.match(/\{\{\s*\w+\s*\}\}/g) ?? [])].sort();

describe("locale files", () => {
  it("cover every Xray Config and Monitoring base key in all four languages", () => {
    const bases = new Set(Object.keys(en).filter(inScope).map(baseKey));
    expect(bases.size).toBeGreaterThan(0);
    for (const [lang, dict] of Object.entries(locales)) {
      const have = new Set(Object.keys(dict).filter(inScope).map(baseKey));
      const missing = [...bases].filter((k) => !have.has(k));
      expect(missing, `${lang} is missing`).toEqual([]);
    }
  });

  it("carry every plural form the language needs for the plural keys", () => {
    const plural = [...new Set(Object.keys(en).filter((k) => inScope(k) && PLURAL_SUFFIX.test(k)).map(baseKey))];
    expect(plural.length).toBeGreaterThan(0);
    for (const [lang, dict] of Object.entries(locales)) {
      const forms = new Intl.PluralRules(lang).resolvedOptions().pluralCategories;
      for (const key of plural) {
        for (const form of forms) expect(dict[`${key}_${form}`], `${lang} ${key}_${form}`).toBeTruthy();
      }
    }
  });

  it("use the same {{placeholders}} as English in every translated string", () => {
    for (const [lang, dict] of Object.entries(locales)) {
      if (lang === "en") continue;
      for (const key of Object.keys(en).filter(inScope)) {
        if (!(key in dict)) continue; // plural forms a language doesn't have
        expect(placeholders(dict[key]), `${lang} ${key}`).toEqual(placeholders(en[key as keyof typeof en]));
      }
    }
  });
});

describe("ports count plural", () => {
  const cases: Array<[string, number, string]> = [
    ["en", 15, "(15 ports)"],
    ["ru", 2, "(2 порта)"],
    ["ru", 5, "(5 портов)"],
    ["ru", 21, "(21 порт)"],
    ["zh", 15, "(15 个端口)"],
    ["fa", 15, "(15 پورت)"],
  ];
  it.each(cases)("%s with %i", (lang, count, expected) => {
    expect(i18n.getFixedT(lang)("rapido.xrayConfig.portsCount", { count })).toBe(expected);
  });
});

describe("tunnel strings", () => {
  it("say the same four things in every language, with the value substituted", () => {
    for (const lang of ["en", "fa", "ru", "zh"]) {
      const t = i18n.getFixedT(lang);
      expect(t("rapido.monitoring.agoMinutes", { value: 3 })).toContain("3");
      expect(t("rapido.monitoring.tunnelsNotUpTitle", { down: 2, total: 5 })).toMatch(/2.*5/);
    }
    expect(i18n.getFixedT("en")("rapido.monitoring.tunnelDownFallback")).toBe("Down - using direct fallback");
    expect(i18n.getFixedT("en")("rapido.monitoring.agoSeconds", { value: 12 })).toBe("12 s ago");
    expect(i18n.getFixedT("en")("rapido.monitoring.agoMinutes", { value: 3 })).toBe("3 min ago");
  });
});
