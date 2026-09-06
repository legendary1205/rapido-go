import { BadgeTone } from "rapido-ui/Badge";
import { NodeStatus } from "types/Node";

// Same colour language as the rest of the panel's status badges (Users,
// Tickets): green is healthy, red needs the admin now, amber is in-flight,
// gray is deliberately switched off. A function rather than a plain lookup
// table so an unrecognized/future status string (a backend rollout ahead of
// this frontend, say) fails safe to "gray" instead of throwing or rendering
// undefined.
export const toneForNodeStatus = (status: string): BadgeTone => {
  switch (status as NodeStatus) {
    case "connected":
      return "green";
    case "connecting":
      return "yellow";
    case "error":
      return "red";
    case "disabled":
      return "gray";
    default:
      return "gray";
  }
};
