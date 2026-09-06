import { FC } from "react";
import { useTranslation } from "react-i18next";
import { RapidoShell } from "rapido-ui/Shell";
import { IntegrationsForm } from "rapido-ui/IntegrationsForm";

export const IntegrationsPage: FC = () => {
  const { t } = useTranslation();

  return (
    <RapidoShell active="integrations">
      <div className="p-6">
        <h1 className="mb-1 text-2xl font-bold">{t("rapido.integrations.title")}</h1>
        <p className="mb-6 text-sm text-rapido-muted">{t("rapido.integrations.subtitle")}</p>
        <IntegrationsForm />
      </div>
    </RapidoShell>
  );
};

export default IntegrationsPage;
