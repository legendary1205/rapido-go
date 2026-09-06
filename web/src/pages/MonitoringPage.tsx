import { FC } from "react";
import { RapidoShell } from "rapido-ui/Shell";
import { Monitoring } from "rapido-ui/Monitoring";

export const MonitoringPage: FC = () => (
  <RapidoShell active="monitoring">
    <div className="p-6">
      <Monitoring />
    </div>
  </RapidoShell>
);

export default MonitoringPage;
