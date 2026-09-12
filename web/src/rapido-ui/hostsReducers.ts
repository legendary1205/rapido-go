import { Host, HostsMap } from "types/Host";

// Pure state-transition functions extracted out of HostsAdmin.tsx's inline
// setHosts callbacks so they can be unit-tested directly, without rendering
// the component. Each returns a brand-new HostsMap rather than mutating -
// this is what lets HostsAdmin.tsx's own dirty check
// (JSON.stringify(hosts) !== original) and PUT /api/hosts's full-replace
// requirement (see internal/httpapi/hosts.go's handlePutHosts) both work
// correctly: editing one host in one tag must never touch any other tag's
// array, or a save would silently take those hosts away from every other
// inbound's customers.

export const emptyHost = (priority = 0): Host => ({
  remark: "",
  address: "",
  port: null,
  sni: "",
  host: "",
  path: "",
  security: "inbound_default",
  alpn: "",
  fingerprint: "",
  allowinsecure: false,
  is_disabled: false,
  mux_enable: false,
  fragment_setting: "",
  noise_setting: "",
  random_user_agent: false,
  use_sni_as_host: false,
  priority,
});

export const patchHostAt = (
  hosts: HostsMap,
  tag: string,
  index: number,
  patch: Partial<Host>
): HostsMap => {
  const list = hosts[tag].slice();
  list[index] = { ...list[index], ...patch };
  return { ...hosts, [tag]: list };
};

export const removeHostAt = (
  hosts: HostsMap,
  tag: string,
  index: number
): HostsMap => {
  const list = hosts[tag].slice();
  list.splice(index, 1);
  return { ...hosts, [tag]: list };
};

// A freshly-added host with no explicit priority goes to the END of the
// GLOBAL order (max across every tag, not just this one) + 1 - matches
// what an admin expects ("new host shows up last"), not "last within
// whichever tag happened to receive it".
const maxPriority = (hosts: HostsMap): number =>
  Object.values(hosts)
    .flat()
    .reduce((max, h) => Math.max(max, h.priority), -1);

export const addHostToTag = (
  hosts: HostsMap,
  tag: string,
  host?: Host
): HostsMap => ({
  ...hosts,
  // `hosts[tag]` may not exist yet - a tag with zero hosts so far (e.g. a
  // brand-new inbound) simply isn't a key in this map at all (see
  // internal/httpapi/hosts.go's handleGetHosts, which only emits a key for
  // tags that already have at least one row).
  [tag]: [...(hosts[tag] ?? []), host ?? emptyHost(maxPriority(hosts) + 1)],
});

// One flattened, globally-sorted view of every host across every tag -
// (priority, then id as a tiebreak so two same-priority hosts stay in a
// stable order instead of jittering) - what the reorder UI and the up/
// down buttons actually operate on, since the whole point of `priority`
// is that it's comparable ACROSS tags, not just within one.
export type FlatHost = { tag: string; index: number; host: Host };

export const flattenSortedHosts = (hosts: HostsMap): FlatHost[] => {
  const flat: FlatHost[] = [];
  for (const tag of Object.keys(hosts)) {
    hosts[tag].forEach((host, index) => flat.push({ tag, index, host }));
  }
  flat.sort((a, b) => {
    if (a.host.priority !== b.host.priority) return a.host.priority - b.host.priority;
    return (a.host.id ?? 0) - (b.host.id ?? 0);
  });
  return flat;
};

// Moves the host at `flatIndex` (from an already-computed
// flattenSortedHosts view) one step up or down, then renumbers EVERY
// host in the flat list to a clean, unique, sequential priority (0..N-1)
// matching its new position.
//
// This used to swap just the two `priority` VALUES between the moved
// host and its neighbor (see git history: swapHostPriority). That broke
// permanently the moment two hosts anywhere in the list ended up with an
// equal priority (a real, recurring incident - e.g. hand-built host
// payloads during a migration, or any other writer that didn't bother to
// give every row a distinct value): swapping two equal numbers changes
// nothing, so the neighbor pair silently stops responding to the up/down
// buttons, and the flattened view's own (priority, id) tiebreak snaps
// straight back to the pre-swap order. Moving by ARRAY POSITION instead
// sidesteps the collision entirely - it doesn't matter what the two
// neighbors' priority values were, only that they trade places - and
// renumbering the whole list afterwards means any duplicate priority
// already present in `hosts` (inherited from before this fix, or from
// some future bug) gets cleaned up automatically the very first time
// anyone moves anything. Backend PUT /api/hosts (see
// internal/httpapi/hosts.go's normalizeHostPriorities) also renumbers on
// every save as a second, independent guarantee.
export const moveFlatHost = (
  hosts: HostsMap,
  flat: FlatHost[],
  flatIndex: number,
  direction: "up" | "down"
): HostsMap => {
  const targetIndex = direction === "up" ? flatIndex - 1 : flatIndex + 1;
  if (targetIndex < 0 || targetIndex >= flat.length) return hosts;

  const reordered = flat.slice();
  const tmp = reordered[flatIndex];
  reordered[flatIndex] = reordered[targetIndex];
  reordered[targetIndex] = tmp;

  let next = hosts;
  reordered.forEach((f, priority) => {
    if (f.host.priority !== priority) {
      next = patchHostAt(next, f.tag, f.index, { priority });
    }
  });
  return next;
};
