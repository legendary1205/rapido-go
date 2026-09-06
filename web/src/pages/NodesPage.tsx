import { FC } from "react";
import { useTranslation } from "react-i18next";
import { RapidoShell } from "rapido-ui/Shell";
import { NodesAdmin } from "rapido-ui/NodesAdmin";

export const NodesPage: FC = () => {
  const { t } = useTranslation();

  return (
    <RapidoShell active="nodes">
      <div className="p-6">
        <h1 className="mb-1 text-2xl font-bold">{t("rapido.nodes.title")}</h1>
        <p className="mb-6 text-sm text-rapido-muted">{t("nodes.title")}</p>
        <NodesAdmin />
      </div>
    </RapidoShell>
  );
};

export default NodesPage;
