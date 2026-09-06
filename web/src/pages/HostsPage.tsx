import { FC } from "react";
import { useTranslation } from "react-i18next";
import { RapidoShell } from "rapido-ui/Shell";
import { HostsAdmin } from "rapido-ui/HostsAdmin";

export const HostsPage: FC = () => {
  const { t } = useTranslation();

  return (
    <RapidoShell active="hosts">
      <div className="p-6">
        <h1 className="mb-1 text-2xl font-bold">{t("rapido.hosts.title")}</h1>
        <p className="mb-6 text-sm text-rapido-muted">{t("rapido.hosts.subtitle")}</p>
        <HostsAdmin />
      </div>
    </RapidoShell>
  );
};

export default HostsPage;
