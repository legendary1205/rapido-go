import { FC } from "react";
import { useTranslation } from "react-i18next";
import { RapidoShell } from "rapido-ui/Shell";
import { TicketsAdmin } from "rapido-ui/TicketsAdmin";

export const TicketsPage: FC = () => {
  const { t } = useTranslation();

  return (
    <RapidoShell active="tickets">
      <div className="flex flex-col gap-6 p-6">
        <div>
          <h1 className="mb-1 text-2xl font-bold">{t("rapido.tickets.title")}</h1>
          <p className="text-sm text-rapido-muted">{t("rapido.tickets.subtitle")}</p>
        </div>
        <TicketsAdmin />
      </div>
    </RapidoShell>
  );
};

export default TicketsPage;
