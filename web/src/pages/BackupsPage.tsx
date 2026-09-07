import { FC } from "react";
import { useTranslation } from "react-i18next";
import { RapidoShell } from "rapido-ui/Shell";
import { BackupsAdmin } from "rapido-ui/BackupsAdmin";

export const BackupsPage: FC = () => {
  const { t } = useTranslation();

  return (
    <RapidoShell active="backups">
      <div className="p-6">
        <h1 className="mb-1 text-2xl font-bold">{t("rapido.backups.pageTitle")}</h1>
        <p className="mb-6 text-sm text-rapido-muted">{t("rapido.backups.pageDesc")}</p>
        <BackupsAdmin />
      </div>
    </RapidoShell>
  );
};

export default BackupsPage;
