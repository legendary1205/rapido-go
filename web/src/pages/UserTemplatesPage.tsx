import { FC } from "react";
import { useTranslation } from "react-i18next";
import { RapidoShell } from "rapido-ui/Shell";
import { UserTemplatesAdmin } from "rapido-ui/UserTemplatesAdmin";

export const UserTemplatesPage: FC = () => {
  const { t } = useTranslation();

  return (
    <RapidoShell active="templates">
      <div className="p-6">
        <h1 className="mb-1 text-2xl font-bold">{t("rapido.templates.title")}</h1>
        <p className="mb-6 text-sm text-rapido-muted">{t("rapido.templates.subtitle")}</p>
        <UserTemplatesAdmin />
      </div>
    </RapidoShell>
  );
};

export default UserTemplatesPage;
