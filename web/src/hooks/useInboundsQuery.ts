import { useQuery } from "@tanstack/react-query";
import { fetch } from "service/http";
import { InboundsByProtocol } from "types/Inbound";
import { queryKeys } from "utils/queryClient";

// GET /api/inbounds - protocol -> tag list (see types/Inbound.ts on why this
// is simpler than the old dashboard's per-inbound-metadata shape). Shared by
// the Users and User Templates forms via rapido-ui/InboundsPicker.tsx.
export const useInboundsQuery = () =>
  useQuery({
    queryKey: queryKeys.inbounds,
    queryFn: () => fetch<InboundsByProtocol>("/inbounds"),
  });
