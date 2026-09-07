import { FC } from "react";
import { useTranslation } from "react-i18next";
import { RapidoShell } from "rapido-ui/Shell";
import { InboundsAdmin } from "rapido-ui/InboundsAdmin";

export const InboundsPage: FC = () => {
  const { t } = useTranslation();

  return (
    <RapidoShell active="inbounds">
      <div className="p-6">
        <h1 className="mb-1 text-2xl font-bold">{t("rapido.inbounds.title")}</h1>
        <p className="mb-6 text-sm text-rapido-muted">{t("rapido.inbounds.subtitle")}</p>
        <InboundsAdmin />
      </div>
    </RapidoShell>
  );
};

export default InboundsPage;
