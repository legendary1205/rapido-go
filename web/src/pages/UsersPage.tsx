import { FC } from "react";
import { useTranslation } from "react-i18next";
import { RapidoShell } from "rapido-ui/Shell";
import { UsersTable } from "rapido-ui/UsersTable";
import { UserFormModal } from "rapido-ui/UserFormModal";
import { UserActionModals } from "rapido-ui/UserActionModals";

export const UsersPage: FC = () => {
  const { t } = useTranslation();

  return (
    <RapidoShell active="users">
      <div className="p-4 sm:p-6">
        <h1 className="mb-6 text-2xl font-bold">{t("users")}</h1>
        <UsersTable />
      </div>
      <UserFormModal />
      <UserActionModals />
    </RapidoShell>
  );
};

export default UsersPage;
