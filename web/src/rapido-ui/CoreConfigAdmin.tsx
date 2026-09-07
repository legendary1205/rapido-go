import { FC, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import classNames from "classnames";
import { useCoreConfigQuery, useSaveCoreConfigMutation } from "hooks/useCoreConfigQuery";
import {
  CoreConfig,
  DNSServer,
  DNS_SERVER_TYPES,
  LOG_LEVELS,
  NETWORK_TYPES,
  NetworkType,
  Outbound,
  OUTBOUND_TYPES,
  PROTOCOL_TYPES,
  ProtocolType,
  RoutingRule,
} from "types/CoreConfig";
import { errorText } from "service/errors";
import {
  OutboundOption,
  buildOutboundOptions,
  formatIntList,
  formatList,
  moveItem,
  parseCommaIntList,
  parseCommaList,
  summarizeRoutingRule,
  validateDnsServerDraft,
  validateOutboundDraft,
  validateRoutingRuleDraft,
} from "utils/coreConfigHelpers";
import { Card, CardSubtitle, CardTitle } from "rapido-ui/Card";
import { Badge } from "rapido-ui/Badge";
import { Button } from "rapido-ui/Button";
import { Input } from "rapido-ui/Input";
import { Select } from "rapido-ui/Select";
import { Checkbox } from "rapido-ui/Checkbox";
import { Modal } from "rapido-ui/Modal";

// A genuinely new, structured page - not a port of the old (Xray-backed)
// dashboard's rapido-ui/CoreSettings.tsx raw-JSON textarea editor. sing-box's
// config shape has nothing in common with Xray's, so there is no JSON to
// carry over - every field here comes straight from
// internal/httpapi/coreconfig.go's DTOs. Follows HostsAdmin.tsx's own
// convention (the closest precedent: also a sudo-only settings page backed
// by a single full-object GET/PUT pair, with no per-item endpoints at all):
// one local edit buffer seeded from the query, one "Save" that PUTs the
// whole object back, with a confirm step since it's a fleet-wide change.

const errorBanner = (text: string) => (
  <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-400">
    {text}
  </div>
);

// ---------------------------------------------------------------------------
// Outbounds

const OutboundFormModal: FC<{
  initial: Outbound | null;
  existing: Outbound[];
  onSave: (ob: Outbound) => void;
  onClose: () => void;
}> = ({ initial, existing, onSave, onClose }) => {
  const { t } = useTranslation();
  const isEdit = !!initial;
  const [tag, setTag] = useState(initial?.tag ?? "");
  const [type, setType] = useState<Outbound["type"]>(initial?.type ?? "direct");
  const [server, setServer] = useState(initial?.server ?? "");
  const [serverPort, setServerPort] = useState(
    initial?.server_port ? String(initial.server_port) : ""
  );
  const [username, setUsername] = useState(initial?.username ?? "");
  const [password, setPassword] = useState(initial?.password ?? "");
  const [members, setMembers] = useState<Set<string>>(new Set(initial?.outbounds ?? []));
  const [error, setError] = useState("");

  // Excludes this outbound's own tag - a selector/urltest cannot list itself
  // as a member (buildOutboundOptions' own doc comment).
  const memberOptions = buildOutboundOptions(existing, initial?.tag);

  const toggleMember = (value: string) =>
    setMembers((prev) => {
      const next = new Set(prev);
      if (next.has(value)) next.delete(value);
      else next.add(value);
      return next;
    });

  const optionLabel = (opt: OutboundOption) =>
    opt.implicit ? t(`rapido.coreConfig.${opt.value}Builtin`) : opt.value;

  const submit = () => {
    const isProxy = type === "socks" || type === "http";
    const isGroup = type === "selector" || type === "urltest";
    const draft: Outbound = {
      tag: tag.trim(),
      type,
      server: isProxy ? server.trim() || undefined : undefined,
      server_port: isProxy && serverPort ? Number(serverPort) : undefined,
      username: isProxy ? username.trim() || undefined : undefined,
      password: isProxy ? password || undefined : undefined,
      outbounds: isGroup ? Array.from(members) : undefined,
    };
    const err = validateOutboundDraft(draft);
    if (err) {
      setError(err);
      return;
    }
    setError("");
    onSave(draft);
  };

  return (
    <Modal onClose={onClose} className="max-w-lg">
      <h2 className="mb-4 text-lg font-semibold">
        {isEdit ? t("rapido.coreConfig.outboundEditTitle") : t("rapido.coreConfig.outboundAddTitle")}
      </h2>
      <div className="flex flex-col gap-3">
        <label className="flex flex-col gap-1">
          <span className="text-xs text-rapido-muted">{t("rapido.coreConfig.tag")}</span>
          <Input dir="ltr" value={tag} disabled={isEdit} onChange={(e) => setTag(e.target.value)} />
        </label>

        <label className="flex flex-col gap-1">
          <span className="text-xs text-rapido-muted">{t("rapido.coreConfig.type")}</span>
          <Select value={type} onChange={(e) => setType(e.target.value as Outbound["type"])}>
            {OUTBOUND_TYPES.map((ty) => (
              <option key={ty} value={ty}>
                {ty}
              </option>
            ))}
          </Select>
        </label>

        {(type === "socks" || type === "http") && (
          <>
            <div className="grid grid-cols-[1fr_7rem] gap-3">
              <label className="flex flex-col gap-1">
                <span className="text-xs text-rapido-muted">{t("rapido.coreConfig.server")}</span>
                <Input dir="ltr" value={server} onChange={(e) => setServer(e.target.value)} />
              </label>
              <label className="flex flex-col gap-1">
                <span className="text-xs text-rapido-muted">{t("rapido.coreConfig.serverPort")}</span>
                <Input
                  dir="ltr"
                  type="number"
                  value={serverPort}
                  onChange={(e) => setServerPort(e.target.value)}
                />
              </label>
            </div>
            <label className="flex flex-col gap-1">
              <span className="text-xs text-rapido-muted">
                {t("rapido.coreConfig.usernameOptional")}
              </span>
              <Input dir="ltr" value={username} onChange={(e) => setUsername(e.target.value)} />
            </label>
            <label className="flex flex-col gap-1">
              <span className="text-xs text-rapido-muted">
                {t("rapido.coreConfig.passwordOptional")}
              </span>
              <Input
                dir="ltr"
                type="password"
                autoComplete="new-password"
                value={password}
                onChange={(e) => setPassword(e.target.value)}
              />
            </label>
          </>
        )}

        {(type === "selector" || type === "urltest") && (
          <div>
            <div className="mb-2 text-xs font-medium text-rapido-muted">
              {t("rapido.coreConfig.memberOutbounds")}
            </div>
            <div className="flex flex-wrap gap-3 rounded-lg border border-rapido-border p-3">
              {memberOptions.map((opt) => (
                <Checkbox
                  key={opt.value}
                  checked={members.has(opt.value)}
                  onChange={() => toggleMember(opt.value)}
                  label={optionLabel(opt)}
                />
              ))}
            </div>
          </div>
        )}

        {error && errorBanner(t(error))}

        <div className="mt-2 flex justify-end gap-2">
          <Button variant="chip" onClick={onClose}>
            {t("cancel")}
          </Button>
          <Button variant="chip" tone="accent" onClick={submit}>
            {t("rapido.coreConfig.save")}
          </Button>
        </div>
      </div>
    </Modal>
  );
};

const OutboundRow: FC<{ outbound: Outbound; onEdit: () => void; onRemove: () => void }> = ({
  outbound,
  onEdit,
  onRemove,
}) => {
  const { t } = useTranslation();
  const [confirmRemove, setConfirmRemove] = useState(false);

  return (
    <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-rapido-border p-3">
      <div className="flex min-w-0 flex-wrap items-center gap-2">
        <span className="truncate text-sm font-semibold" dir="ltr">
          {outbound.tag}
        </span>
        <Badge tone="sky">{outbound.type}</Badge>
        {(outbound.type === "socks" || outbound.type === "http") && outbound.server && (
          <span className="text-xs text-rapido-muted" dir="ltr">
            {outbound.server}:{outbound.server_port}
          </span>
        )}
        {(outbound.type === "selector" || outbound.type === "urltest") &&
          (outbound.outbounds?.length ?? 0) > 0 && (
            <span className="text-xs text-rapido-muted" dir="ltr">
              {outbound.outbounds!.join(", ")}
            </span>
          )}
      </div>
      <div className="flex flex-wrap items-center gap-1.5">
        <Button variant="chip" tone="accent" onClick={onEdit}>
          {t("rapido.edit")}
        </Button>
        {confirmRemove ? (
          <>
            <Button variant="chip" tone="red" onClick={onRemove}>
              {t("delete")}
            </Button>
            <Button variant="chip" onClick={() => setConfirmRemove(false)}>
              {t("cancel")}
            </Button>
          </>
        ) : (
          <Button variant="chip" tone="red" onClick={() => setConfirmRemove(true)}>
            {t("delete")}
          </Button>
        )}
      </div>
    </div>
  );
};

// ---------------------------------------------------------------------------
// Routing rules

const RoutingRuleFormModal: FC<{
  initial: RoutingRule | null;
  outboundOptions: OutboundOption[];
  onSave: (rule: RoutingRule) => void;
  onClose: () => void;
}> = ({ initial, outboundOptions, onSave, onClose }) => {
  const { t } = useTranslation();
  const isEdit = !!initial;
  const [domain, setDomain] = useState(formatList(initial?.domain));
  const [domainSuffix, setDomainSuffix] = useState(formatList(initial?.domain_suffix));
  const [domainKeyword, setDomainKeyword] = useState(formatList(initial?.domain_keyword));
  const [ipCidr, setIpCidr] = useState(formatList(initial?.ip_cidr));
  const [ipIsPrivate, setIpIsPrivate] = useState(initial?.ip_is_private ?? false);
  const [port, setPort] = useState(formatIntList(initial?.port));
  const [portRange, setPortRange] = useState(formatList(initial?.port_range));
  const [network, setNetwork] = useState<Set<NetworkType>>(new Set(initial?.network ?? []));
  const [protocol, setProtocol] = useState<Set<ProtocolType>>(new Set(initial?.protocol ?? []));
  const [outboundTag, setOutboundTag] = useState(
    initial?.outbound_tag ?? outboundOptions[0]?.value ?? "direct"
  );
  const [error, setError] = useState("");

  const toggleNetwork = (n: NetworkType) =>
    setNetwork((prev) => {
      const next = new Set(prev);
      if (next.has(n)) next.delete(n);
      else next.add(n);
      return next;
    });

  const toggleProtocol = (p: ProtocolType) =>
    setProtocol((prev) => {
      const next = new Set(prev);
      if (next.has(p)) next.delete(p);
      else next.add(p);
      return next;
    });

  const submit = () => {
    const draft: RoutingRule = {
      domain: parseCommaList(domain),
      domain_suffix: parseCommaList(domainSuffix),
      domain_keyword: parseCommaList(domainKeyword),
      ip_cidr: parseCommaList(ipCidr),
      ip_is_private: ipIsPrivate,
      port: parseCommaIntList(port),
      port_range: parseCommaList(portRange),
      network: Array.from(network),
      protocol: Array.from(protocol),
      outbound_tag: outboundTag,
    };
    const err = validateRoutingRuleDraft(draft);
    if (err) {
      setError(err);
      return;
    }
    setError("");
    onSave(draft);
  };

  return (
    <Modal onClose={onClose} className="max-w-2xl">
      <h2 className="mb-4 text-lg font-semibold">
        {isEdit ? t("rapido.coreConfig.ruleEditTitle") : t("rapido.coreConfig.ruleAddTitle")}
      </h2>
      <div className="flex flex-col gap-3">
        <div className="grid gap-3 sm:grid-cols-2">
          <label className="flex flex-col gap-1">
            <span className="text-xs text-rapido-muted">{t("rapido.coreConfig.domain")}</span>
            <Input dir="ltr" value={domain} onChange={(e) => setDomain(e.target.value)} placeholder="example.com" />
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-xs text-rapido-muted">{t("rapido.coreConfig.domainSuffix")}</span>
            <Input
              dir="ltr"
              value={domainSuffix}
              onChange={(e) => setDomainSuffix(e.target.value)}
              placeholder=".example.com"
            />
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-xs text-rapido-muted">{t("rapido.coreConfig.domainKeyword")}</span>
            <Input
              dir="ltr"
              value={domainKeyword}
              onChange={(e) => setDomainKeyword(e.target.value)}
              placeholder="google"
            />
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-xs text-rapido-muted">{t("rapido.coreConfig.ipCidr")}</span>
            <Input dir="ltr" value={ipCidr} onChange={(e) => setIpCidr(e.target.value)} placeholder="10.0.0.0/8" />
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-xs text-rapido-muted">{t("rapido.coreConfig.port")}</span>
            <Input dir="ltr" value={port} onChange={(e) => setPort(e.target.value)} placeholder="443, 8443" />
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-xs text-rapido-muted">{t("rapido.coreConfig.portRange")}</span>
            <Input
              dir="ltr"
              value={portRange}
              onChange={(e) => setPortRange(e.target.value)}
              placeholder="1000:2000"
            />
          </label>
        </div>
        <p className="-mt-1 text-xs text-rapido-muted">
          {t("rapido.coreConfig.commaSeparated")} · {t("rapido.coreConfig.portRangeHint")}
        </p>

        <Checkbox
          checked={ipIsPrivate}
          onChange={(e) => setIpIsPrivate(e.target.checked)}
          label={t("rapido.coreConfig.ipIsPrivate")}
        />

        <div>
          <div className="mb-2 text-xs font-medium text-rapido-muted">{t("rapido.coreConfig.network")}</div>
          <div className="flex flex-wrap gap-4">
            {NETWORK_TYPES.map((n) => (
              <Checkbox key={n} checked={network.has(n)} onChange={() => toggleNetwork(n)} label={n} />
            ))}
          </div>
        </div>

        <div>
          <div className="mb-2 text-xs font-medium text-rapido-muted">{t("rapido.coreConfig.protocol")}</div>
          <div className="flex flex-wrap gap-4">
            {PROTOCOL_TYPES.map((p) => (
              <Checkbox key={p} checked={protocol.has(p)} onChange={() => toggleProtocol(p)} label={p} />
            ))}
          </div>
        </div>

        <label className="flex flex-col gap-1">
          <span className="text-xs text-rapido-muted">{t("rapido.coreConfig.outboundLabel")}</span>
          <Select value={outboundTag} onChange={(e) => setOutboundTag(e.target.value)}>
            {outboundOptions.map((opt) => (
              <option key={opt.value} value={opt.value}>
                {opt.implicit ? t(`rapido.coreConfig.${opt.value}Builtin`) : opt.value}
              </option>
            ))}
          </Select>
        </label>

        {error && errorBanner(t(error))}

        <div className="mt-2 flex justify-end gap-2">
          <Button variant="chip" onClick={onClose}>
            {t("cancel")}
          </Button>
          <Button variant="chip" tone="accent" onClick={submit}>
            {t("rapido.coreConfig.save")}
          </Button>
        </div>
      </div>
    </Modal>
  );
};

const RoutingRuleRow: FC<{
  rule: RoutingRule;
  index: number;
  count: number;
  onEdit: () => void;
  onRemove: () => void;
  onMove: (direction: -1 | 1) => void;
}> = ({ rule, index, count, onEdit, onRemove, onMove }) => {
  const { t } = useTranslation();
  const [confirmRemove, setConfirmRemove] = useState(false);

  return (
    <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-rapido-border p-3">
      <div className="flex min-w-0 flex-wrap items-center gap-2">
        <Badge tone="gray">#{index + 1}</Badge>
        <span className="truncate text-xs text-rapido-muted" dir="ltr">
          {summarizeRoutingRule(rule)}
        </span>
        <Badge tone="brand">
          <span dir="ltr">{rule.outbound_tag}</span>
        </Badge>
      </div>
      <div className="flex flex-wrap items-center gap-1.5">
        <Button
          variant="chip"
          disabled={index === 0}
          onClick={() => onMove(-1)}
          aria-label={t("rapido.coreConfig.moveUp")}
        >
          {"↑"}
        </Button>
        <Button
          variant="chip"
          disabled={index === count - 1}
          onClick={() => onMove(1)}
          aria-label={t("rapido.coreConfig.moveDown")}
        >
          {"↓"}
        </Button>
        <Button variant="chip" tone="accent" onClick={onEdit}>
          {t("rapido.edit")}
        </Button>
        {confirmRemove ? (
          <>
            <Button variant="chip" tone="red" onClick={onRemove}>
              {t("delete")}
            </Button>
            <Button variant="chip" onClick={() => setConfirmRemove(false)}>
              {t("cancel")}
            </Button>
          </>
        ) : (
          <Button variant="chip" tone="red" onClick={() => setConfirmRemove(true)}>
            {t("delete")}
          </Button>
        )}
      </div>
    </div>
  );
};

// ---------------------------------------------------------------------------
// DNS servers

const DnsServerFormModal: FC<{
  initial: DNSServer | null;
  onSave: (srv: DNSServer) => void;
  onClose: () => void;
}> = ({ initial, onSave, onClose }) => {
  const { t } = useTranslation();
  const isEdit = !!initial;
  const [tag, setTag] = useState(initial?.tag ?? "");
  const [type, setType] = useState<DNSServer["type"]>(initial?.type ?? "udp");
  const [address, setAddress] = useState(initial?.address ?? "");
  const [port, setPort] = useState(initial?.port ? String(initial.port) : "");
  const [path, setPath] = useState(initial?.path ?? "");
  const [error, setError] = useState("");

  const submit = () => {
    const draft: DNSServer = {
      tag: tag.trim(),
      type,
      address: type === "local" ? undefined : address.trim() || undefined,
      port: type === "local" ? undefined : port ? Number(port) : undefined,
      path: type === "https" ? path.trim() || undefined : undefined,
    };
    const err = validateDnsServerDraft(draft);
    if (err) {
      setError(err);
      return;
    }
    setError("");
    onSave(draft);
  };

  return (
    <Modal onClose={onClose} className="max-w-md">
      <h2 className="mb-4 text-lg font-semibold">
        {isEdit ? t("rapido.coreConfig.dnsEditTitle") : t("rapido.coreConfig.dnsAddTitle")}
      </h2>
      <div className="flex flex-col gap-3">
        <label className="flex flex-col gap-1">
          <span className="text-xs text-rapido-muted">{t("rapido.coreConfig.tag")}</span>
          <Input dir="ltr" value={tag} disabled={isEdit} onChange={(e) => setTag(e.target.value)} />
        </label>

        <label className="flex flex-col gap-1">
          <span className="text-xs text-rapido-muted">{t("rapido.coreConfig.type")}</span>
          <Select value={type} onChange={(e) => setType(e.target.value as DNSServer["type"])}>
            {DNS_SERVER_TYPES.map((ty) => (
              <option key={ty} value={ty}>
                {ty}
              </option>
            ))}
          </Select>
        </label>

        {type !== "local" && (
          <div className="grid grid-cols-[1fr_7rem] gap-3">
            <label className="flex flex-col gap-1">
              <span className="text-xs text-rapido-muted">{t("rapido.coreConfig.address")}</span>
              <Input dir="ltr" value={address} onChange={(e) => setAddress(e.target.value)} />
            </label>
            <label className="flex flex-col gap-1">
              <span className="text-xs text-rapido-muted">{t("rapido.coreConfig.port")}</span>
              <Input dir="ltr" type="number" value={port} onChange={(e) => setPort(e.target.value)} />
            </label>
          </div>
        )}

        {type === "https" && (
          <label className="flex flex-col gap-1">
            <span className="text-xs text-rapido-muted">{t("rapido.coreConfig.path")}</span>
            <Input dir="ltr" value={path} onChange={(e) => setPath(e.target.value)} placeholder="/dns-query" />
          </label>
        )}

        {error && errorBanner(t(error))}

        <div className="mt-2 flex justify-end gap-2">
          <Button variant="chip" onClick={onClose}>
            {t("cancel")}
          </Button>
          <Button variant="chip" tone="accent" onClick={submit}>
            {t("rapido.coreConfig.save")}
          </Button>
        </div>
      </div>
    </Modal>
  );
};

const DnsServerRow: FC<{ server: DNSServer; onEdit: () => void; onRemove: () => void }> = ({
  server,
  onEdit,
  onRemove,
}) => {
  const { t } = useTranslation();
  const [confirmRemove, setConfirmRemove] = useState(false);

  return (
    <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-rapido-border p-3">
      <div className="flex min-w-0 flex-wrap items-center gap-2">
        <span className="truncate text-sm font-semibold" dir="ltr">
          {server.tag}
        </span>
        <Badge tone="sky">{server.type}</Badge>
        {server.type !== "local" && server.address && (
          <span className="text-xs text-rapido-muted" dir="ltr">
            {server.address}
            {server.port ? `:${server.port}` : ""}
            {server.path ?? ""}
          </span>
        )}
      </div>
      <div className="flex flex-wrap items-center gap-1.5">
        <Button variant="chip" tone="accent" onClick={onEdit}>
          {t("rapido.edit")}
        </Button>
        {confirmRemove ? (
          <>
            <Button variant="chip" tone="red" onClick={onRemove}>
              {t("delete")}
            </Button>
            <Button variant="chip" onClick={() => setConfirmRemove(false)}>
              {t("cancel")}
            </Button>
          </>
        ) : (
          <Button variant="chip" tone="red" onClick={() => setConfirmRemove(true)}>
            {t("delete")}
          </Button>
        )}
      </div>
    </div>
  );
};

// ---------------------------------------------------------------------------

// index -1 means "adding a new item" (append on save); any other index is
// the position of the item being edited (replace on save). Shared shape for
// all three list-editor modals below.
type ModalState<T> = { index: number; initial: T | null };

export const CoreConfigAdmin: FC = () => {
  const { t } = useTranslation();
  const { data, isLoading, isError, refetch } = useCoreConfigQuery();
  const saveCoreConfig = useSaveCoreConfigMutation();

  const [config, setConfig] = useState<CoreConfig | null>(null);
  const [original, setOriginal] = useState("");
  const [confirming, setConfirming] = useState(false);
  const [msg, setMsg] = useState<{ tone: "ok" | "err"; text: string } | null>(null);

  const [outboundModal, setOutboundModal] = useState<ModalState<Outbound> | null>(null);
  const [ruleModal, setRuleModal] = useState<ModalState<RoutingRule> | null>(null);
  const [dnsModal, setDnsModal] = useState<ModalState<DNSServer> | null>(null);

  // Local edit buffer, seeded from the query result and re-seeded whenever a
  // fresh copy arrives (initial load, a manual refetch, or a successful
  // save's echoed-back response) - same convention as HostsAdmin.tsx: never
  // merged with in-progress edits, since "discard and reload" is the only
  // way this page lets an admin back out of a change anyway.
  useEffect(() => {
    if (data) {
      setConfig(data);
      setOriginal(JSON.stringify(data));
      setMsg(null);
    }
  }, [data]);

  const dirty = config !== null && JSON.stringify(config) !== original;

  const save = () => {
    if (!config) return;
    setMsg(null);
    // The whole object goes back every time - PUT /settings/core-config
    // replaces the lot (see internal/httpapi/coreconfig.go's
    // handleUpdateCoreConfig), so sending only the edited section would wipe
    // every other section for every node.
    saveCoreConfig.mutate(config, {
      onSuccess: (saved) => {
        setConfig(saved);
        setOriginal(JSON.stringify(saved));
        setMsg({ tone: "ok", text: t("rapido.coreConfig.saved") });
        setConfirming(false);
      },
      onError: (e) => {
        setMsg({ tone: "err", text: errorText(e, t("rapido.coreConfig.saveFailed")) });
        setConfirming(false);
      },
    });
  };

  if (isError) {
    return errorBanner(t("rapido.coreConfig.loadFailed"));
  }
  if (isLoading || !config) {
    return <p className="text-sm text-rapido-muted">{t("rapido.tickets.loading")}</p>;
  }

  const outboundOptionsForRules = buildOutboundOptions(config.outbounds);

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-end gap-2">
        <div className="flex flex-wrap items-center gap-2">
          {dirty && <Badge tone="yellow">{t("rapido.coreConfig.unsaved")}</Badge>}
          <Button
            variant="chip"
            disabled={saveCoreConfig.isPending}
            onClick={() => {
              refetch();
              setMsg(null);
              setConfirming(false);
            }}
          >
            {dirty ? t("rapido.coreConfig.discard") : t("rapido.tickets.refresh")}
          </Button>
          {confirming ? (
            <>
              <span className="text-xs text-amber-400">{t("rapido.coreConfig.saveWarning")}</span>
              <Button variant="chip" tone="amber" disabled={saveCoreConfig.isPending} onClick={save}>
                {saveCoreConfig.isPending ? t("rapido.pleaseWait") : t("rapido.coreConfig.saveConfirm")}
              </Button>
              <Button variant="chip" onClick={() => setConfirming(false)}>
                {t("cancel")}
              </Button>
            </>
          ) : (
            <Button
              variant="chip"
              tone="accent"
              disabled={!dirty}
              onClick={() => setConfirming(true)}
            >
              {t("rapido.coreConfig.save")}
            </Button>
          )}
        </div>
      </div>

      {msg && (
        <div
          className={classNames(
            "rounded-lg border px-3 py-2 text-sm",
            msg.tone === "ok"
              ? "border-emerald-500/30 bg-emerald-500/10 text-emerald-400"
              : "border-red-500/30 bg-red-500/10 text-red-400"
          )}
        >
          {msg.text}
        </div>
      )}

      <Card className="p-4">
        <CardTitle>{t("rapido.coreConfig.logLevelTitle")}</CardTitle>
        <CardSubtitle className="mb-3">{t("rapido.coreConfig.logLevelDesc")}</CardSubtitle>
        <Select
          className="max-w-xs"
          value={config.log_level}
          onChange={(e) => setConfig({ ...config, log_level: e.target.value })}
        >
          {LOG_LEVELS.map((lvl) => (
            <option key={lvl} value={lvl}>
              {lvl}
            </option>
          ))}
        </Select>
      </Card>

      <Card className="p-4">
        <CardTitle>{t("rapido.coreConfig.sniffingTitle")}</CardTitle>
        <CardSubtitle className="mb-3">{t("rapido.coreConfig.sniffingDesc")}</CardSubtitle>
        <Checkbox
          checked={config.sniff_enabled}
          onChange={(e) => setConfig({ ...config, sniff_enabled: e.target.checked })}
          label={t("rapido.coreConfig.sniffEnabled")}
        />
      </Card>

      <Card className="p-4">
        <div className="mb-1 flex flex-wrap items-center justify-between gap-2">
          <div>
            <CardTitle>{t("rapido.coreConfig.outboundsTitle")}</CardTitle>
            <CardSubtitle>{t("rapido.coreConfig.outboundsDesc")}</CardSubtitle>
          </div>
          <Button variant="chip" tone="accent" onClick={() => setOutboundModal({ index: -1, initial: null })}>
            + {t("rapido.coreConfig.addOutbound")}
          </Button>
        </div>
        {config.outbounds.length === 0 ? (
          <p className="mt-2 text-xs text-rapido-muted">{t("rapido.coreConfig.outboundsEmpty")}</p>
        ) : (
          <div className="mt-3 flex flex-col gap-2">
            {config.outbounds.map((ob, i) => (
              <OutboundRow
                key={i}
                outbound={ob}
                onEdit={() => setOutboundModal({ index: i, initial: ob })}
                onRemove={() =>
                  setConfig({ ...config, outbounds: config.outbounds.filter((_, j) => j !== i) })
                }
              />
            ))}
          </div>
        )}
      </Card>

      <Card className="p-4">
        <div className="mb-1 flex flex-wrap items-center justify-between gap-2">
          <div>
            <CardTitle>{t("rapido.coreConfig.routingRulesTitle")}</CardTitle>
            <CardSubtitle>{t("rapido.coreConfig.routingRulesDesc")}</CardSubtitle>
          </div>
          <Button variant="chip" tone="accent" onClick={() => setRuleModal({ index: -1, initial: null })}>
            + {t("rapido.coreConfig.addRule")}
          </Button>
        </div>
        {config.routing_rules.length === 0 ? (
          <p className="mt-2 text-xs text-rapido-muted">{t("rapido.coreConfig.rulesEmpty")}</p>
        ) : (
          <div className="mt-3 flex flex-col gap-2">
            {config.routing_rules.map((rule, i) => (
              <RoutingRuleRow
                key={i}
                rule={rule}
                index={i}
                count={config.routing_rules.length}
                onEdit={() => setRuleModal({ index: i, initial: rule })}
                onRemove={() =>
                  setConfig({
                    ...config,
                    routing_rules: config.routing_rules.filter((_, j) => j !== i),
                  })
                }
                onMove={(direction) =>
                  setConfig({
                    ...config,
                    routing_rules: moveItem(config.routing_rules, i, direction),
                  })
                }
              />
            ))}
          </div>
        )}
      </Card>

      <Card className="p-4">
        <div className="mb-1 flex flex-wrap items-center justify-between gap-2">
          <div>
            <CardTitle>{t("rapido.coreConfig.dnsServersTitle")}</CardTitle>
            <CardSubtitle>{t("rapido.coreConfig.dnsServersDesc")}</CardSubtitle>
          </div>
          <Button variant="chip" tone="accent" onClick={() => setDnsModal({ index: -1, initial: null })}>
            + {t("rapido.coreConfig.addDnsServer")}
          </Button>
        </div>
        {config.dns_servers.length === 0 ? (
          <p className="mt-2 text-xs text-rapido-muted">{t("rapido.coreConfig.dnsEmpty")}</p>
        ) : (
          <div className="mt-3 flex flex-col gap-2">
            {config.dns_servers.map((srv, i) => (
              <DnsServerRow
                key={i}
                server={srv}
                onEdit={() => setDnsModal({ index: i, initial: srv })}
                onRemove={() =>
                  setConfig({ ...config, dns_servers: config.dns_servers.filter((_, j) => j !== i) })
                }
              />
            ))}
          </div>
        )}
      </Card>

      {outboundModal && (
        <OutboundFormModal
          initial={outboundModal.initial}
          existing={config.outbounds}
          onClose={() => setOutboundModal(null)}
          onSave={(ob) => {
            const outbounds =
              outboundModal.index === -1
                ? [...config.outbounds, ob]
                : config.outbounds.map((o, i) => (i === outboundModal.index ? ob : o));
            setConfig({ ...config, outbounds });
            setOutboundModal(null);
          }}
        />
      )}

      {ruleModal && (
        <RoutingRuleFormModal
          initial={ruleModal.initial}
          outboundOptions={outboundOptionsForRules}
          onClose={() => setRuleModal(null)}
          onSave={(rule) => {
            const routing_rules =
              ruleModal.index === -1
                ? [...config.routing_rules, rule]
                : config.routing_rules.map((r, i) => (i === ruleModal.index ? rule : r));
            setConfig({ ...config, routing_rules });
            setRuleModal(null);
          }}
        />
      )}

      {dnsModal && (
        <DnsServerFormModal
          initial={dnsModal.initial}
          onClose={() => setDnsModal(null)}
          onSave={(srv) => {
            const dns_servers =
              dnsModal.index === -1
                ? [...config.dns_servers, srv]
                : config.dns_servers.map((d, i) => (i === dnsModal.index ? srv : d));
            setConfig({ ...config, dns_servers });
            setDnsModal(null);
          }}
        />
      )}
    </div>
  );
};

export default CoreConfigAdmin;
