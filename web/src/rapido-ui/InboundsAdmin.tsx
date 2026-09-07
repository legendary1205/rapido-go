import { FC, useState } from "react";
import { useTranslation } from "react-i18next";
import classNames from "classnames";
import {
  useDeleteInboundMutation,
  useInboundsDetailQuery,
  useSyncInboundMutation,
} from "hooks/useInboundsQuery";
import { Inbound, InboundNetwork, InboundSecurity } from "types/Inbound";
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

const InboundForm: FC<{ onClose: () => void }> = ({ onClose }) => {
  const { t } = useTranslation();
  const [tag, setTag] = useState("");
  const [protocol, setProtocol] = useState(PROTOCOLS[0]);
  const [network, setNetwork] = useState<InboundNetwork>("tcp");
  const [headerType, setHeaderType] = useState("");
  const [security, setSecurity] = useState<InboundSecurity>("none");
  const [realityPrivateKey, setRealityPrivateKey] = useState("");
  const [realityShortIDs, setRealityShortIDs] = useState("");
  const [realityServerName, setRealityServerName] = useState("");
  const [realityServerPort, setRealityServerPort] = useState("");
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
      })
      .then(onClose)
      .catch((e) => setError(errorText(e, t("rapido.inbounds.saveFailed"))));
  };

  const canSubmit = !!tag && !!protocol;

  return (
    <Modal onClose={onClose} className="max-w-md">
      <h2 className="mb-4 text-lg font-semibold">{t("rapido.inbounds.addTitle")}</h2>
      <div className="flex flex-col gap-3">
        <label className="flex flex-col gap-1">
          <span className="text-xs text-rapido-muted">{t("rapido.inbounds.tag")}</span>
          <Input dir="ltr" value={tag} onChange={(e) => setTag(e.target.value)} placeholder="VLESS TCP" />
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

const InboundCard: FC<{ row: Inbound }> = ({ row }) => {
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
            <Button variant="chip" tone="red" onClick={() => setConfirmDelete(true)}>
              {t("delete")}
            </Button>
          )}
        </div>
      </div>
    </Card>
  );
};

// ---------------------------------------------------------------------------

export const InboundsAdmin: FC = () => {
  const { t } = useTranslation();
  const { data: rows, isLoading, isError } = useInboundsDetailQuery();
  const [adding, setAdding] = useState(false);

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="text-sm text-rapido-muted">
          {t("rapido.inbounds.summary", { count: rows?.length ?? 0 })}
        </div>
        <Button variant="chip" tone="accent" onClick={() => setAdding(true)}>
          + {t("rapido.inbounds.addTitle")}
        </Button>
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
            <InboundCard key={row.tag} row={row} />
          ))}
        </div>
      )}

      {adding && <InboundForm onClose={() => setAdding(false)} />}
    </div>
  );
};

export default InboundsAdmin;
