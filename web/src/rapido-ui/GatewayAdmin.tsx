import { FC, useEffect, useState } from "react";
import { useTranslation } from "react-i18next";
import classNames from "classnames";
import {
  useCreateGatewayPeerMutation,
  useDeleteGatewayPeerMutation,
  useGatewayPeersQuery,
  useGatewaySettingsQuery,
  useTestGatewayPeerMutation,
  useUpdateGatewayPeerMutation,
  useUpdateGatewaySettingsMutation,
} from "hooks/useGatewayQuery";
import { GatewayPeer } from "types/Gateway";
import { errorText } from "service/errors";
import { Card, CardSubtitle, CardTitle } from "rapido-ui/Card";
import { Badge } from "rapido-ui/Badge";
import { Button } from "rapido-ui/Button";
import { Input } from "rapido-ui/Input";

// Same shape as NodesAdmin.tsx's own CopyField (not exported from there,
// so duplicated rather than reached across files for one small component -
// matches this codebase's existing convention of small per-file UI
// helpers, e.g. HostsAdmin.tsx's own `field` class string).
const CopyField: FC<{ label: string; value: string }> = ({ label, value }) => {
  const { t } = useTranslation();
  const [copied, setCopied] = useState(false);

  const copy = () => {
    navigator.clipboard?.writeText(value).then(
      () => {
        setCopied(true);
        window.setTimeout(() => setCopied(false), 2000);
      },
      () => undefined
    );
  };

  return (
    <div className="flex flex-col gap-1">
      <span className="text-xs font-medium text-rapido-text">{label}</span>
      <textarea
        readOnly
        dir="ltr"
        spellCheck={false}
        rows={2}
        className="w-full resize-none rounded-lg border border-rapido-border bg-rapido-bg px-3 py-2 font-mono text-xs text-rapido-text focus:outline-none focus:ring-1 focus:ring-rapido-accent"
        value={value}
        onFocus={(e) => e.currentTarget.select()}
      />
      <Button variant="chip" tone={copied ? "accent" : "neutral"} className="self-start" onClick={copy}>
        {copied ? t("usersTable.copied") : t("rapido.copy")}
      </Button>
    </div>
  );
};

// ---------------------------------------------------------------------------

// This panel's own identity - what an admin copies into a PEER panel's
// "add peer" form. Deliberately shown in full (see types/Gateway.ts's own
// doc comment) - masking would defeat the only thing this card exists for.
const ThisPanelCard: FC = () => {
  const { t } = useTranslation();
  const { data: settings, isLoading } = useGatewaySettingsQuery();
  const [name, setName] = useState("");
  const [confirmRotate, setConfirmRotate] = useState(false);
  const update = useUpdateGatewaySettingsMutation();
  const [error, setError] = useState("");

  useEffect(() => {
    if (settings) setName(settings.name);
  }, [settings]);

  const nameDirty = settings !== undefined && name !== settings?.name;

  const saveName = () => {
    setError("");
    update.mutate({ name }, { onError: (e) => setError(errorText(e, t("rapido.gateway.saveFailed"))) });
  };

  const rotate = () => {
    setError("");
    setConfirmRotate(false);
    update.mutate({ rotate_secret: true }, { onError: (e) => setError(errorText(e, t("rapido.gateway.saveFailed"))) });
  };

  if (isLoading || !settings) {
    return <p className="text-sm text-rapido-muted">{t("rapido.tickets.loading")}</p>;
  }

  return (
    <Card className="p-4">
      <CardTitle>{t("rapido.gateway.thisPanelTitle")}</CardTitle>
      <CardSubtitle className="mb-3">{t("rapido.gateway.thisPanelDesc")}</CardSubtitle>

      <div className="flex flex-col gap-3">
        <label className="flex flex-col gap-1">
          <span className="text-xs text-rapido-muted">{t("rapido.gateway.panelName")}</span>
          <div className="flex flex-wrap items-center gap-2">
            <Input
              dir="ltr"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={t("rapido.gateway.panelNamePlaceholder")}
              className="max-w-xs"
            />
            <Button variant="chip" tone="accent" disabled={!nameDirty || update.isPending} onClick={saveName}>
              {t("rapido.gateway.saveName")}
            </Button>
          </div>
        </label>

        <CopyField label={t("rapido.gateway.thisSecret")} value={settings.secret} />

        {confirmRotate ? (
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-xs text-red-400">{t("rapido.gateway.rotateWarning")}</span>
            <Button variant="chip" tone="red" disabled={update.isPending} onClick={rotate}>
              {t("rapido.gateway.rotateConfirm")}
            </Button>
            <Button variant="chip" onClick={() => setConfirmRotate(false)}>
              {t("cancel")}
            </Button>
          </div>
        ) : (
          <Button variant="chip" tone="amber" className="self-start" onClick={() => setConfirmRotate(true)}>
            {t("rapido.gateway.rotateSecret")}
          </Button>
        )}

        {error && (
          <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-400">
            {error}
          </div>
        )}
      </div>
    </Card>
  );
};

// ---------------------------------------------------------------------------

const PeerForm: FC<{ initial?: GatewayPeer; onClose: () => void }> = ({ initial, onClose }) => {
  const { t } = useTranslation();
  const editing = !!initial;
  const [name, setName] = useState(initial?.name ?? "");
  const [baseURL, setBaseURL] = useState(initial?.base_url ?? "");
  const [secret, setSecret] = useState(initial?.secret ?? "");
  const [error, setError] = useState("");
  const create = useCreateGatewayPeerMutation();
  const update = useUpdateGatewayPeerMutation();
  const pending = create.isPending || update.isPending;

  const submit = () => {
    setError("");
    const body = { name, base_url: baseURL, secret, enabled: initial?.enabled ?? true };
    const onError = (e: unknown) => setError(errorText(e, t("rapido.gateway.saveFailed")));
    if (editing) {
      update.mutate({ id: initial!.id, body }, { onSuccess: onClose, onError });
    } else {
      create.mutate(body, { onSuccess: onClose, onError });
    }
  };

  const canSubmit = !!name && !!baseURL && !!secret;

  return (
    <Card className="p-4">
      <CardTitle>{editing ? t("rapido.gateway.editPeer") : t("rapido.gateway.addPeer")}</CardTitle>
      <div className="mt-3 flex flex-col gap-3">
        <label className="flex flex-col gap-1">
          <span className="text-xs text-rapido-muted">{t("rapido.gateway.peerName")}</span>
          <Input dir="ltr" value={name} onChange={(e) => setName(e.target.value)} placeholder="EU Panel" />
        </label>
        <label className="flex flex-col gap-1">
          <span className="text-xs text-rapido-muted">{t("rapido.gateway.peerBaseUrl")}</span>
          <Input
            dir="ltr"
            value={baseURL}
            onChange={(e) => setBaseURL(e.target.value)}
            placeholder="https://peer-panel.example.com"
          />
        </label>
        <label className="flex flex-col gap-1">
          <span className="text-xs text-rapido-muted">{t("rapido.gateway.peerSecret")}</span>
          <Input
            dir="ltr"
            value={secret}
            onChange={(e) => setSecret(e.target.value)}
            placeholder={t("rapido.gateway.peerSecretPlaceholder")}
          />
          <span className="text-xs text-rapido-muted">{t("rapido.gateway.peerSecretHint")}</span>
        </label>

        {error && (
          <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-400">
            {error}
          </div>
        )}

        <div className="flex justify-end gap-2">
          <Button variant="chip" onClick={onClose}>
            {t("cancel")}
          </Button>
          <Button variant="chip" tone="accent" disabled={!canSubmit || pending} onClick={submit}>
            {pending ? t("rapido.pleaseWait") : t("rapido.gateway.save")}
          </Button>
        </div>
      </div>
    </Card>
  );
};

// ---------------------------------------------------------------------------

const PeerRow: FC<{ peer: GatewayPeer; onEdit: () => void }> = ({ peer, onEdit }) => {
  const { t } = useTranslation();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const [testResult, setTestResult] = useState<{ ok: boolean; text: string } | null>(null);
  const deletePeer = useDeleteGatewayPeerMutation();
  const testPeer = useTestGatewayPeerMutation();

  const runTest = () => {
    setTestResult(null);
    testPeer.mutate(peer.id, {
      onSuccess: (result) =>
        setTestResult({
          ok: result.ok,
          text: result.ok
            ? t("rapido.gateway.testOk", { name: result.panel_name || peer.name })
            : t("rapido.gateway.testFailed", { detail: result.detail || "" }),
        }),
      onError: (e) => setTestResult({ ok: false, text: errorText(e, t("rapido.gateway.testFailed", { detail: "" })) }),
    });
  };

  return (
    <Card className="p-4">
      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="flex min-w-0 flex-wrap items-center gap-2">
          <span className="truncate text-sm font-semibold">{peer.name}</span>
          <Badge tone="sky" dir="ltr">
            {peer.base_url}
          </Badge>
          {!peer.enabled && <Badge tone="gray">{t("rapido.gateway.disabled")}</Badge>}
        </div>
      </div>

      {testResult && (
        <div
          className={classNames(
            "mt-2 rounded-lg border px-3 py-2 text-xs",
            testResult.ok
              ? "border-emerald-500/30 bg-emerald-500/10 text-emerald-400"
              : "border-red-500/30 bg-red-500/10 text-red-400"
          )}
        >
          {testResult.text}
        </div>
      )}

      <div className="mt-3 flex flex-wrap items-center gap-1.5">
        <Button variant="chip" disabled={testPeer.isPending} onClick={runTest}>
          {testPeer.isPending ? t("rapido.pleaseWait") : t("rapido.gateway.testConnection")}
        </Button>
        <Button variant="chip" onClick={onEdit}>
          {t("rapido.gateway.edit")}
        </Button>
        {confirmDelete ? (
          <>
            <span className="text-xs text-red-400">{t("rapido.gateway.deleteWarning")}</span>
            <Button variant="chip" tone="red" disabled={deletePeer.isPending} onClick={() => deletePeer.mutate(peer.id)}>
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
    </Card>
  );
};

// ---------------------------------------------------------------------------

export const GatewayAdmin: FC = () => {
  const { t } = useTranslation();
  const { data: peers, isLoading, isError } = useGatewayPeersQuery();
  const [formTarget, setFormTarget] = useState<false | "new" | GatewayPeer>(false);

  return (
    <div className="flex flex-col gap-4">
      <ThisPanelCard />

      <div className="flex flex-wrap items-center justify-between gap-2">
        <div className="text-sm text-rapido-muted">{t("rapido.gateway.peersSummary", { count: peers?.length ?? 0 })}</div>
        <Button variant="chip" tone="accent" onClick={() => setFormTarget("new")}>
          + {t("rapido.gateway.addPeer")}
        </Button>
      </div>

      {isError && (
        <div className="rounded-lg border border-red-500/30 bg-red-500/10 px-3 py-2 text-sm text-red-400">
          {t("rapido.gateway.loadFailed")}
        </div>
      )}

      {formTarget !== false && (
        <PeerForm initial={formTarget === "new" ? undefined : formTarget} onClose={() => setFormTarget(false)} />
      )}

      {isLoading ? (
        <p className="text-sm text-rapido-muted">{t("rapido.tickets.loading")}</p>
      ) : (peers ?? []).length === 0 ? (
        <Card className="p-6 text-center text-sm text-rapido-muted">{t("rapido.gateway.empty")}</Card>
      ) : (
        <div className="flex flex-col gap-3">
          {(peers ?? []).map((p) => (
            <PeerRow key={p.id} peer={p} onEdit={() => setFormTarget(p)} />
          ))}
        </div>
      )}
    </div>
  );
};

export default GatewayAdmin;
