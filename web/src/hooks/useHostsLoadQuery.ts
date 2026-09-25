import { useQuery } from "@tanstack/react-query";
import { fetch } from "service/http";
import { HostsLoad } from "types/HostLoad";
import { queryKeys } from "utils/queryClient";

// GET /hosts/load is sudo-only (like the Hosts and Nodes pages that read it).
// The Hosts page's per-config pills and the Nodes page's per-node chips share
// this one query, so both are served from a single cache entry and poller. The
// backend caches it for 3 s, so a 5 s poll always sees fresh numbers;
// react-query does not poll a hidden tab (refetchIntervalInBackground defaults
// to false), which is the "only while visible" rule.
//
// retry: false - this is decoration on top of the host and node lists, and a
// poll that failed is simply retried by the next tick; it must never delay or
// block the page it decorates.
export const useHostsLoadQuery = (enabled = true) =>
  useQuery({
    queryKey: queryKeys.hostsLoad,
    queryFn: () => fetch<HostsLoad>("/hosts/load"),
    refetchInterval: 5_000,
    refetchIntervalInBackground: false,
    retry: false,
    enabled,
  });
