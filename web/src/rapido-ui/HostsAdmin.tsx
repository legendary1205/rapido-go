import { FC, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import classNames from "classnames";
import { useHostsQuery, useSaveHostsMutation } from "hooks/useHostsQuery";
import { Host, HostsMap } from "types/Host";
import { errorText } from "service/errors";
import { Card } from "rapido-ui/Card";
import { Badge } from "rapido-ui/Badge";
import { Button } from "rapido-ui/Button";
import { addHostToTag, patchHostAt, removeHostAt } from "rapido-ui/hostsReducers";

const SECURITY = ["inbound_default", "none", "tls"];
const ALPN = ["", "h3", "h2", "http/1.1", "h3,h2,http/1.1", "h3,h2", "h2,http/1.1"];
const FINGERPRINT = [
  "", "chrome", "firefox", "safari", "ios", "android", "edge", "360", "qq", "random", "randomized",
];

const field =
  "w-full rounded-lg border border-rapido-border bg-rapido-bg px-2.5 py-1.5 text-xs text-rapido-text placeholder:text-rapido-muted focus:outline-none focus:ring-1 focus:ring-rapido-accent";

// ---------------------------------------------------------------------------

const HostRow: FC<{
  host: Host;
  onChange: (patch: Partial<Host>) => void;
  onRemove: () => void;
}> = ({ host, onChange, onRemove }) => {
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
  const saveHosts = useSaveHostsMutation();

  const [hosts, setHosts] = useState<HostsMap | null>(null);
  const [original, setOriginal] = useState<string>("");
  const [confirming, setConfirming] = useState(false);
  const [msg, setMsg] = useState<{ tone: "ok" | "err"; text: string } | null>(null);

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

  const addHost = (tag: string) => {
    setHosts((h) => (h ? addHostToTag(h, tag) : h));
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

  const tags = Object.keys(hosts);
  const totalHosts = tags.reduce((n, tag) => n + hosts[tag].length, 0);

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="text-sm text-rapido-muted">
          {t("rapido.hosts.summary", { inbounds: tags.length, hosts: totalHosts })}
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

      {tags.length === 0 && (
        <Card className="p-6 text-center text-sm text-rapido-muted">
          {t("rapido.hosts.empty")}
        </Card>
      )}

      {tags.map((tag) => (
        <Card key={tag} className="p-4">
          <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
            <div className="flex items-center gap-2">
              <span className="text-sm font-semibold" dir="ltr">
                {tag}
              </span>
              <Badge tone={hosts[tag].length ? "sky" : "gray"}>
                {t("rapido.hosts.count", { count: hosts[tag].length })}
              </Badge>
            </div>
            <Button variant="chip" tone="accent" onClick={() => addHost(tag)}>
              + {t("rapido.hosts.addHost")}
            </Button>
          </div>

          {hosts[tag].length === 0 ? (
            <p className="text-xs text-rapido-muted">{t("rapido.hosts.noneForInbound")}</p>
          ) : (
            <div className="flex flex-col gap-2">
              {hosts[tag].map((host, i) => (
                <HostRow
                  key={i}
                  host={host}
                  onChange={(p) => patch(tag, i, p)}
                  onRemove={() => removeHost(tag, i)}
                />
              ))}
            </div>
          )}
        </Card>
      ))}
    </div>
  );
};

export default HostsAdmin;
