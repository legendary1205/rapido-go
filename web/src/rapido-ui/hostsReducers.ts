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

export const emptyHost = (): Host => ({
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

export const addHostToTag = (
  hosts: HostsMap,
  tag: string,
  host: Host = emptyHost()
): HostsMap => ({ ...hosts, [tag]: [...hosts[tag], host] });
