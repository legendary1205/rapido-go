import { FC } from "react";
import { useTranslation } from "react-i18next";
import { RapidoShell } from "rapido-ui/Shell";
import { CoreConfigAdmin } from "rapido-ui/CoreConfigAdmin";

export const CoreConfigPage: FC = () => {
  const { t } = useTranslation();

  return (
    <RapidoShell active="coreConfig">
      <div className="p-6">
        <h1 className="mb-1 text-2xl font-bold">{t("rapido.coreConfig.title")}</h1>
        <p className="mb-6 text-sm text-rapido-muted">{t("rapido.coreConfig.subtitle")}</p>
        <CoreConfigAdmin />
      </div>
    </RapidoShell>
  );
};

export default CoreConfigPage;
