import { FC } from "react";
import { useTranslation } from "react-i18next";
import { RapidoShell } from "rapido-ui/Shell";
import { GatewayAdmin } from "rapido-ui/GatewayAdmin";

export const GatewayPage: FC = () => {
  const { t } = useTranslation();

  return (
    <RapidoShell active="gateway">
      <div className="p-6">
        <h1 className="mb-1 text-2xl font-bold">{t("rapido.gateway.pageTitle")}</h1>
        <p className="mb-6 text-sm text-rapido-muted">{t("rapido.gateway.pageDesc")}</p>
        <GatewayAdmin />
      </div>
    </RapidoShell>
  );
};

export default GatewayPage;
