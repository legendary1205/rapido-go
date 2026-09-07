import { FC, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import classNames from "classnames";
import {
  useDeleteInboundMutation,
  useImportXrayConfigMutation,
  useInboundsDetailQuery,
  useSyncInboundMutation,
  useSyncInboundsBulkMutation,
} from "hooks/useInboundsQuery";
import { Inbound, InboundNetwork, InboundSecurity, InboundSyncEntry } from "types/Inbound";
import { XrayImportResult } from "types/XrayImport";
import { errorText } from "service/errors";
import { Card } from "rapido-ui/Card";
import { Badge } from "rapido-ui/Badge";
import { Button } from "rapido-ui/Button";
import { Input } from "rapido-ui/Input";
import { Select } from "rapido-ui/Select";
import { Modal } from "rapido-ui/Modal";

// Matches internal/httpapi/inbounds.go's proxyTypeValid - the only four
// protocols this backend accepts anywhere (user proxies, inbound sync).
const PROTOCOLS = ["vmess", "vless", "trojan", "shadowsocks"];
const NETWORKS: InboundNetwork[] = ["tcp", "ws", "grpc", "kcp", "quic", "splithttp", "xhttp"];
const SECURITIES: InboundSecurity[] = ["none", "tls", "reality"];

// ---------------------------------------------------------------------------

// initial !== null puts this in edit mode: every field is pre-filled from
// the existing row and the tag becomes read-only (UpsertInbound keys on
// tag, and hosts/routing-rules/exclusions elsewhere all reference it by
// name - silently renaming it here would orphan those instead of
// updating them, so renaming isn't offered at all; delete and recreate
// covers the rare case someone actually wants a new tag).
const InboundForm: FC<{ initial?: Inbound | null; onClose: () => void }> = ({ initial, onClose }) => {
  const { t } = useTranslation();
  const editing = !!initial;
  const [tag, setTag] = useState(initial?.tag ?? "");
  const [protocol, setProtocol] = useState(initial?.protocol ?? PROTOCOLS[0]);
  const [network, setNetwork] = useState<InboundNetwork>((initial?.network as InboundNetwork) ?? "tcp");
  const [headerType, setHeaderType] = useState(initial?.header_type ?? "");
  const [security, setSecurity] = useState<InboundSecurity>((initial?.security as InboundSecurity) ?? "none");
  const [realityPrivateKey, setRealityPrivateKey] = useState(initial?.reality_private_key ?? "");
  const [realityShortIDs, setRealityShortIDs] = useState((initial?.reality_short_ids ?? []).join(", "));
  const [realityServerName, setRealityServerName] = useState(initial?.reality_server_name ?? "");
  const [realityServerPort, setRealityServerPort] = useState(
    initial?.reality_server_port ? String(initial.reality_server_port) : ""
  );
  const [tlsCertificate, setTlsCertificate] = useState(initial?.tls_certificate ?? "");
  const [tlsKey, setTlsKey] = useState(initial?.tls_key ?? "");
  const [tlsServerName, setTlsServerName] = useState(initial?.tls_server_name ?? "");
  const [error, setError] = useState("");

  const syncInbound = useSyncInboundMutation();

  const submit = () => {
    setError("");
    syncInbound
      .mutateAsync({
        tag,
        protocol,
        network,
        header_type: headerType || undefined,
        security,
        // Only meaningful when security is reality - omitted otherwise so a
        // stray leftover value can never silently apply to a plain inbound.
        ...(security === "reality"
          ? {
              reality_private_key: realityPrivateKey || undefined,
              reality_short_ids: realityShortIDs
                ? realityShortIDs.split(",").map((s) => s.trim()).filter(Boolean)
                : undefined,
              reality_server_name: realityServerName || undefined,
              reality_server_port: realityServerPort ? Number(realityServerPort) : undefined,
            }
          : {}),
        // Only meaningful when security is tls - same "omit rather than
        // send a stray leftover value" reasoning as the reality fields
        // above. A tls inbound with no certificate/key yet is still a
        // valid save (see rapido.inbounds.tlsNoCertWarning on the card
        // below) - it just stays out of automatic node sync until filled
        // in, matching ListAutoSyncInbounds's own behavior.
        ...(security === "tls"
          ? {
              tls_certificate: tlsCertificate || undefined,
              tls_key: tlsKey || undefined,
              tls_server_name: tlsServerName || undefined,
            }
          : {}),
      })
      .then(onClose)
      .catch((e) => setError(errorText(e, t("rapido.inbounds.saveFailed"))));
  };

  const canSubmit = !!tag && !!protocol;

  return (
    <Modal onClose={onClose} className="max-w-md">
      <h2 className="mb-4 text-lg font-semibold">
        {editing ? t("rapido.inbounds.editTitle") : t("rapido.inbounds.addTitle")}
      </h2>
      <div className="flex flex-col gap-3">
        <label className="flex flex-col gap-1">
          <span className="text-xs text-rapido-muted">{t("rapido.inbounds.tag")}</span>
          <Input
            dir="ltr"
            value={tag}
            onChange={(e) => setTag(e.target.value)}
            placeholder="VLESS TCP"
            disabled={editing}
          />
          {editing && <span className="text-xs text-rapido-muted">{t("rapido.inbounds.tagLocked")}</span>}
        </label>

        <label className="flex flex-col gap-1">
          <span className="text-xs text-rapido-muted">{t("rapido.inbounds.protocol")}</span>
          <Select dir="ltr" value={protocol} onChange={(e) => setProtocol(e.target.value)}>
            {PROTOCOLS.map((p) => (
              <option key={p} value={p}>
                {p}
              </option>
            ))}
          </Select>
        </label>

        <label className="flex flex-col gap-1">
          <span className="text-xs text-rapido-muted">{t("rapido.inbounds.network")}</span>
          <Select dir="ltr" value={network} onChange={(e) => setNetwork(e.target.value as InboundNetwork)}>
            {NETWORKS.map((n) => (
              <option key={n} value={n}>
                {n}
              </option>
            ))}
          </Select>
        </label>

        <label className="flex flex-col gap-1">
          <span className="text-xs text-rapido-muted">{t("rapido.inbounds.headerType")}</span>
          <Input dir="ltr" value={headerType} onChange={(e) => setHeaderType(e.target.value)} placeholder="http" />
        </label>

        <label className="flex flex-col gap-1">
          <span className="text-xs text-rapido-muted">{t("hostsDialog.security")}</span>
          <Select dir="ltr" value={security} onChange={(e) => setSecurity(e.target.value as InboundSecurity)}>
            {SECURITIES.map((s) => (
              <option key={s} value={s}>
                {s}
              </option>
            ))}
          </Select>
        </label>

        {security === "reality" && (
          <div className="flex flex-col gap-3 rounded-lg border border-rapido-border p-3">
            <label className="flex flex-col gap-1">
              <span className="text-xs text-rapido-muted">{t("rapido.inbounds.realityPrivateKey")}</span>
              <Input
                dir="ltr"
                value={realityPrivateKey}
                onChange={(e) => setRealityPrivateKey(e.target.value)}
              />
            </label>
            <label className="flex flex-col gap-1">
              <span className="text-xs text-rapido-muted">{t("rapido.inbounds.realityShortIds")}</span>
              <Input
                dir="ltr"
                value={realityShortIDs}
                onChange={(e) => setRealityShortIDs(e.target.value)}
                placeholder="ab12, cd34"
              />
            </label>
            <label className="flex flex-col gap-1">
              <span className="text-xs text-rapido-muted">{t("rapido.inbounds.realityServerName")}</span>
              <Input
                dir="ltr"
                value={realityServerName}
                onChange={(e) => setRealityServerName(e.target.value)}
                placeholder="example.com"
              />
            </label>
            <label className="flex flex-col gap-1">
              <span className="text-xs text-rapido-muted">{t("rapido.inbounds.realityServerPort")}</span>
              <Input
                dir="ltr"
                inputMode="numeric"
                value={realityServerPort}
                onChange={(e) => setRealityServerPort(e.target.value.replace(/[^0-9]/g, ""))}
                placeholder="443"
              />
            </label>
          </div>
        )}

        {security === "tls" && (
          <div className="flex flex-col gap-3 rounded-lg border border-rapido-border p-3">
            <label className="flex flex-col gap-1">
              <span className="text-xs text-rapido-muted">{t("rapido.inbounds.tlsServerName")}</span>
              <Input
                dir="ltr"
                value={tlsServerName}
                onChange={(e) => setTlsServerName(e.target.value)}
                placeholder="example.com"
              />
            </label>
            <label className="flex flex-col gap-1">
              <span className="text-xs text-rapido-muted">{t("rapido.inbounds.tlsCertificate")}</span>
              <textarea
                dir="ltr"
                rows={6}
                value={tlsCertificate}
                onChange={(e) => setTlsCertificate(e.target.value)}
                placeholder="-----BEGIN CERTIFICATE-----"
                className="w-full resize-y rounded-lg border border-rapido-border bg-rapido-bg px-3 py-2 font-mono text-xs text-rapido-text focus:outline-none focus:ring-1 focus:ring-rapido-accent"
              />
            </label>
            <label className="flex flex-col gap-1">
              <span className="text-xs text-rapido-muted">{t("rapido.inbounds.tlsKey")}</span>
              <textarea
                dir="ltr"
                rows={6}
                value={tlsKey}
                onChange={(e) => setTlsKey(e.target.value)}
                placeholder="-----BEGIN PRIVATE KEY-----"
                className="w-full resize-y rounded-lg border border-rapido-border bg-rapido-bg px-3 py-2 font-mono text-xs text-rapido-text focus:outline-none focus:ring-1 focus:ring-rapido-accent"
              />
            </label>
            {(!tlsCertificate || !tlsKey) && (
              <p className="text-xs text-amber-400">{t("rapido.inbounds.tlsNoCertWarning")}</p>
            )}
          </div>
        )}

        {error && (
          <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-400">
            {error}
          </div>
        )}

        <div className="mt-2 flex justify-end gap-2">
          <Button variant="chip" onClick={onClose}>
            {t("cancel")}
          </Button>
          <Button
            variant="chip"
            tone="accent"
            disabled={syncInbound.isPending || !canSubmit}
            onClick={submit}
          >
            {syncInbound.isPending ? t("rapido.pleaseWait") : t("rapido.inbounds.save")}
          </Button>
        </div>
      </div>
    </Modal>
  );
};

// ---------------------------------------------------------------------------

const InboundCard: FC<{ row: Inbound; onEdit: () => void }> = ({ row, onEdit }) => {
  const { t } = useTranslation();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [msg, setMsg] = useState<{ tone: "ok" | "err"; text: string } | null>(null);
  const deleteInbound = useDeleteInboundMutation();

  const remove = () => {
    setMsg(null);
    deleteInbound.mutate(row.tag, {
      onSuccess: () => setMsg({ tone: "ok", text: t("rapido.inbounds.deleted") }),
      onError: (e) => {
        setMsg({ tone: "err", text: errorText(e, t("rapido.inbounds.actionFailed")) });
        setConfirmDelete(false);
      },
    });
  };

  return (
    <Card className="p-4">
      <div className="flex flex-col gap-3">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex min-w-0 flex-wrap items-center gap-2">
            <span className="truncate text-sm font-semibold" dir="ltr">
              {row.tag}
            </span>
            <Badge tone="brand">{row.protocol}</Badge>
            <Badge tone="sky" dir="ltr">
              {row.network}
            </Badge>
            <Badge tone={row.security === "none" ? "gray" : "orange"} dir="ltr">
              {row.security}
            </Badge>
          </div>
        </div>

        {row.security === "reality" && (
          <div className="flex flex-wrap gap-x-6 gap-y-1 text-xs text-rapido-muted">
            {row.reality_server_name && (
              <span dir="ltr">
                SNI: <span className="text-rapido-text">{row.reality_server_name}</span>
              </span>
            )}
            {!!row.reality_server_port && (
              <span dir="ltr">
                Port: <span className="text-rapido-text">{row.reality_server_port}</span>
              </span>
            )}
          </div>
        )}

        {row.security === "tls" &&
          (!row.tls_certificate || !row.tls_key ? (
            <p className="text-xs text-amber-400">{t("rapido.inbounds.tlsNoCertWarning")}</p>
          ) : (
            row.tls_server_name && (
              <span className="text-xs text-rapido-muted" dir="ltr">
                SNI: <span className="text-rapido-text">{row.tls_server_name}</span>
              </span>
            )
          ))}

        {msg && (
          <div
            className={classNames(
              "rounded-lg border px-3 py-2 text-xs",
              msg.tone === "ok"
                ? "border-emerald-500/30 bg-emerald-500/10 text-emerald-400"
                : "border-red-500/30 bg-red-500/10 text-red-400"
            )}
          >
            {msg.text}
          </div>
        )}

        <div className="flex flex-wrap items-center gap-1.5">
          {confirmDelete ? (
            <>
              <span className="text-xs text-red-400">{t("rapido.inbounds.deleteWarning")}</span>
              <Button variant="chip" tone="red" disabled={deleteInbound.isPending} onClick={remove}>
                {t("delete")}
              </Button>
              <Button variant="chip" onClick={() => setConfirmDelete(false)}>
                {t("cancel")}
              </Button>
            </>
          ) : (
            <>
              <Button variant="chip" onClick={onEdit}>
                {t("rapido.inbounds.edit")}
              </Button>
              <Button variant="chip" tone="red" onClick={() => setConfirmDelete(true)}>
                {t("delete")}
              </Button>
            </>
          )}
        </div>
      </div>
    </Card>
  );
};

// ---------------------------------------------------------------------------

// XrayImportModal is a two-step preview-then-apply flow around POST
// /api/inbounds/import-xray: "Preview" (confirm:false) always runs first
// and shows the parsed counts/warnings without writing anything, and only
// once that's happened does "Apply" (confirm:true) - re-running the exact
// same parse server-side, not reusing the preview's client-side result -
// become available. Re-editing the pasted text after a preview resets
// back to preview-only, so Apply can never fire against text the admin
// hasn't actually previewed.
const XrayImportModal: FC<{ onClose: () => void; onApplied: () => void }> = ({ onClose, onApplied }) => {
  const { t } = useTranslation();
  const [config, setConfig] = useState("");
  const [preview, setPreview] = useState<XrayImportResult | null>(null);
  const [applied, setApplied] = useState<XrayImportResult | null>(null);
  const [error, setError] = useState("");
  const importXray = useImportXrayConfigMutation();

  const runPreview = () => {
    setError("");
    setApplied(null);
    importXray.mutate(
      { config, confirm: false },
      {
        onSuccess: (result) => setPreview(result),
        onError: (e) => setError(errorText(e, t("rapido.inbounds.xrayImportFailed"))),
      }
    );
  };

  const runApply = () => {
    setError("");
    importXray.mutate(
      { config, confirm: true },
      {
        onSuccess: (result) => {
          setApplied(result);
          onApplied();
        },
        onError: (e) => setError(errorText(e, t("rapido.inbounds.xrayImportFailed"))),
      }
    );
  };

  const summary = applied ?? preview;

  return (
    <Modal onClose={onClose} className="max-w-2xl">
      <h2 className="mb-1 text-lg font-semibold">{t("rapido.inbounds.xrayImportTitle")}</h2>
      <p className="mb-4 text-xs text-rapido-muted">{t("rapido.inbounds.xrayImportSubtitle")}</p>
      <div className="flex flex-col gap-3">
        <textarea
          dir="ltr"
          rows={12}
          value={config}
          onChange={(e) => {
            setConfig(e.target.value);
            setPreview(null);
            setApplied(null);
          }}
          placeholder='{ "inbounds": [...], "outbounds": [...], "routing": {...} }'
          className="w-full resize-y rounded-lg border border-rapido-border bg-rapido-bg px-3 py-2 font-mono text-xs text-rapido-text focus:outline-none focus:ring-1 focus:ring-rapido-accent"
        />

        {summary && (
          <div className="rounded-lg border border-rapido-border p-3 text-xs">
            <div className="mb-2 flex flex-wrap gap-x-4 gap-y-1 font-medium">
              <span>{t("rapido.inbounds.xrayImportInbounds", { count: summary.inbounds_created })}</span>
              <span>{t("rapido.inbounds.xrayImportOutbounds", { count: summary.outbounds_saved })}</span>
              <span>{t("rapido.inbounds.xrayImportRules", { count: summary.routing_rules_saved })}</span>
              <span>{t("rapido.inbounds.xrayImportDns", { count: summary.dns_servers_saved })}</span>
            </div>
            {summary.warnings.length > 0 && (
              <ul className="list-inside list-disc space-y-0.5 text-amber-400">
                {summary.warnings.map((w, i) => (
                  <li key={i}>{w}</li>
                ))}
              </ul>
            )}
            {applied && <p className="mt-2 text-emerald-400">{t("rapido.inbounds.xrayImportApplied")}</p>}
          </div>
        )}

        {error && (
          <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-400">
            {error}
          </div>
        )}

        <div className="mt-2 flex justify-end gap-2">
          <Button variant="chip" onClick={onClose}>
            {applied ? t("rapido.close") : t("cancel")}
          </Button>
          {!applied && (
            <>
              <Button variant="chip" tone="accent" disabled={!config.trim() || importXray.isPending} onClick={runPreview}>
                {importXray.isPending && !preview ? t("rapido.pleaseWait") : t("rapido.inbounds.xrayImportPreview")}
              </Button>
              {preview && (
                <Button variant="chip" tone="accent" disabled={importXray.isPending} onClick={runApply}>
                  {importXray.isPending ? t("rapido.pleaseWait") : t("rapido.inbounds.xrayImportApply")}
                </Button>
              )}
            </>
          )}
        </div>
      </div>
    </Modal>
  );
};

// ---------------------------------------------------------------------------

// parseFullInboundsJSON is deliberately strict about the one thing that
// actually matters (this must be an array, so the sync request below can't
// silently send garbage) and leaves everything else to the same server-side
// validation every other save on this page already goes through.
const parseFullInboundsJSON = (text: string): InboundSyncEntry[] => {
  const parsed = JSON.parse(text);
  if (!Array.isArray(parsed)) {
    throw new Error("rapido.inbounds.fullJsonMustBeArray");
  }
  return parsed;
};

// Every inbound as one editable JSON array - Apply sends the whole array
// through the same upsert-by-tag sync endpoint every other action on this
// page already uses (see useSyncInboundsBulkMutation), so nothing here can
// do anything a one-at-a-time edit couldn't already do. Two things it
// deliberately does NOT do, both called out in the UI copy rather than
// silently assumed: it never deletes a tag that got removed from the JSON
// (the sync endpoint only ever upserts - each card's own Delete button is
// still the way to remove one), and Revert is a single level of undo (the
// text right before the most recent Apply), not a full history.
const FullInboundsJSONCard: FC<{ rows: Inbound[] }> = ({ rows }) => {
  const { t } = useTranslation();
  const [expanded, setExpanded] = useState(false);
  const [text, setText] = useState(() => JSON.stringify(rows, null, 2));
  const [dirty, setDirty] = useState(false);
  const [error, setError] = useState("");
  const [previousText, setPreviousText] = useState<string | null>(null);
  const syncBulk = useSyncInboundsBulkMutation();

  useEffect(() => {
    if (!dirty) setText(JSON.stringify(rows, null, 2));
  }, [rows, dirty]);

  const apply = () => {
    setError("");
    try {
      const entries = parseFullInboundsJSON(text);
      const before = JSON.stringify(rows, null, 2);
      syncBulk.mutate(entries, {
        onSuccess: () => {
          setPreviousText(before);
          setDirty(false);
        },
        onError: (e) => setError(errorText(e, t("rapido.inbounds.fullJsonApplyFailed"))),
      });
    } catch (e) {
      setError(e instanceof Error ? t(e.message) : String(e));
    }
  };

  const discard = () => {
    setDirty(false);
    setError("");
    setText(JSON.stringify(rows, null, 2));
  };

  const revert = () => {
    if (!previousText) return;
    setError("");
    try {
      const entries = parseFullInboundsJSON(previousText);
      syncBulk.mutate(entries, {
        onSuccess: () => {
          setPreviousText(null);
          setDirty(false);
        },
        onError: (e) => setError(errorText(e, t("rapido.inbounds.revertFailed"))),
      });
    } catch (e) {
      setError(e instanceof Error ? t(e.message) : String(e));
    }
  };

  return (
    <Card className="p-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div>
          <h3 className="text-sm font-semibold">{t("rapido.inbounds.fullJsonTitle")}</h3>
          <p className="text-xs text-rapido-muted">{t("rapido.inbounds.fullJsonDesc")}</p>
        </div>
        <Button variant="chip" onClick={() => setExpanded((v) => !v)}>
          {expanded ? t("rapido.inbounds.fullJsonHide") : t("rapido.inbounds.fullJsonShow")}
        </Button>
      </div>
      {expanded && (
        <div className="mt-3 flex flex-col gap-2">
          <textarea
            dir="ltr"
            spellCheck={false}
            rows={16}
            className="w-full resize-y rounded-lg border border-rapido-border bg-rapido-bg px-3 py-2 font-mono text-xs text-rapido-text focus:outline-none focus:ring-1 focus:ring-rapido-accent"
            value={text}
            onChange={(e) => {
              setText(e.target.value);
              setDirty(true);
              setError("");
            }}
          />
          {error && (
            <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-400">
              {error}
            </div>
          )}
          <div className="flex flex-wrap items-center justify-between gap-2">
            <span className="text-xs text-rapido-muted">
              {dirty ? t("rapido.inbounds.fullJsonUnapplied") : t("rapido.inbounds.fullJsonInSync")}
            </span>
            <div className="flex gap-2">
              <Button variant="chip" disabled={!dirty} onClick={discard}>
                {t("rapido.inbounds.fullJsonDiscard")}
              </Button>
              <Button variant="chip" tone="accent" disabled={!dirty || syncBulk.isPending} onClick={apply}>
                {syncBulk.isPending ? t("rapido.pleaseWait") : t("rapido.inbounds.fullJsonApply")}
              </Button>
            </div>
          </div>
          {!dirty && previousText && (
            <div className="flex flex-wrap items-center justify-between gap-2 rounded-lg border border-rapido-border p-2">
              <div>
                <p className="text-xs text-rapido-text">{t("rapido.inbounds.previousVersionAvailable")}</p>
                <p className="text-xs text-rapido-muted">{t("rapido.inbounds.revertNote")}</p>
              </div>
              <Button variant="chip" disabled={syncBulk.isPending} onClick={revert}>
                {t("rapido.inbounds.revert")}
              </Button>
            </div>
          )}
        </div>
      )}
    </Card>
  );
};

// ---------------------------------------------------------------------------

export const InboundsAdmin: FC = () => {
  const { t } = useTranslation();
  const { data: rows, isLoading, isError } = useInboundsDetailQuery();
  // false = closed, "new" = the add form, an Inbound = editing that row.
  const [inboundForm, setInboundForm] = useState<false | "new" | Inbound>(false);
  const [importing, setImporting] = useState(false);

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="text-sm text-rapido-muted">
          {t("rapido.inbounds.summary", { count: rows?.length ?? 0 })}
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <Button variant="chip" onClick={() => setImporting(true)}>
            {t("rapido.inbounds.xrayImportTitle")}
          </Button>
          <Button variant="chip" tone="accent" onClick={() => setInboundForm("new")}>
            + {t("rapido.inbounds.addTitle")}
          </Button>
        </div>
      </div>

      {isError && (
        <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-400">
          {t("rapido.inbounds.loadFailed")}
        </div>
      )}

      {isLoading ? (
        <p className="text-sm text-rapido-muted">{t("rapido.tickets.loading")}</p>
      ) : (rows ?? []).length === 0 ? (
        <Card className="p-6 text-center text-sm text-rapido-muted">{t("rapido.inbounds.empty")}</Card>
      ) : (
        <div className="grid gap-3 lg:grid-cols-2">
          {(rows ?? []).map((row) => (
            <InboundCard key={row.tag} row={row} onEdit={() => setInboundForm(row)} />
          ))}
        </div>
      )}

      <FullInboundsJSONCard rows={rows ?? []} />

      {inboundForm !== false && (
        <InboundForm
          initial={inboundForm === "new" ? null : inboundForm}
          onClose={() => setInboundForm(false)}
        />
      )}
      {importing && (
        <XrayImportModal onClose={() => setImporting(false)} onApplied={() => {}} />
      )}
    </div>
  );
};

export default InboundsAdmin;
