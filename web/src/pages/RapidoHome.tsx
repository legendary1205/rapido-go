import { FC } from "react";
import { RapidoShell } from "rapido-ui/Shell";
import { OverviewNew } from "./OverviewNew";

export const RapidoHome: FC = () => (
  <RapidoShell active="overview">
    <OverviewNew />
  </RapidoShell>
);

export default RapidoHome;
