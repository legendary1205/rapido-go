import { FC } from "react";
import { useTranslation } from "react-i18next";
import { RapidoShell } from "rapido-ui/Shell";
import { AdminsAdmin } from "rapido-ui/AdminsAdmin";
import { InactiveAdmins } from "rapido-ui/InactiveAdmins";

export const AdminsPage: FC = () => {
  const { t } = useTranslation();

  return (
    <RapidoShell active="admins">
      <div className="flex flex-col gap-6 p-6">
        <div>
          <h1 className="mb-1 text-2xl font-bold">{t("rapido.admins.title")}</h1>
          <p className="text-sm text-rapido-muted">{t("rapido.admins.subtitle")}</p>
        </div>
        <AdminsAdmin />
        <InactiveAdmins />
      </div>
    </RapidoShell>
  );
};

export default AdminsPage;
