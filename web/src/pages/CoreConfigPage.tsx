import { FC } from "react";
import { useTranslation } from "react-i18next";
import { RapidoShell } from "rapido-ui/Shell";
import { XrayConfigAdmin } from "rapido-ui/XrayConfigAdmin";

// Formerly two separate pages (Core Config, Inbounds) - merged into one,
// see rapido-ui/XrayConfigAdmin.tsx's own doc comment. The route/nav key
// stays "coreConfig"/"/core-config/" rather than being renamed: an internal
// admin URL, not worth the churn of a rename for its own sake.
export const CoreConfigPage: FC = () => {
  const { t } = useTranslation();

  return (
    <RapidoShell active="coreConfig">
      <div className="p-6">
        <h1 className="mb-1 text-2xl font-bold">{t("rapido.xrayConfig.title")}</h1>
        <p className="mb-6 text-sm text-rapido-muted">{t("rapido.xrayConfig.subtitle")}</p>
        <XrayConfigAdmin />
      </div>
    </RapidoShell>
  );
};

export default CoreConfigPage;
