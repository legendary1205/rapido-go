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

// Swaps the `priority` value of two hosts, addressed by {tag, index} -
// they can be from different tags, which is the actual point: this is how
// a "move up/down" click on the flattened view crosses a tag/node
// boundary. Reuses patchHostAt twice rather than mutating array position,
// since position within a tag's own array no longer means anything for
// display order (only `priority` does) - swapping the array slots
// wouldn't change what's shown to a customer at all.
export const swapHostPriority = (
  hosts: HostsMap,
  a: { tag: string; index: number },
  b: { tag: string; index: number }
): HostsMap => {
  const priorityA = hosts[a.tag][a.index].priority;
  const priorityB = hosts[b.tag][b.index].priority;
  const withA = patchHostAt(hosts, a.tag, a.index, { priority: priorityB });
  return patchHostAt(withA, b.tag, b.index, { priority: priorityA });
};
