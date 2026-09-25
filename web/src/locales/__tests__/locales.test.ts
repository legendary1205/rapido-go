import { describe, expect, it } from "vitest";
import i18n from "locales/i18n";
import en from "../../../public/statics/locales/en.json";
import fa from "../../../public/statics/locales/fa.json";
import ru from "../../../public/statics/locales/ru.json";
import zh from "../../../public/statics/locales/zh.json";

const locales: Record<string, Record<string, string>> = { en, fa, ru, zh };

// The Core Config, Monitoring, Logs and per-host load namespaces are the ones
// every locale must cover completely. (Other namespaces have known older gaps -
// e.g. ru/zh have no rapido.templates.* - which are not this test's business.)
const NAMESPACES = [
  "rapido.xrayConfig.",
  "rapido.monitoring.",
  "rapido.nodes.",
  "rapido.logs.",
  "rapido.hosts.load",
  "rapido.hosts.varLoad",
];
const PLURAL_SUFFIX = /_(zero|one|two|few|many|other)$/;

const inScope = (key: string) => NAMESPACES.some((ns) => key.startsWith(ns));
const baseKey = (key: string) => key.replace(PLURAL_SUFFIX, "");
const placeholders = (value: string) => [...new Set(value.match(/\{\{\s*\w+\s*\}\}/g) ?? [])].sort();

describe("locale files", () => {
  it("cover every Core Config and Monitoring base key in all four languages", () => {
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

// Single keys the Logs / Overview / Users / Hosts work added outside a namespace.
const V12_KEYS = ["rapido.onlineNowDesc", "rapido.userStatusLegend", "rapido.underAMinute"];

// Left as-is in a translation on purpose: a log level or the word LIVE reads the
// same in that language's UI.
const SAME_AS_ENGLISH_OK = new Set(["ru:rapido.logs.live"]);

describe("logs, load and presence strings", () => {
  it("exist in all four languages", () => {
    for (const [lang, dict] of Object.entries(locales)) {
      for (const key of V12_KEYS) expect(dict[key], `${lang} ${key}`).toBeTruthy();
    }
  });

  it("are actually translated, not English copied into fa, ru and zh", () => {
    const keys = Object.keys(en).filter(
      (k) =>
        k.startsWith("rapido.logs.") ||
        k.startsWith("rapido.hosts.load") ||
        k.startsWith("rapido.hosts.varLoad") ||
        V12_KEYS.includes(k)
    );
    expect(keys.length).toBeGreaterThan(30);
    for (const lang of ["fa", "ru", "zh"]) {
      const dict = locales[lang];
      for (const key of keys) {
        if (SAME_AS_ENGLISH_OK.has(`${lang}:${key}`) || !(key in dict)) continue;
        expect(dict[key], `${lang} ${key}`).not.toBe((en as Record<string, string>)[key]);
      }
    }
  });

  it("fill the tooltip and count strings with real values", () => {
    for (const lang of ["en", "fa", "ru", "zh"]) {
      const t = i18n.getFixedT(lang);
      expect(t("rapido.hosts.loadTooltip", { count: 312, capacity: 1000, percent: 31 })).toMatch(/312.*1000.*31/);
      expect(t("rapido.logs.count", { shown: 7, total: 20 })).toMatch(/7.*20|20.*7/);
      expect(t("rapido.hosts.loadHint", { token: "{LOAD}" })).toContain("{LOAD}");
    }
    expect(i18n.getFixedT("en")("rapido.hosts.loadTooltip", { count: 1, capacity: 1000, percent: 0 })).toBe(
      "1 open connection of 1000 (0%)"
    );
    // Russian needs all four plural forms; 2 and 5 pick different ones.
    expect(i18n.getFixedT("ru")("rapido.hosts.loadTooltip", { count: 2, capacity: 1000, percent: 0 })).toContain("открытых соединения");
    expect(i18n.getFixedT("ru")("rapido.hosts.loadTooltip", { count: 5, capacity: 1000, percent: 0 })).toContain("открытых соединений");
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

// Per-node capacity: the form, the row and the load chip.
const CAPACITY_KEYS = [
  "rapido.nodes.capacity",
  "rapido.nodes.capacityField",
  "rapido.nodes.capacityHint",
  "rapido.nodes.capacityDefault",
  "rapido.nodes.capacityDefaultValue",
  "rapido.nodes.capacityError.notInteger",
  "rapido.nodes.capacityError.outOfRange",
  "rapido.nodes.loadTooltip_other",
  "rapido.nodes.loadDefaultCapacity",
  "rapido.hosts.loadConns_other",
  "rapido.hosts.loadLimitConfig",
  "rapido.hosts.loadLimitNode",
];

describe("per-node capacity strings", () => {
  it("are translated, not English copied into fa, ru and zh", () => {
    for (const lang of ["fa", "ru", "zh"]) {
      for (const key of CAPACITY_KEYS) {
        expect(locales[lang][key], `${lang} ${key}`).toBeTruthy();
        expect(locales[lang][key], `${lang} ${key}`).not.toBe(en[key as keyof typeof en]);
      }
    }
  });

  it("fill the capacity field's messages with the value and the range", () => {
    for (const lang of ["en", "fa", "ru", "zh"]) {
      const t = i18n.getFixedT(lang);
      expect(t("rapido.nodes.capacityError.notInteger", { value: "abc" }), lang).toContain("abc");
      const range = t("rapido.nodes.capacityError.outOfRange", { value: "0", min: 1, max: "10,000,000" });
      expect(range, lang).toContain("0");
      expect(range, lang).toContain("10,000,000");
      expect(t("rapido.nodes.capacityDefaultValue", { value: 10000 }), lang).toContain("10000");
    }
  });

  it("count the connections in the node's tooltip, with the plural forms each language needs", () => {
    const tip = (lang: string, count: number) =>
      i18n.getFixedT(lang)("rapido.nodes.loadTooltip", { count, capacity: 15000, percent: 26 });
    for (const lang of ["en", "fa", "ru", "zh"]) expect(tip(lang, 3960), lang).toMatch(/3960.*15000.*26/);
    expect(tip("en", 1)).toBe("1 client connection of 15000 (26%)");
    expect(tip("en", 3960)).toBe("3960 client connections of 15000 (26%)");
    expect(tip("ru", 1)).toContain("клиентское соединение");
    expect(tip("ru", 2)).toContain("клиентских соединения");
    expect(tip("ru", 5)).toContain("клиентских соединений");
  });

  it("count the connections on a config in the host pill's tooltip, with the same plural forms", () => {
    const conns = (lang: string, count: number) => i18n.getFixedT(lang)("rapido.hosts.loadConns", { count });
    expect(conns("en", 1)).toBe("1 open connection on this config");
    expect(conns("en", 312)).toBe("312 open connections on this config");
    expect(conns("ru", 1)).toContain("открытое соединение");
    expect(conns("ru", 2)).toContain("открытых соединения");
    expect(conns("ru", 5)).toContain("открытых соединений");
    for (const lang of ["fa", "zh"]) expect(conns(lang, 312), lang).toContain("312");
  });

  it("keep the two limit explanations apart in every language", () => {
    for (const lang of ["en", "fa", "ru", "zh"]) {
      const t = i18n.getFixedT(lang);
      const node = t("rapido.hosts.loadLimitNode", { node: 45, port: 3 });
      const config = t("rapido.hosts.loadLimitConfig", { node: 26, port: 72 });
      expect(node, lang).not.toBe(config);
      expect(node, lang).not.toContain("{{");
      expect(config, lang).not.toContain("{{");
    }
  });
});
