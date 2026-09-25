import { FC } from "react";
import { RapidoShell } from "rapido-ui/Shell";
import { Logs } from "rapido-ui/Logs";

export const LogsPage: FC = () => (
  <RapidoShell active="logs">
    <div className="p-6">
      <Logs />
    </div>
  </RapidoShell>
);

export default LogsPage;
