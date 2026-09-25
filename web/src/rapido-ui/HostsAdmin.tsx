import { FC, useEffect, useMemo, useState } from "react";
import { useTranslation } from "react-i18next";
import classNames from "classnames";
import { useHostsQuery, useSaveHostsMutation } from "hooks/useHostsQuery";
import { useHostsLoadQuery } from "hooks/useHostsLoadQuery";
import { useInboundsQuery } from "hooks/useInboundsQuery";
import { Host, HostsMap } from "types/Host";
import { HostLoadEntry } from "types/HostLoad";
import { errorText } from "service/errors";
import { Card } from "rapido-ui/Card";
import { Badge } from "rapido-ui/Badge";
import { Button } from "rapido-ui/Button";
import { HostLoadPill } from "rapido-ui/HostLoadPill";
import { ltrIsolate } from "rapido-ui/bidi";
import {
  addHostToTag,
  flattenSortedHosts,
  moveFlatHost,
  patchHostAt,
  removeHostAt,
} from "rapido-ui/hostsReducers";

const SECURITY = ["inbound_default", "none", "tls"];
const ALPN = ["", "h3", "h2", "http/1.1", "h3,h2,http/1.1", "h3,h2", "h2,http/1.1"];
const FINGERPRINT = [
  "", "chrome", "firefox", "safari", "ios", "android", "edge", "360", "qq", "random", "randomized",
];

const field =
  "w-full rounded-lg border border-rapido-border bg-rapido-bg px-2.5 py-1.5 text-xs text-rapido-text placeholder:text-rapido-muted focus:outline-none focus:ring-1 focus:ring-rapido-accent";

// The exact, complete set this backend's placeholder substitution
// (internal/subscription/vars.go) actually supports - every token here
// works identically in both the Remark and Address fields (see
// internal/httpapi/subscription.go's forEachUserHost, which formats both
// through the same Variables map). Deliberately does NOT list
// {ACTIVE_USERS}: that token is referenced only in a doc comment in
// vars.go, never actually assigned - listing it here would advertise a
// token that silently renders as "<missing>" today. Also omits
// {SERVER_IPV6}/{JALALI_EXPIRE_DATE}, both explicitly deferred (never
// ported from the old Python system) for the same reason.
// {LOAD}, {LOAD_EMOJI}, {LOAD_PERCENT} and {LOAD_LEVEL} are the live per-config
// load (GET /hosts/load); they render empty for a host with no load data.
const VARIABLES: { token: string; descKey: string }[] = [
  { token: "{USERNAME}", descKey: "hostsDialog.username" },
  { token: "{SERVER_IP}", descKey: "hostsDialog.currentServer" },
  { token: "{DATA_USAGE}", descKey: "hostsDialog.dataUsage" },
  { token: "{DATA_LIMIT}", descKey: "hostsDialog.dataLimit" },
  { token: "{DATA_LEFT}", descKey: "hostsDialog.remainingData" },
  { token: "{USAGE_PERCENTAGE}", descKey: "rapido.hosts.varUsagePercentage" },
  { token: "{DAYS_LEFT}", descKey: "hostsDialog.remainingDays" },
  { token: "{TIME_LEFT}", descKey: "hostsDialog.remainingTime" },
  { token: "{EXPIRE_DATE}", descKey: "hostsDialog.expireDate" },
  { token: "{STATUS_EMOJI}", descKey: "hostsDialog.statusEmoji" },
  { token: "{STATUS_TEXT}", descKey: "hostsDialog.statusText" },
  { token: "{PROTOCOL}", descKey: "rapido.hosts.varProtocol" },
  { token: "{TRANSPORT}", descKey: "rapido.hosts.varTransport" },
  { token: "{LOAD}", descKey: "rapido.hosts.varLoad" },
  { token: "{LOAD_EMOJI}", descKey: "rapido.hosts.varLoadEmoji" },
  { token: "{LOAD_PERCENT}", descKey: "rapido.hosts.varLoadPercent" },
  { token: "{LOAD_LEVEL}", descKey: "rapido.hosts.varLoadLevel" },
];

const HostVariablesReference: FC = () => {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);

  return (
    <Card className="p-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <div className="text-sm font-semibold">{t("rapido.hosts.variablesTitle")}</div>
          <div className="text-xs text-rapido-muted">{t("hostsDialog.desc")}</div>
        </div>
        <Button variant="chip" onClick={() => setOpen((o) => !o)}>
          {open ? t("rapido.hosts.hideVariables") : t("rapido.hosts.showVariables")}
        </Button>
      </div>

      {open && (
        <div className="mt-3 flex flex-col gap-1.5">
          <p className="text-xs text-rapido-muted">{t("rapido.hosts.variablesScope")}</p>
          <div className="flex flex-col divide-y divide-rapido-border overflow-x-auto">
            {VARIABLES.map((v) => (
              <div key={v.token} className="flex items-center gap-3 py-1.5 text-xs">
                <span
                  className="w-40 shrink-0 font-mono text-rapido-accent"
                  dir="ltr"
                >
                  {v.token}
                </span>
                <span className="text-rapido-muted">{t(v.descKey)}</span>
              </div>
            ))}
          </div>
        </div>
      )}
    </Card>
  );
};

// ---------------------------------------------------------------------------

const HostRow: FC<{
  host: Host;
  tag: string;
  /** Live load for this host, when the backend reports one (a saved, enabled, non-info host). */
  load?: HostLoadEntry;
  capacity: number;
  onChange: (patch: Partial<Host>) => void;
  onRemove: () => void;
  onMoveUp: () => void;
  onMoveDown: () => void;
  canMoveUp: boolean;
  canMoveDown: boolean;
}> = ({ host, tag, load, capacity, onChange, onRemove, onMoveUp, onMoveDown, canMoveUp, canMoveDown }) => {
  const { t } = useTranslation();
  const [open, setOpen] = useState(false);
  const [confirmRemove, setConfirmRemove] = useState(false);
  const disabled = !!host.is_disabled;

  return (
    <div
      className={classNames(
        "rounded-lg border p-3",
        disabled ? "border-rapido-border opacity-60" : "border-sky-500/40 bg-sky-500/[0.03]"
      )}
    >
      <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
        <Badge tone="sky" dir="ltr">
          {tag}
        </Badge>
        {load && <HostLoadPill load={load} capacity={capacity} />}
      </div>
      <div className="grid gap-2 sm:grid-cols-[1fr_1fr_5rem]">
        <label className="flex flex-col gap-1">
          <span className="text-[11px] text-rapido-muted">{t("hostsDialog.remark")}</span>
          <input
            className={field}
            value={host.remark}
            onChange={(e) => onChange({ remark: e.target.value })}
          />
        </label>
        <label className="flex flex-col gap-1">
          <span className="text-[11px] text-rapido-muted">{t("hostsDialog.address")}</span>
          <input
            className={field}
            dir="ltr"
            value={host.address}
            onChange={(e) => onChange({ address: e.target.value })}
          />
        </label>
        <label className="flex flex-col gap-1">
          <span className="text-[11px] text-rapido-muted">{t("hostsDialog.port")}</span>
          <input
            className={field}
            dir="ltr"
            inputMode="numeric"
            value={host.port ?? ""}
            onChange={(e) =>
              onChange({ port: e.target.value ? Number(e.target.value) : null })
            }
          />
        </label>
      </div>

      <div className="mt-2 flex flex-wrap items-center gap-2">
        <Button
          variant="chip"
          disabled={!canMoveUp}
          onClick={onMoveUp}
          title={t("rapido.hosts.moveUp")}
          aria-label={t("rapido.hosts.moveUp")}
        >
          ↑
        </Button>
        <Button
          variant="chip"
          disabled={!canMoveDown}
          onClick={onMoveDown}
          title={t("rapido.hosts.moveDown")}
          aria-label={t("rapido.hosts.moveDown")}
        >
          ↓
        </Button>
        <Button variant="chip" onClick={() => setOpen((o) => !o)}>
          {open ? t("rapido.hosts.hideAdvanced") : t("rapido.hosts.showAdvanced")}
        </Button>
        <Button
          variant="chip"
          tone={disabled ? "sky" : "amber"}
          onClick={() => onChange({ is_disabled: !disabled })}
        >
          {disabled ? t("rapido.hosts.enable") : t("rapido.hosts.disable")}
        </Button>
        {confirmRemove ? (
          <>
            <span className="text-xs text-red-400">{t("rapido.hosts.removeWarning")}</span>
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
        {disabled && <Badge tone="gray">{t("rapido.hosts.disabled")}</Badge>}
      </div>

      {open && (
        <div className="mt-3 grid gap-2 sm:grid-cols-2 lg:grid-cols-3">
          {(["sni", "host", "path"] as const).map((k) => (
            <label key={k} className="flex flex-col gap-1">
              <span className="text-[11px] text-rapido-muted">{k.toUpperCase()}</span>
              <input
                className={field}
                dir="ltr"
                value={(host[k] as string) ?? ""}
                onChange={(e) => onChange({ [k]: e.target.value } as Partial<Host>)}
              />
            </label>
          ))}

          <label className="flex flex-col gap-1">
            <span className="text-[11px] text-rapido-muted">{t("hostsDialog.security")}</span>
            <select
              className={field}
              value={host.security}
              onChange={(e) => onChange({ security: e.target.value })}
            >
              {SECURITY.map((s) => (
                <option key={s} value={s}>{s}</option>
              ))}
            </select>
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-[11px] text-rapido-muted">ALPN</span>
            <select
              className={field}
              value={host.alpn}
              onChange={(e) => onChange({ alpn: e.target.value })}
            >
              {ALPN.map((a) => (
                <option key={a} value={a}>{a || "none"}</option>
              ))}
            </select>
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-[11px] text-rapido-muted">{t("hostsDialog.fingerprint")}</span>
            <select
              className={field}
              value={host.fingerprint}
              onChange={(e) => onChange({ fingerprint: e.target.value })}
            >
              {FINGERPRINT.map((f) => (
                <option key={f} value={f}>{f || "none"}</option>
              ))}
            </select>
          </label>

          {(["fragment_setting", "noise_setting"] as const).map((k) => (
            <label key={k} className="flex flex-col gap-1">
              <span className="text-[11px] text-rapido-muted">{k.replace("_", " ")}</span>
              <input
                className={field}
                dir="ltr"
                value={(host[k] as string) ?? ""}
                onChange={(e) => onChange({ [k]: e.target.value } as Partial<Host>)}
              />
            </label>
          ))}

          <div className="flex flex-col gap-1.5 text-xs sm:col-span-2 lg:col-span-3">
            {(
              [
                ["allowinsecure", t("rapido.hosts.allowInsecure")],
                ["mux_enable", "Mux"],
                ["random_user_agent", t("rapido.hosts.randomUserAgent")],
                ["use_sni_as_host", t("rapido.hosts.useSniAsHost")],
              ] as const
            ).map(([k, label]) => (
              <label key={k} className="flex items-center gap-2">
                <input
                  type="checkbox"
                  checked={!!host[k]}
                  onChange={(e) => onChange({ [k]: e.target.checked } as Partial<Host>)}
                />
                <span>{label}</span>
              </label>
            ))}
          </div>
        </div>
      )}
    </div>
  );
};

// ---------------------------------------------------------------------------

export const HostsAdmin: FC = () => {
  const { t } = useTranslation();
  const { data: remoteHosts, isLoading, isError, refetch } = useHostsQuery();
  const { data: inboundsByProtocol } = useInboundsQuery();
  const saveHosts = useSaveHostsMutation();
  // Decoration only: if it is slow, missing or failing the list still works.
  const { data: hostsLoad } = useHostsLoadQuery();
  const loadByHostId = useMemo(
    () => new Map((hostsLoad?.hosts ?? []).map((h) => [h.host_id, h] as const)),
    [hostsLoad]
  );

  const [hosts, setHosts] = useState<HostsMap | null>(null);
  const [original, setOriginal] = useState<string>("");
  const [confirming, setConfirming] = useState(false);
  const [msg, setMsg] = useState<{ tone: "ok" | "err"; text: string } | null>(null);

  // Every real inbound tag, not just ones that already have a host - a
  // brand-new inbound (auto-created with one default host, see
  // internal/httpapi/inbounds.go, but included here for robustness/future-
  // proofing anyway) needs to be choosable in the "add host" tag picker
  // even if `hosts` itself has no entry for it yet.
  const allTags = useMemo(() => {
    const fromInbounds = Object.values(inboundsByProtocol ?? {}).flat();
    const fromHosts = Object.keys(hosts ?? {});
    return Array.from(new Set([...fromInbounds, ...fromHosts])).sort();
  }, [inboundsByProtocol, hosts]);
  const [newHostTag, setNewHostTag] = useState("");
  useEffect(() => {
    if (!newHostTag && allTags.length > 0) setNewHostTag(allTags[0]);
  }, [allTags, newHostTag]);

  // Local edit buffer, seeded from the query result and re-seeded whenever a
  // fresh copy arrives (initial load, a manual refetch, or a successful
  // save's echoed-back response) - never merged with in-progress edits, since
  // "discard and reload" is the only way this page lets an admin back out of
  // a change anyway.
  useEffect(() => {
    if (remoteHosts) {
      setHosts(remoteHosts);
      setOriginal(JSON.stringify(remoteHosts));
      setMsg(null);
    }
  }, [remoteHosts]);

  const dirty = hosts !== null && JSON.stringify(hosts) !== original;

  const patch = (tag: string, index: number, p: Partial<Host>) => {
    setHosts((h) => (h ? patchHostAt(h, tag, index, p) : h));
    setMsg(null);
  };

  const removeHost = (tag: string, index: number) => {
    setHosts((h) => (h ? removeHostAt(h, tag, index) : h));
    setMsg(null);
  };

  const addHost = () => {
    if (!newHostTag) return;
    setHosts((h) => (h ? addHostToTag(h, newHostTag) : h));
    setMsg(null);
  };

  // The flattened, globally-sorted view IS the actual customer-facing
  // order (see internal/httpapi/subscription.go's forEachUserHost, which
  // sorts by this exact same (priority, id) pair after gathering hosts
  // from every included tag) - moving "up"/"down" here swaps priority
  // with the flat list's real neighbor, which can freely be a host from a
  // different inbound tag/node. That's the point: this is what lets an
  // admin interleave configs across tags, not just reorder within one.
  const flat = useMemo(() => (hosts ? flattenSortedHosts(hosts) : []), [hosts]);

  const moveInFlatList = (flatIndex: number, direction: "up" | "down") => {
    setHosts((h) => (h ? moveFlatHost(h, flat, flatIndex, direction) : h));
    setMsg(null);
  };

  const save = () => {
    if (!hosts) return;
    setMsg(null);
    // The whole map goes back every time - PUT /hosts replaces the lot (see
    // internal/httpapi/hosts.go's handlePutHosts), so sending only the
    // edited inbound would silently delete every host of every other
    // inbound, i.e. take those servers away from all of their customers.
    saveHosts.mutate(hosts, {
      onSuccess: (saved) => {
        setHosts(saved);
        setOriginal(JSON.stringify(saved));
        setMsg({ tone: "ok", text: t("rapido.hosts.saved") });
        setConfirming(false);
      },
      onError: (e) => {
        setMsg({ tone: "err", text: errorText(e, t("rapido.hosts.saveFailed")) });
        setConfirming(false);
      },
    });
  };

  if (isError) {
    return (
      <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-400">
        {t("rapido.hosts.loadFailed")}
      </div>
    );
  }
  if (isLoading || !hosts) {
    return <p className="text-sm text-rapido-muted">{t("rapido.tickets.loading")}</p>;
  }

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="text-sm text-rapido-muted">
          {t("rapido.hosts.summary", { inbounds: allTags.length, hosts: flat.length })}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          {dirty && <Badge tone="yellow">{t("rapido.hosts.unsaved")}</Badge>}
          <Button
            variant="chip"
            disabled={saveHosts.isPending}
            onClick={() => {
              refetch();
              setMsg(null);
            }}
          >
            {dirty ? t("rapido.hosts.discard") : t("rapido.tickets.refresh")}
          </Button>
          {confirming ? (
            <>
              <span className="text-xs text-amber-400">{t("rapido.hosts.saveWarning")}</span>
              <Button variant="chip" tone="amber" disabled={saveHosts.isPending} onClick={save}>
                {saveHosts.isPending ? t("rapido.pleaseWait") : t("rapido.hosts.saveConfirm")}
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
              {t("rapido.hosts.save")}
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

      {hostsLoad?.indicator && (
        <p className="text-xs text-rapido-muted">
          {t("rapido.hosts.loadHint", { token: ltrIsolate("{LOAD}") })}
        </p>
      )}

      <HostVariablesReference />

      <Card className="p-4">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <span className="text-sm font-semibold">{t("rapido.hosts.addHost")}</span>
          <div className="flex flex-wrap items-center gap-2">
            <select
              className={field}
              dir="ltr"
              value={newHostTag}
              onChange={(e) => setNewHostTag(e.target.value)}
              disabled={allTags.length === 0}
            >
              {allTags.map((tag) => (
                <option key={tag} value={tag}>
                  {tag}
                </option>
              ))}
            </select>
            <Button variant="chip" tone="accent" disabled={!newHostTag} onClick={addHost}>
              + {t("rapido.hosts.addHost")}
            </Button>
          </div>
        </div>
      </Card>

      {flat.length === 0 ? (
        <Card className="p-6 text-center text-sm text-rapido-muted">
          {t("rapido.hosts.empty")}
        </Card>
      ) : (
        <div className="flex flex-col gap-2">
          {flat.map((f, flatIndex) => (
            <HostRow
              key={`${f.tag}:${f.host.id ?? f.index}`}
              host={f.host}
              tag={f.tag}
              load={f.host.id != null ? loadByHostId.get(f.host.id) : undefined}
              capacity={hostsLoad?.capacity ?? 0}
              onChange={(p) => patch(f.tag, f.index, p)}
              onRemove={() => removeHost(f.tag, f.index)}
              onMoveUp={() => moveInFlatList(flatIndex, "up")}
              onMoveDown={() => moveInFlatList(flatIndex, "down")}
              canMoveUp={flatIndex > 0}
              canMoveDown={flatIndex < flat.length - 1}
            />
          ))}
        </div>
      )}
    </div>
  );
};

export default HostsAdmin;
