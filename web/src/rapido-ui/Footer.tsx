import { FC } from "react";
import { ORGANIZATION_URL, REPO_URL } from "constants/Project";

export const RapidoFooter: FC = () => (
  // Entirely Latin and punctuation-heavy; left in the surrounding RTL run the
  // bidi algorithm moves the comma to the wrong side of "Rapido".
  <div dir="ltr" className="w-full py-2 text-center text-xs text-rapido-muted">
    <a href={REPO_URL} className="text-rapido-accent hover:underline">
      Rapido
    </a>
    {", "}Made with ❤️ by{" "}
    <a href={ORGANIZATION_URL} className="text-rapido-accent hover:underline">
      legendary1205
    </a>
  </div>
);
