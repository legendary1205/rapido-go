import { joinPaths } from "@remix-run/router";

// Every translation is bundled into the app instead of being fetched at
// runtime. Loading them over HTTP failed in a way that was almost impossible
// to diagnose from the outside: a blocked or truncated response still arrives
// as a 200, JSON.parse then fails with nothing logged, and the whole panel
// renders raw keys ("rapido.tickets.title") with no error shown anywhere.
// On the connections these admins actually have, that is not a rare event -
// it happened twice while testing. ~20 KB gzipped removes the failure mode
// outright, and takes the cache-invalidation problem with it.
import enBundled from "../../public/statics/locales/en.json";
import faBundled from "../../public/statics/locales/fa.json";
import ruBundled from "../../public/statics/locales/ru.json";
import zhBundled from "../../public/statics/locales/zh.json";

import dayjs from "dayjs";
import i18n from "i18next";
import LanguageDetector from "i18next-browser-languagedetector";
import HttpApi from "i18next-http-backend";
import { initReactI18next } from "react-i18next";

declare module "i18next" {
    interface CustomTypeOptions {
        returnNull: false;
    }
}

i18n
    .use(LanguageDetector)
    .use(initReactI18next)
    .use(HttpApi)
    .init(
        {
            debug: import.meta.env.NODE_ENV === "development",
            returnNull: false,
            fallbackLng: "en",
            resources: {
                en: { translation: enBundled },
                fa: { translation: faBundled },
                ru: { translation: ruBundled },
                zh: { translation: zhBundled },
            },
            // Tells i18next these resources may not cover everything, so the
            // HTTP backend below still serves anything that isn't bundled
            // (a language added to the server later). Nothing that IS bundled
            // is ever re-fetched.
            partialBundledLanguages: true,
            interpolation: {
                escapeValue: false,
            },
            react: {
                useSuspense: false,
            },
            load: "languageOnly",
            detection: {
                caches: ["localStorage", "sessionStorage", "cookie"],
            },
            backend: {
                loadPath:
                    joinPaths([
                        import.meta.env.BASE_URL,
                        `statics/locales/{{lng}}.json`,
                    ]) + `?v=${__LOCALE_VERSION__}`,
            },
        },
        function (err, t) {
            dayjs.locale(i18n.language);
        }
    );

i18n.on("languageChanged", (lng) => {
    dayjs.locale(lng);
});

// DataPicker
// The date-fns locales were only ever registered with react-datepicker,
// which left with the Chakra dashboard.

export default i18n;