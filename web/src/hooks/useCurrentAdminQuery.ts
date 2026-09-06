import { useQuery } from "@tanstack/react-query";
import { fetch } from "service/http";
import { Admin } from "types/Admin";
import { queryKeys } from "utils/queryClient";

// Replaces the old dashboard's hooks/useGetUser.tsx (which used the retired
// react-query v3 useQuery with no queryKey at all - every caller shared one
// implicit cache slot only by coincidence). GET /api/admin identifies the
// logged-in admin: Shell reads `is_sudo` to filter nav items, SudoOnly reads
// it to gate whole pages, and the Users/Admins pages use it to tell "this
// row is me" apart from everyone else.
export const fetchCurrentAdmin = () => fetch<Admin>("/admin");

export const useCurrentAdminQuery = () =>
  useQuery({
    queryKey: queryKeys.currentAdmin,
    queryFn: fetchCurrentAdmin,
  });
